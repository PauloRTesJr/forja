"""UniRig (MIT), VAST-AI-Research: a skeleton for any mesh, then skin weights for it.

Upstream's scripts read and write meshes through Blender's Python module and run each half as
its own process. Here both models run in the family's worker: the low-poly goes in as
upstream's raw_data.npz, the skeleton and weights come back in upstream's normalized frame,
and Forja writes the skinned GLB itself on the mesh it was given, so UVs, materials and
vertex order stay as baked. Upstream's code is fetched at install time and put on the path;
its package is named `src`. Weights come from VAST-AI/UniRig at a pinned revision.

Upstream reads meshes as Blender holds them, Z up; Forja's are glTF's, Y up.
"""

from __future__ import annotations

import contextlib
import gc
import importlib.util
import json
import os
import random
import tempfile
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import fast_simplification
import numpy as np
import trimesh
from numpy.typing import NDArray

from forja_engine import flash_compat
from forja_engine.ops import mesh as mesh_ops
from forja_engine.ops import rig as rig_ops
from forja_engine.ops import skin as skin_ops
from forja_engine.sdk import (
    Adapter,
    ArtifactKind,
    Capabilities,
    Device,
    StageContext,
    StageOutput,
    StageType,
)

GB = 1 << 30
SKELETON_TASK = "configs/task/quick_inference_skeleton_articulationxl_ar_256.yaml"
SKIN_TASK = "configs/task/quick_inference_unirig_skin.yaml"
SKELETON_CKPT = "skeleton/articulation-xl_quantization_256/model.ckpt"
SKIN_CKPT = "skin/articulation-xl/model.ckpt"
# Upstream's extract step decimates a mesh above this many faces before predicting.
FACES = 50_000
#: OPT-350m's architecture. Upstream builds its skeleton model on that Hub config, which would
#: reach the network at every load; its model YAML overrides the sizes that differ.
OPT_350M = {
    "model_type": "opt",
    "activation_function": "relu",
    "activation_dropout": 0.0,
    "attention_dropout": 0.0,
    "dropout": 0.1,
    "layerdrop": 0.0,
    "init_std": 0.02,
    "ffn_dim": 4096,
    "hidden_size": 1024,
    "num_attention_heads": 16,
    "num_hidden_layers": 24,
    "max_position_embeddings": 2048,
    "word_embed_proj_dim": 512,
    "do_layer_norm_before": False,
    "enable_bias": True,
    "layer_norm_elementwise_affine": True,
    "vocab_size": 50272,
    "bos_token_id": 2,
    "eos_token_id": 2,
    "pad_token_id": 1,
}


def to_blender(v: NDArray[np.floating[Any]]) -> NDArray[np.float64]:
    x, y, z = np.asarray(v, dtype=np.float64).T
    return np.stack([x, -z, y], axis=1)


def from_blender(v: NDArray[np.floating[Any]]) -> NDArray[np.float64]:
    x, y, z = np.asarray(v, dtype=np.float64).T
    return np.stack([x, z, -y], axis=1)


def upstream_root() -> Path:
    spec = importlib.util.find_spec("src")
    if spec is None or not spec.submodule_search_locations:
        raise RuntimeError("unirig: its source is not on the path; run forja models install unirig")
    return Path(next(iter(spec.submodule_search_locations))).parent


@contextlib.contextmanager
def inside(folder: Path) -> Iterator[None]:
    """Upstream opens its configs by paths relative to its own folder."""
    before = Path.cwd()
    os.chdir(folder)
    try:
        yield
    finally:
        os.chdir(before)


def frame_change(
    src: NDArray[np.float64], dst: NDArray[np.floating[Any]]
) -> tuple[NDArray[np.float64], float, NDArray[np.float64]]:
    """(center, scale, center') with dst = (src - center) * scale + center'.

    Upstream normalizes a mesh into a cube by its bounds, without rotating it.
    """
    lo, hi = src.min(axis=0), src.max(axis=0)
    dlo, dhi = np.asarray(dst).min(axis=0), np.asarray(dst).max(axis=0)
    scale = float(np.max(dhi - dlo) / max(float(np.max(hi - lo)), 1e-12))
    return (lo + hi) / 2, scale, (dlo + dhi) / 2


def seed_everything(seed: int, torch: Any) -> None:
    random.seed(seed)
    np.random.seed(seed % (1 << 32))
    torch.manual_seed(seed)


@dataclass
class _Part:
    """One of upstream's two models with what it reads its input through."""

    system: Any
    model: Any
    tokenizer: Any
    transform: Any


