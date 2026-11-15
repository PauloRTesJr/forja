"""SkinTokens / TokenRig (MIT), VAST-AI-Research: UniRig's successor, predicting the skeleton
and the skin weights as one token sequence. It can also weight a skeleton it is given.

Upstream's demo reads meshes through a Blender server and writes its results with Blender.
Here the mesh goes in through upstream's own npz loader, the rig comes back as upstream's
Asset in its normalized frame, and Forja writes the skinned GLB on the mesh it was given.
Upstream reads meshes as Blender holds them, Z up; Forja's are glTF's, Y up.

`skeleton: template` fits Forja's humanoid template to the mesh and has the model weight it,
so a character always gets the standard humanoid bones.
"""

from __future__ import annotations

import contextlib
import gc
import json
import os
import random
import tempfile
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import numpy as np
import trimesh
from numpy.typing import NDArray
from scipy.spatial import cKDTree

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
RIG_CKPT = "experiments/articulation_xl_quantization_256_token_4/grpo_1400.ckpt"
VAE_CKPT = "experiments/skin_vae_2_10_32768/last.ckpt"
#: Qwen3-0.6B's architecture, the base of upstream's rig model. Upstream fetches it from the
#: Hub unless models/Qwen3-0.6B is in its working folder, so the adapter puts it there.
QWEN3_06B = {
    "model_type": "qwen3",
    "attention_bias": False,
    "attention_dropout": 0.0,
    "bos_token_id": 151643,
    "eos_token_id": 151645,
    "head_dim": 128,
    "hidden_act": "silu",
    "hidden_size": 1024,
    "initializer_range": 0.02,
    "intermediate_size": 3072,
    "max_position_embeddings": 40960,
    "max_window_layers": 28,
    "num_attention_heads": 16,
    "num_hidden_layers": 28,
    "num_key_value_heads": 8,
    "rms_norm_eps": 1e-06,
    "rope_theta": 1000000,
    "sliding_window": None,
    "tie_word_embeddings": True,
    "use_sliding_window": False,
    "vocab_size": 151936,
}


def to_blender(v: NDArray[np.floating[Any]]) -> NDArray[np.float64]:
    x, y, z = np.asarray(v, dtype=np.float64).T
    return np.stack([x, -z, y], axis=1)


def from_blender(v: NDArray[np.floating[Any]]) -> NDArray[np.float64]:
    x, y, z = np.asarray(v, dtype=np.float64).T
    return np.stack([x, z, -y], axis=1)


def frame_change(
    src: NDArray[np.float64], dst: NDArray[np.floating[Any]]
) -> tuple[NDArray[np.float64], float, NDArray[np.float64]]:
    """(center, scale, center') with dst = (src - center) * scale + center'."""
    lo, hi = src.min(axis=0), src.max(axis=0)
    dlo, dhi = np.asarray(dst).min(axis=0), np.asarray(dst).max(axis=0)
    scale = float(np.max(dhi - dlo) / max(float(np.max(hi - lo)), 1e-12))
    return (lo + hi) / 2, scale, (dlo + dhi) / 2


@contextlib.contextmanager
def inside(folder: Path) -> Iterator[None]:
    before = Path.cwd()
    os.chdir(folder)
    try:
        yield
    finally:
        os.chdir(before)


@contextlib.contextmanager
def sdpa_for_transformers() -> Iterator[None]:
    """Upstream asks transformers for flash-attention 2 by name; without flash-attn, PyTorch's."""
    if not flash_compat.standing_in():
        yield
        return
    import transformers

    original = transformers.AutoModelForCausalLM.from_config

    def from_config(*args: Any, **kwargs: Any) -> Any:
        if kwargs.get("attn_implementation") == "flash_attention_2":
            kwargs["attn_implementation"] = "sdpa"
        return original(*args, **kwargs)

    transformers.AutoModelForCausalLM.from_config = from_config
    try:
        yield
    finally:
        transformers.AutoModelForCausalLM.from_config = original


def match_joints(predicted: NDArray[np.float64], given: NDArray[np.float64]) -> list[int] | None:
    """Which given joint each predicted one is, when the model weighted a given skeleton: its
    tokens quantize positions and may reorder joints. None when they do not pair one to one."""
    if len(predicted) != len(given):
        return None
    _, idx = cKDTree(given).query(predicted)
    pairs = [int(i) for i in np.atleast_1d(idx)]
    return pairs if len(set(pairs)) == len(pairs) else None


class SkinTokens(Adapter):
    version = "2"

    def __init__(self) -> None:
        self.torch: Any = None
        self.model: Any = None
        self.tokenizer: Any = None
        self.transform: Any = None

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.RIG,), devices=(Device.CUDA,), vram_bytes=14 * GB)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        import torch

        if self.weights_dir is None:
            raise RuntimeError("skintokens: no weights folder; run forja models install skintokens")
        os.environ.setdefault("HF_HUB_OFFLINE", "1")
        flash_compat.install()
        from src.data.transform import Transform
        from src.model.tokenrig import TokenRig
        from src.tokenizer.parse import get_tokenizer

        self.torch = torch
        ckpt = self.weights_dir / RIG_CKPT
        hparams = torch.load(str(ckpt), map_location="cpu", weights_only=False)["hyper_parameters"]
        model_config = dict(hparams["model_config"])
        model_config["pretrained_vae"] = str(self.weights_dir / VAE_CKPT)
        with tempfile.TemporaryDirectory(prefix="forja-skintokens-") as tmp, inside(Path(tmp)):
            qwen = Path(tmp) / "models" / "Qwen3-0.6B"
            qwen.mkdir(parents=True)
            (qwen / "config.json").write_text(json.dumps(QWEN3_06B), encoding="utf-8")
            with sdpa_for_transformers():
                model = TokenRig.load_from_system_checkpoint(
                    checkpoint_path=str(ckpt),
                    model_config=model_config,
                    transform_config=hparams["transform_config"],
                    tokenizer_config=hparams["tokenizer_config"],
                )
        self.model = model.to("cuda").eval()
        self.tokenizer = get_tokenizer(**model.tokenizer_config)
        self.transform = Transform.parse(**model.transform_config["predict_transform"])

    def unload(self) -> None:
        self.model = None
        gc.collect()
        if self.torch is not None:
            self.torch.cuda.empty_cache()

    def run(self, ctx: StageContext) -> list[StageOutput]:
        torch = self.torch
        # The normals the bake's normal map was made against: recomputed, they split at UV seams.
        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path, keep_normals=True)
        template = rig_ops.template(mesh) if ctx.params.get("skeleton", "predict") == "template" else None
        random.seed(ctx.seed)
        np.random.seed(ctx.seed % (1 << 32))
        torch.manual_seed(ctx.seed)
        with tempfile.TemporaryDirectory(dir=ctx.out_dir) as tmp:
            source, path = self.write_npz(mesh, Path(tmp), template)
            ctx.progress(1, 3)
            ctx.check_cancel()
            asset = self.predict(path, ctx.params, template is not None)
        ctx.progress(2, 3)
        ctx.check_cancel()
        center, scale, center_n = frame_change(source, asset.vertices)

        def back(p: NDArray[np.floating[Any]]) -> NDArray[np.float64]:
            return from_blender((np.asarray(p, dtype=np.float64) - center_n) / scale + center)

        joints = back(asset.joints)
        parents = [int(p) if p is not None and p >= 0 else -1 for p in asset.parents]
        skin = np.asarray(asset.skin, dtype=np.float64)
        pairs = match_joints(joints, template.heads) if template is not None else None
        if template is not None and pairs is not None:
            # Weighted the template: keep its exact joints and names, in its order.
            dense = np.zeros((len(skin), len(template)), dtype=np.float64)
            dense[:, pairs] = skin
            skeleton, skin = template, dense
            bone_map = {n: n for n in template.names}
        else:
            names = list(asset.joint_names) if asset.joint_names is not None else []
            if len(names) != len(parents) or len(set(names)) != len(names):
                names = [f"bone_{i}" for i in range(len(parents))]
            skeleton, bone_map = skin_ops.Skeleton(tuple(names), tuple(parents), joints), {}
            if ctx.params.get("humanoid", True):
                skeleton, bone_map = rig_ops.as_humanoid(skeleton)
        on_mesh = rig_ops.transfer(
            np.asarray(mesh.vertices, dtype=np.float64),
            back(asset.vertices),
            skin_ops.from_dense(skin),
            len(skeleton),
        )
        glb, profile = rig_ops.save(mesh, skeleton, on_mesh, bone_map, "skintokens", ctx.out_dir)
        ctx.progress(3, 3)
        return [StageOutput(glb, ArtifactKind.MESH_GLB), StageOutput(profile, ArtifactKind.JSON)]

    def write_npz(
        self, mesh: trimesh.Trimesh, folder: Path, skeleton: skin_ops.Skeleton | None
    ) -> tuple[NDArray[np.float64], Path]:
        """The mesh, welded, in Blender's frame, as upstream's npz loader reads it; with the
        skeleton to weight, if any."""
        welded = trimesh.Trimesh(to_blender(mesh.vertices), np.asarray(mesh.faces), process=True)
        fields: dict[str, Any] = {
            "vertices": np.asarray(welded.vertices, dtype=np.float32),
            "faces": np.asarray(welded.faces, dtype=np.int32),
        }
        if skeleton is not None:
            local = np.repeat(np.eye(4, dtype=np.float32)[None], len(skeleton), axis=0)
            local[:, :3, 3] = to_blender(skeleton.heads)
            fields.update(
                joint_names=np.array(skeleton.names),
                parents=np.array(skeleton.parents, dtype=np.int32),
                matrix_local=local,
            )
        path = folder / "mesh.npz"
        np.savez(path, **fields)
        return np.asarray(welded.vertices, dtype=np.float64), path

    def predict(self, path: Path, params: dict[str, Any], given: bool) -> Any:
        """Upstream's demo, without its Blender server: the rigged Asset."""
        from src.data.dataset import DatasetConfig, RigDatasetModule
        from src.data.vertex_group import voxel_skin

        torch = self.torch
        config = DatasetConfig.parse(
            shuffle=False,
            batch_size=1,
            num_workers=0,
            pin_memory=False,
            persistent_workers=False,
            datapath={"data_name": None, "loader": "npz", "filepaths": {"articulation": [str(path)]}},
        ).split_by_cls()
        module = RigDatasetModule(
            predict_dataset_config=config,
            predict_transform=self.transform,
            tokenizer=self.tokenizer,
            process_fn=self.model._process_fn,
        )
        batch = next(iter(module.predict_dataloader()["articulation"]))
        batch = {k: v.to("cuda") if isinstance(v, torch.Tensor) else v for k, v in batch.items()}
        tokens = None
        if given and "skeleton_tokens" in batch and "skeleton_mask" in batch:
            mask = batch["skeleton_mask"][0] == 1
            tokens = [batch["skeleton_tokens"][0][mask].cpu().numpy()]
        else:
            batch.pop("skeleton_tokens", None)
            batch.pop("skeleton_mask", None)
        batch["generate_kwargs"] = {
            "max_length": 2048,
            "top_k": int(params.get("top_k", 5)),
            "top_p": float(params.get("top_p", 0.95)),
            "temperature": float(params.get("temperature", 1.0)),
            "repetition_penalty": float(params.get("repetition_penalty", 2.0)),
            "num_return_sequences": 1,
            "num_beams": int(params.get("beams", 10)),
            "do_sample": True,
        }
        with torch.no_grad():
            result = self.model.predict_step(batch, skeleton_tokens=tokens, make_asset=True)["results"][0]
        asset = result.asset
        if asset is None:
            raise RuntimeError("SkinTokens returned no rig for this mesh")
        if params.get("postprocess", False):
            voxel = asset.voxel(resolution=196)
            asset.skin *= voxel_skin(
                grid=0,
                grid_coords=voxel.coords,
                joints=asset.joints,
                vertices=asset.vertices,
                faces=asset.faces,
                mode="square",
                voxel_size=voxel.voxel_size,
            )
            asset.normalize_skin()
        return asset