class UniRig(Adapter):
    version = "1"

    def __init__(self) -> None:
        self.torch: Any = None
        self.root: Path | None = None
        self.skeleton: _Part | None = None
        self.skinner: _Part | None = None
        self._config_dir: tempfile.TemporaryDirectory[str] | None = None

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.RIG,), devices=(Device.CUDA,), vram_bytes=8 * GB)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        import torch

        if self.weights_dir is None:
            raise RuntimeError("unirig: no weights folder; run forja models install unirig")
        os.environ.setdefault("HF_HUB_OFFLINE", "1")
        flash_compat.install()
        self.torch = torch
        self.root = upstream_root()
        self._config_dir = tempfile.TemporaryDirectory(prefix="forja-unirig-")
        opt = Path(self._config_dir.name) / "opt-350m"
        opt.mkdir()
        (opt / "config.json").write_text(json.dumps(OPT_350M), encoding="utf-8")
        # Both models wait in RAM and take the GPU in turn, so a run needs only the larger one.
        with inside(self.root):
            self.skeleton = self._part(SKELETON_TASK, self.weights_dir / SKELETON_CKPT, opt)
            self.skinner = self._part(SKIN_TASK, self.weights_dir / SKIN_CKPT, opt)

    def _part(self, task_file: str, checkpoint: Path, opt: Path) -> _Part:
        import yaml
        from box import Box
        from src.data.transform import TransformConfig
        from src.model.parse import get_model
        from src.system.parse import get_system
        from src.tokenizer.parse import get_tokenizer
        from src.tokenizer.spec import TokenizerConfig

        def read(path: str) -> Any:
            return Box(yaml.safe_load(Path(path).read_text(encoding="utf-8")))

        parts = read(task_file).components
        transform = read(f"configs/transform/{parts.transform}.yaml").predict_transform_config
        groups = transform.get("vertex_group_config") or {}
        voxel = (groups.get("kwargs") or {}).get("voxel_skin")
        if voxel is not None:
            voxel["backend"] = "open3d"  # pyrender would need an OpenGL context
        tokenizer = None
        if parts.get("tokenizer"):
            spec = read(f"configs/tokenizer/{parts.tokenizer}.yaml")
            tokenizer = get_tokenizer(config=TokenizerConfig.parse(config=spec))
        model_config = read(f"configs/model/{parts.model}.yaml")
        if "llm" in model_config:
            model_config.llm.pretrained_model_name_or_path = str(opt)
            if flash_compat.standing_in():
                model_config.llm._attn_implementation = "sdpa"
        model = get_model(tokenizer=tokenizer, **model_config)
        system = get_system(
            **read(f"configs/system/{parts.system}.yaml"),
            model=model,
            optimizer_config=None,
            loss_config=None,
            scheduler_config=None,
            steps_per_epoch=1,
        )
        state = self.torch.load(str(checkpoint), map_location="cpu", weights_only=False)["state_dict"]
        system.load_state_dict(state)
        system.eval()
        return _Part(system, model, tokenizer, TransformConfig.parse(config=transform))

    def unload(self) -> None:
        self.skeleton = self.skinner = None
        if self._config_dir is not None:
            self._config_dir.cleanup()
            self._config_dir = None
        gc.collect()
        if self.torch is not None:
            self.torch.cuda.empty_cache()

    @contextlib.contextmanager
    def _on_gpu(self, part: _Part) -> Iterator[None]:
        part.system.to("cuda")
        try:
            # Upstream runs prediction with Lightning's bf16-mixed precision.
            with self.torch.no_grad(), self.torch.autocast("cuda", dtype=self.torch.bfloat16):
                yield
        finally:
            part.system.to("cpu")
            self.torch.cuda.empty_cache()

    def _batch(self, part: _Part, folder: Path, data_name: str) -> dict[str, Any]:
        from src.data.dataset import UniRigDataset

        data = UniRigDataset(
            data=[("inference", str(folder))],
            name="predict-inference",
            process_fn=part.model._process_fn,
            tokenizer=part.tokenizer,
            transform_config=part.transform,
            data_name=data_name,
        )
        batch: dict[str, Any] = data.collate_fn([data[0]])
        return batch

    def _to_gpu(self, batch: dict[str, Any]) -> dict[str, Any]:
        return {k: v.to("cuda") if isinstance(v, self.torch.Tensor) else v for k, v in batch.items()}

    def run(self, ctx: StageContext) -> list[StageOutput]:
        assert self.root is not None and self.skeleton is not None and self.skinner is not None
        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        seed_everything(ctx.seed, self.torch)
        with tempfile.TemporaryDirectory(dir=ctx.out_dir) as tmp, inside(self.root):
            work = Path(tmp)
            raw = self.write_raw_data(mesh, work)
            ctx.progress(1, 4)
            ctx.check_cancel()
            names = self.predict_skeleton(work, int(ctx.params.get("beams", 15)))
            ctx.progress(2, 4)
            ctx.check_cancel()
            vertices, joints, parents, weights = self.predict_skin(work)
            ctx.progress(3, 4)
        center, scale, center_n = frame_change(raw, vertices)

        def back(p: NDArray[np.floating[Any]]) -> NDArray[np.float64]:
            return from_blender((np.asarray(p, dtype=np.float64) - center_n) / scale + center)

        skeleton = skin_ops.Skeleton(tuple(names), tuple(parents), back(joints))
        named, bone_map = (
            rig_ops.as_humanoid(skeleton) if ctx.params.get("humanoid", True) else (skeleton, {})
        )
        on_mesh = rig_ops.transfer(
            np.asarray(mesh.vertices, dtype=np.float64),
            back(vertices),
            skin_ops.from_dense(weights),
            len(named),
        )
        glb, profile = rig_ops.save(mesh, named, on_mesh, bone_map, "unirig", ctx.out_dir)
        ctx.progress(4, 4)
        return [StageOutput(glb, ArtifactKind.MESH_GLB), StageOutput(profile, ArtifactKind.JSON)]

    def write_raw_data(self, mesh: trimesh.Trimesh, folder: Path) -> NDArray[np.float64]:
        """The mesh as upstream's extract step saves it: welded, decimated past FACES."""
        from src.data.raw_data import RawData

        welded = trimesh.Trimesh(to_blender(mesh.vertices), np.asarray(mesh.faces), process=True)
        vertices = np.asarray(welded.vertices, dtype=np.float32)
        faces = np.asarray(welded.faces, dtype=np.int64)
        if len(faces) > FACES:
            vertices, faces = fast_simplification.simplify(vertices, faces, target_count=FACES)
            welded = trimesh.Trimesh(vertices, faces)
        RawData(
            vertices=np.asarray(welded.vertices, dtype=np.float32),
            vertex_normals=np.asarray(welded.vertex_normals, dtype=np.float32),
            faces=np.asarray(welded.faces, dtype=np.int64),
            face_normals=np.asarray(welded.face_normals, dtype=np.float32),
            joints=None,
            tails=None,
            skin=None,
            no_skin=None,
            parents=None,
            names=None,
            matrix_local=None,
        ).save(path=str(folder / "raw_data.npz"))
        return np.asarray(welded.vertices, dtype=np.float64)

    def predict_skeleton(self, folder: Path, beams: int) -> list[str]:
        """Writes predict_skeleton.npz as upstream's skeleton writer does; returns bone names."""
        from src.data.raw_data import RawData

        part = self.skeleton
        assert part is not None
        batch = self._batch(part, folder, "raw_data.npz")
        part.system.generate_kwargs = {**part.system.generate_kwargs, "num_beams": beams}
        with self._on_gpu(part):
            # Upstream's predict_step prints and swallows errors; this one raises them.
            out = part.system._predict_step(self._to_gpu(batch), 0)
        if not out:
            raise RuntimeError("UniRig found no skeleton for this mesh")
        found = out[0]
        n, f = int(batch["num_points"][0]), int(batch["num_faces"][0])

        def first(key: str, count: int) -> Any:
            return batch[key][0, :count].detach().cpu().numpy()

        RawData(
            vertices=first("origin_vertices", n),
            vertex_normals=first("origin_vertex_normals", n),
            faces=first("origin_faces", f),
            face_normals=first("origin_face_normals", f),
            joints=found.joints,
            tails=found.tails,
            parents=found.parents,
            skin=None,
            no_skin=found.no_skin,
            names=found.names,
            matrix_local=None,
            path=None,
            cls=found.cls,
        ).save(path=str(folder / "predict_skeleton.npz"))
        names = list(found.names) if found.names is not None else []
        if len(names) != len(found.parents) or len(set(names)) != len(names):
            names = [f"bone_{i}" for i in range(len(found.parents))]
        return [str(n) for n in names]

    def predict_skin(
        self, folder: Path
    ) -> tuple[NDArray[np.float32], NDArray[np.float32], list[int], NDArray[np.float32]]:
        """Weights for upstream's copy of the mesh, resampled as its skin writer does: that
        copy's vertices, the joints and parents (-1 for the root), and (vertices, joints) weights."""
        from src.system.skin import reskin

        part = self.skinner
        assert part is not None
        batch = self._batch(part, folder, "predict_skeleton.npz")
        with self._on_gpu(part):
            res = part.system.predict_step(self._to_gpu(batch), 0)
            sampled = res.get("sampled_vertices")
            sampled = batch["vertices"] if sampled is None else sampled
            predicted = res["skin_pred"][0].float().cpu().numpy()
            sampled = sampled[0].float().cpu().numpy()
        n, j, f = (int(batch[k][0]) for k in ("num_points", "num_bones", "num_faces"))
        vertices = batch["origin_vertices"][0, :n].float().numpy()
        parents = [int(p) for p in batch["parents"][0, :j].tolist()]
        weights = reskin(
            sampled_vertices=sampled,
            vertices=vertices,
            parents=[None if p < 0 else p for p in parents],
            faces=batch["origin_faces"][0, :f].numpy(),
            sampled_skin=predicted,
            sample_method="median",
            alpha=2.0,
            threshold=0.03,
        )
        joints = batch["joints"][0, :j].float().numpy()
        return vertices, joints, parents, np.asarray(weights, dtype=np.float32)
