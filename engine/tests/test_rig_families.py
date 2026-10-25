"""The UniRig and SkinTokens adapters, tested against fake upstream modules.

The real models need CUDA and gigabytes of weights; these pin down what Forja owns around
them: the mesh in upstream's frame and format, the rig mapped back onto the mesh that ships,
humanoid names, the given-skeleton mode, and running without flash-attn.
"""

import contextlib
import importlib
import json
import sys
import types
from dataclasses import dataclass, fields
from pathlib import Path
from typing import Any, ClassVar

import numpy as np
import pytest
import trimesh
import yaml

from forja_engine import families, flash_compat, install
from forja_engine.ops import mesh as mesh_ops
from forja_engine.ops import rig as rig_ops
from forja_engine.ops import skin as skin_ops
from forja_engine.registry import Registry
from forja_engine.sdk import ArtifactKind, Device, StageContext, StageInput, StageType


class Tensor(np.ndarray):
    """Enough of torch.Tensor for adapter glue: numpy underneath."""

    def detach(self):
        return self

    def cpu(self):
        return self

    def numpy(self):
        return np.asarray(self)

    def float(self):
        return np.asarray(self, dtype=np.float32).view(Tensor)

    def to(self, *_a, **_k):
        return self


def tensor(a) -> Tensor:
    return np.asarray(a).view(Tensor)


@pytest.fixture
def torch(monkeypatch):
    t = types.ModuleType("torch")
    t.Tensor = Tensor
    t.bfloat16 = "bf16"
    t.no_grad = contextlib.nullcontext
    t.autocast = lambda *a, **k: contextlib.nullcontext()
    t.manual_seed = lambda seed: None
    t.cuda = types.SimpleNamespace(empty_cache=lambda: None)
    monkeypatch.setitem(sys.modules, "torch", t)
    return t


def module(monkeypatch, name: str, **attrs: Any) -> types.ModuleType:
    parts = name.split(".")
    for i in range(1, len(parts) + 1):
        sub = ".".join(parts[:i])
        if sub not in sys.modules or not isinstance(sys.modules[sub], types.ModuleType):
            monkeypatch.setitem(sys.modules, sub, types.ModuleType(sub))
    for k, v in attrs.items():
        setattr(sys.modules[name], k, v)
    return sys.modules[name]


def character() -> trimesh.Trimesh:
    """A blocky figure facing +Z, unwrapped (so its vertices are split along UV seams)."""
    boxes = [
        ((0.14, 0.9, 0.16), (0.11, 0.45, 0)),
        ((0.14, 0.9, 0.16), (-0.11, 0.45, 0)),
        ((0.42, 0.6, 0.22), (0, 1.2, 0)),
        ((0.22, 0.26, 0.22), (0, 1.66, 0)),
        ((0.1, 0.62, 0.1), (0.28, 1.14, 0)),
        ((0.1, 0.62, 0.1), (-0.28, 1.14, 0)),
    ]
    parts = [trimesh.creation.box(e, trimesh.transformations.translation_matrix(c)) for e, c in boxes]
    return mesh_ops.unwrap(trimesh.util.concatenate([p.subdivide() for p in parts]))


def normalize(v: np.ndarray) -> tuple[np.ndarray, Any]:
    """Into [-1, 1] by the bounds, as upstream's transforms do."""
    lo, hi = v.min(axis=0), v.max(axis=0)
    center, scale = (lo + hi) / 2, 2 / np.max(hi - lo)
    return (v - center) * scale, lambda p: (np.asarray(p) - center) * scale


def ctx(tmp_path: Path, mesh_path: Path, **params) -> StageContext:
    out = tmp_path / "out"
    out.mkdir(exist_ok=True)
    return StageContext(
        "s", StageType.RIG, [StageInput(mesh_path, ArtifactKind.MESH_GLB, "")], params, 7, out
    )


def anonymous(skeleton: skin_ops.Skeleton) -> tuple[list[str], list[int | None]]:
    return [f"bone_{i}" for i in range(len(skeleton))], [None if p < 0 else p for p in skeleton.parents]


# --- UniRig -------------------------------------------------------------------------------------


@pytest.fixture
def unirig(monkeypatch, torch, tmp_path):
    """Upstream's modules, faked to behave like its pipeline: npz in, normalized rig out."""
    seen: dict[str, Any] = {"gpu": [], "beams": None, "attn": None, "backend": None}

    @dataclass(frozen=True)
    class RawData:
        vertices: Any
        vertex_normals: Any
        faces: Any
        face_normals: Any
        joints: Any
        tails: Any
        skin: Any
        no_skin: Any
        parents: Any
        names: Any
        matrix_local: Any
        path: Any = None
        cls: Any = None

        def save(self, path: str) -> None:
            np.savez(path, **{f.name: np.array(getattr(self, f.name), dtype=object) for f in fields(self)})

    def load(path: Path) -> dict[str, Any]:
        d = np.load(path, allow_pickle=True)
        return {k: d[k][()] for k in d}

    class Dataset:
        def __init__(self, data, name, process_fn, tokenizer, transform_config, data_name):
            (_, self.folder), self.data_name = data[0], data_name

        def __getitem__(self, i):
            return load(Path(self.folder) / self.data_name)

        def collate_fn(self, items):
            d = items[0]
            v, _ = normalize(np.asarray(d["vertices"], dtype=np.float64))
            if d["joints"] is not None:
                v = np.asarray(d["vertices"], dtype=np.float64)  # the skeleton phase saved it normalized
            batch = {
                "origin_vertices": tensor(v[None].astype(np.float32)),
                "origin_vertex_normals": tensor(np.asarray(d["vertex_normals"])[None]),
                "origin_faces": tensor(np.asarray(d["faces"])[None]),
                "origin_face_normals": tensor(np.asarray(d["face_normals"])[None]),
                "num_points": tensor([len(v)]),
                "num_faces": tensor([len(d["faces"])]),
                "vertices": tensor(v[None, ::3].astype(np.float32)),
                "path": [self.folder],
            }
            if d["joints"] is not None:
                parents = [-1 if p is None else p for p in d["parents"]]
                batch.update(
                    joints=tensor(np.asarray(d["joints"], dtype=np.float32)[None]),
                    parents=tensor(np.asarray(parents)[None]),
                    num_bones=tensor([len(parents)]),
                )
            return batch

    class System:
        def __init__(self, kind, model):
            self.kind, self.model = kind, model
            self.generate_kwargs = {"num_beams": 15, "top_k": 5}

        def load_state_dict(self, state):
            assert state == {"weights": self.kind}

        def eval(self):
            return self

        def to(self, device):
            if device == "cuda":
                seen["gpu"].append(self.kind)
            return self

        def _predict_step(self, batch, _i):
            seen["beams"] = self.generate_kwargs["num_beams"]
            v = np.asarray(batch["origin_vertices"][0])
            # A humanoid skeleton for the figure, as upstream would find it: Blender frame,
            # normalized, anonymous names.
            figure = trimesh.PointCloud(np.stack([v[:, 0], v[:, 2], -v[:, 1]], axis=1))
            t = rig_ops.template(figure)
            heads = np.stack([t.heads[:, 0], -t.heads[:, 2], t.heads[:, 1]], axis=1)
            names, parents = anonymous(t)
            return [
                types.SimpleNamespace(
                    joints=heads,
                    tails=heads,
                    parents=parents,
                    no_skin=None,
                    names=names,
                    cls="articulationxl",
                )
            ]

        def predict_step(self, batch, _i):
            sampled = np.asarray(batch["vertices"][0], dtype=np.float64)
            parents = [int(p) for p in batch["parents"][0]]
            sk = skin_ops.Skeleton(
                tuple(f"b{i}" for i in range(len(parents))),
                tuple(parents),
                np.asarray(batch["joints"][0], float),
            )
            return {"skin_pred": [tensor(rig_ops.distance_weights(sampled, sk).dense(len(sk)))]}

    def get_model(tokenizer=None, **cfg):
        if "llm" in cfg:
            seen["attn"] = cfg["llm"].get("_attn_implementation")
            seen["opt"] = json.loads(
                (Path(cfg["llm"]["pretrained_model_name_or_path"]) / "config.json").read_text()
            )
        return types.SimpleNamespace(_process_fn=None, kind=cfg["__target__"])

    def get_system(model, **cfg):
        return System(cfg["__target__"], model)

    class TransformConfig:
        @staticmethod
        def parse(config):
            groups = config.get("vertex_group_config") or {}
            voxel = (groups.get("kwargs") or {}).get("voxel_skin")
            if voxel:
                seen["backend"] = voxel["backend"]
            return config

    def reskin(sampled_vertices, vertices, parents, faces, sampled_skin, **kw):
        from scipy.spatial import cKDTree

        _, idx = cKDTree(sampled_vertices).query(vertices)
        return sampled_skin[idx]

    class Box(dict):
        def __init__(self, d=None):
            super().__init__({k: Box(v) if isinstance(v, dict) else v for k, v in (d or {}).items()})

        __getattr__ = dict.get

        def __setattr__(self, k, v):
            self[k] = v

    module(monkeypatch, "box", Box=Box)
    module(monkeypatch, "src.data.raw_data", RawData=RawData)
    module(monkeypatch, "src.data.dataset", UniRigDataset=Dataset)
    module(monkeypatch, "src.data.transform", TransformConfig=TransformConfig)
    module(monkeypatch, "src.model.parse", get_model=get_model)
    module(monkeypatch, "src.system.parse", get_system=get_system)
    module(monkeypatch, "src.system.skin", reskin=reskin)
    module(monkeypatch, "src.tokenizer.parse", get_tokenizer=lambda config: "tokenizer")
    module(
        monkeypatch, "src.tokenizer.spec", TokenizerConfig=types.SimpleNamespace(parse=lambda config: config)
    )

    root = tmp_path / "upstream"
    configs = {
        "task/quick_inference_skeleton_articulationxl_ar_256": {
            "components": {"transform": "ar_t", "tokenizer": "tok", "model": "ar_m", "system": "ar_s"}
        },
        "task/quick_inference_unirig_skin": {
            "components": {"transform": "skin_t", "model": "skin_m", "system": "skin_s"}
        },
        "transform/ar_t": {"predict_transform_config": {"vertex_group_config": None}},
        "transform/skin_t": {
            "predict_transform_config": {
                "vertex_group_config": {"kwargs": {"voxel_skin": {"backend": "pyrender"}}}
            }
        },
        "tokenizer/tok": {"method": "tokenizer_part"},
        "model/ar_m": {
            "__target__": "unirig_ar",
            "llm": {"pretrained_model_name_or_path": "facebook/opt-350m"},
        },
        "model/skin_m": {"__target__": "unirig_skin"},
        "system/ar_s": {"__target__": "ar"},
        "system/skin_s": {"__target__": "skin"},
    }
    for name, doc in configs.items():
        path = root / "configs" / f"{name}.yaml"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(yaml.safe_dump(doc))
    torch.load = lambda path, **kw: {"state_dict": {"weights": "ar" if "skeleton" in str(path) else "skin"}}

    import forja_unirig

    monkeypatch.setattr(forja_unirig, "upstream_root", lambda: root)
    monkeypatch.setattr(flash_compat, "install", lambda: True)
    monkeypatch.setattr(flash_compat, "standing_in", lambda: True)
    adapter = forja_unirig.UniRig()
    adapter.weights_dir = tmp_path / "weights"
    adapter.load(Device.CUDA)
    return adapter, seen


def test_unirig_loads_offline_and_without_flash_attn(unirig):
    _, seen = unirig
    assert seen["opt"]["model_type"] == "opt" and seen["opt"]["num_hidden_layers"] == 24
    assert seen["attn"] == "sdpa"  # flash-attn is not installed: PyTorch's attention
    assert seen["backend"] == "open3d"  # not pyrender, which needs an OpenGL context


def test_unirig_rigs_the_mesh_that_ships(unirig, tmp_path):
    adapter, seen = unirig
    mesh = character()
    src = mesh_ops.save(mesh, tmp_path / "baked.glb")
    steps = []
    c = ctx(tmp_path, src, beams=4)
    c.progress = lambda i, n: steps.append((i, n))
    glb, profile = adapter.run(c)
    assert seen["beams"] == 4 and seen["gpu"] == ["ar", "skin"]  # one model on the GPU at a time
    assert steps[-1] == (4, 4)
    skeleton, skins = skin_ops.read(glb.path)
    # Back in glTF's frame, on the figure: the head on top, the left hand at +X.
    assert skeleton.profile == "humanoid" and set(rig_ops.REQUIRED) <= set(skeleton.names)
    head = skeleton.heads[skeleton.names.index("Head")]
    hand = skeleton.heads[skeleton.names.index("LeftHand")]
    assert head[1] > 1.4 and hand[0] > 0.15
    info = json.loads(profile.path.read_text())
    assert info["source"] == "unirig" and info["bone_map"]["Hips"].startswith("bone_")
    # Every vertex of the mesh as baked keeps its place, and is weighted.
    out = mesh_ops.load(glb.path)
    assert len(out.vertices) == len(mesh.vertices)
    (weights,) = skins.values()
    assert np.allclose(weights.weights.sum(axis=1), 1, atol=1e-5)
    tip = int(np.argmax(out.vertices[:, 0]))
    assert skeleton.names[weights.joints[tip, 0]].startswith("Left")


def test_unirig_can_leave_a_skeleton_unnamed(unirig, tmp_path):
    adapter, _ = unirig
    src = mesh_ops.save(character(), tmp_path / "baked.glb")
    skeleton, _ = skin_ops.read(adapter.run(ctx(tmp_path, src, humanoid=False))[0].path)
    assert skeleton.profile == "generic" and skeleton.names[0].startswith("bone_")


def test_frames_round_trip():
    from forja_unirig import frame_change, from_blender, to_blender

    v = np.random.default_rng(1).normal(size=(50, 3))
    assert np.allclose(from_blender(to_blender(v)), v)
    assert np.allclose(to_blender([[0, 1, 0]]), [[0, 0, 1]])  # glTF's up is Blender's +Z
    n, _ = normalize(v)
    center, scale, center_n = frame_change(v, n)
    assert np.allclose((v - center) * scale + center_n, n)


# --- SkinTokens ---------------------------------------------------------------------------------


@pytest.fixture
def skintokens(monkeypatch, torch, tmp_path):
    seen: dict[str, Any] = {}

    class Model:
        tokenizer_config: ClassVar = {"method": "tokenizer_part"}
        transform_config: ClassVar = {"predict_transform": {"normalize": True}}

        @classmethod
        def load_from_system_checkpoint(
            cls, checkpoint_path, model_config, transform_config, tokenizer_config
        ):
            import transformers

            seen["vae"] = model_config["pretrained_vae"]
            seen["qwen"] = json.loads(Path("models/Qwen3-0.6B/config.json").read_text())
            transformers.AutoModelForCausalLM.from_config(config={}, attn_implementation="flash_attention_2")
            return cls()

        _process_fn = None

        def to(self, device):
            return self

        def eval(self):
            return self

        def predict_step(self, batch, skeleton_tokens=None, make_asset=False):
            seen["tokens"], seen["kwargs"] = skeleton_tokens, batch["generate_kwargs"]
            d = batch["npz"]
            v, moved = normalize(np.asarray(d["vertices"], dtype=np.float64))
            if "matrix_local" in d:
                # Weighting the given skeleton: quantized joints, in an order of its own.
                given = moved(d["matrix_local"][:, :3, 3])
                order = np.random.default_rng(3).permutation(len(given))
                joints = np.round(given[order] * 128) / 128
                parents = [int(np.where(order == p)[0][0]) if p >= 0 else -1 for p in d["parents"][order]]
            else:
                figure = trimesh.PointCloud(np.stack([v[:, 0], v[:, 2], -v[:, 1]], axis=1))
                t = rig_ops.template(figure)
                joints = np.stack([t.heads[:, 0], -t.heads[:, 2], t.heads[:, 1]], axis=1)
                parents = list(t.parents)
            sk = skin_ops.Skeleton(tuple(f"b{i}" for i in range(len(joints))), tuple(parents), joints)
            asset = types.SimpleNamespace(
                vertices=v,
                joints=joints,
                parents=np.array(parents),
                joint_names=None,
                skin=rig_ops.distance_weights(v, sk).dense(len(sk)),
            )
            return {"results": [types.SimpleNamespace(asset=asset)]}

    class DatasetConfig:
        @staticmethod
        def parse(**kw):
            seen["workers"] = kw["num_workers"]
            return types.SimpleNamespace(split_by_cls=lambda: kw["datapath"])

    class Module:
        def __init__(self, predict_dataset_config, **kw):
            (path,) = predict_dataset_config["filepaths"]["articulation"]
            self.npz = dict(np.load(path))

        def predict_dataloader(self):
            batch = {"npz": self.npz, "vertices": tensor(self.npz["vertices"])}
            if "matrix_local" in self.npz:
                batch.update(skeleton_tokens=tensor([[7, 8, 9]]), skeleton_mask=tensor([[1, 1, 0]]))
            return {"articulation": [batch]}

    real_from_config = types.SimpleNamespace(calls=[])

    class Auto:
        @staticmethod
        def from_config(**kw):
            real_from_config.calls.append(kw["attn_implementation"])

    module(monkeypatch, "transformers", AutoModelForCausalLM=Auto)
    module(monkeypatch, "src.model.tokenrig", TokenRig=Model)
    module(monkeypatch, "src.data.transform", Transform=types.SimpleNamespace(parse=lambda **kw: kw))
    module(monkeypatch, "src.tokenizer.parse", get_tokenizer=lambda **kw: "tokenizer")
    module(monkeypatch, "src.data.dataset", DatasetConfig=DatasetConfig, RigDatasetModule=Module)
    module(monkeypatch, "src.data.vertex_group", voxel_skin=None)
    torch.load = lambda path, **kw: {
        "hyper_parameters": {
            "model_config": {"pretrained_vae": "experiments/skin_vae_2_10_32768/last.ckpt"},
            "transform_config": {},
            "tokenizer_config": {},
        }
    }
    monkeypatch.setattr(flash_compat, "install", lambda: True)
    monkeypatch.setattr(flash_compat, "standing_in", lambda: True)
    from forja_skintokens import SkinTokens

    adapter = SkinTokens()
    adapter.weights_dir = tmp_path / "weights"
    adapter.load(Device.CUDA)
    seen["attn"] = real_from_config.calls
    return adapter, seen


def test_skintokens_loads_offline_and_without_flash_attn(skintokens, tmp_path):
    _, seen = skintokens
    assert seen["qwen"]["model_type"] == "qwen3" and seen["qwen"]["num_hidden_layers"] == 28
    assert Path(seen["vae"]) == tmp_path / "weights" / "experiments" / "skin_vae_2_10_32768" / "last.ckpt"
    assert seen["attn"] == ["sdpa"]


def test_skintokens_predicts_a_rig_and_names_a_humanoid(skintokens, tmp_path):
    adapter, seen = skintokens
    mesh = character()
    glb, profile = adapter.run(ctx(tmp_path, mesh_ops.save(mesh, tmp_path / "baked.glb"), beams=3))
    assert seen["tokens"] is None and seen["kwargs"]["num_beams"] == 3 and seen["workers"] == 0
    skeleton, skins = skin_ops.read(glb.path)
    assert skeleton.profile == "humanoid" and set(rig_ops.REQUIRED) <= set(skeleton.names)
    assert skeleton.heads[skeleton.names.index("LeftHand")][0] > 0.15
    assert len(next(iter(skins.values()))) == len(mesh.vertices)
    assert json.loads(profile.path.read_text())["source"] == "skintokens"


def test_skintokens_weights_the_standard_skeleton_it_is_given(skintokens, tmp_path):
    adapter, seen = skintokens
    mesh = character()
    glb, profile = adapter.run(
        ctx(tmp_path, mesh_ops.save(mesh, tmp_path / "baked.glb"), skeleton="template")
    )
    assert [t.tolist() for t in seen["tokens"]] == [[7, 8]]  # the skeleton's own tokens, unpadded
    skeleton, skins = skin_ops.read(glb.path)
    template = rig_ops.template(mesh)
    assert skeleton.names == template.names and np.allclose(skeleton.heads, template.heads, atol=1e-5)
    # The weights followed their joints back into the template's order: the lowest point on the
    # left arm's outer side moves with the left hand.
    (weights,) = skins.values()
    v = mesh_ops.load(glb.path).vertices
    arm = np.flatnonzero(v[:, 0] > 0.32)
    tip = int(arm[np.argmin(v[arm, 1])])
    assert skeleton.names[weights.joints[tip, 0]] == "LeftHand"
    assert json.loads(profile.path.read_text())["bone_map"]["Hips"] == "Hips"


# --- around them --------------------------------------------------------------------------------


def test_rig_manifests_mirror_their_adapters_and_pin_everything():
    models = Registry.load().models
    for mid in ("unirig", "skintokens"):
        m = models[mid]
        module_name, _, cls = m.adapter.partition(":")
        assert getattr(importlib.import_module(module_name), cls).version == m.adapter_version
        assert len(m.weights["revision"]) == 40 and m.weights["repo"].startswith("VAST-AI/")
        assert all(len(f["sha256"]) == 64 and f["bytes"] > 0 for f in m.weights["files"])
        assert m.tasks == (StageType.RIG,) and not m.license.restrictions
        fam = families.load(m.family)
        assert fam.upstream and len(fam.upstream.sha256) == 64 and "VAST-AI-Research" in fam.upstream.url
        assert fam.requirements("cuda").is_file()
    # SkinTokens ranks above UniRig, but needs a 14 GB card.
    assert models["skintokens"].rank > models["unirig"].rank
    assert models["skintokens"].requirements["cuda"]["min_vram_gb"] == 14


@pytest.mark.parametrize("name", ["unirig", "skintokens"])
def test_flash_attn_is_built_on_linux_only(name):
    fam = families.load(name)
    linux = fam.builds("cuda", {"sys_platform": "linux"})
    assert linux == fam.build_by_lane["cuda"] and linux[0].startswith("flash-attn==")
    assert fam.builds("cuda", {"sys_platform": "win32"}) == ()
    plain = families.Family("x", fam.root, "3.11", ("cuda",), build_by_lane={"cuda": ("pkg==1",)})
    assert plain.builds("cuda") == ("pkg==1",)


def test_a_family_with_nothing_to_build_here_needs_no_compiler(tmp_path, monkeypatch):
    commands: list[list[str]] = []
    monkeypatch.setattr(
        install, "_run", lambda cmd, log, env=None, cwd=None, **kw: commands.append([str(c) for c in cmd])
    )
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")

    def no_compiler(backend, log):
        raise AssertionError("asked for a CUDA toolchain")

    monkeypatch.setattr(install.toolchain, "build_env", no_compiler)
    monkeypatch.setattr(install, "fetch_verified", lambda url, sha, cache, log: Path("/cache") / sha)
    monkeypatch.setattr(install, "extract_tarball", lambda archive, dest: tmp_path / "up")
    site = tmp_path / "site"
    site.mkdir()
    monkeypatch.setattr(install, "site_packages", lambda py: site)
    monkeypatch.setattr(install.Installer, "weights", lambda self, m: None)
    monkeypatch.setattr(families.Family, "builds", lambda self, lane, environment=None: ())
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    inst.install("unirig", lane="cuda")
    assert not any("--no-build-isolation" in c for c in commands)
    pip = next(c for c in commands if "-r" in c)
    assert pip[pip.index("--torch-backend") + 1] == "cu124"


def test_flash_attn_stand_ins_register_without_torch(monkeypatch):
    for name in [n for n in sys.modules if n == "flash_attn" or n.startswith("flash_attn.")]:
        monkeypatch.delitem(sys.modules, name)
    monkeypatch.setattr(importlib.util, "find_spec", lambda name, *a: None)
    assert not flash_compat.standing_in()
    try:
        assert flash_compat.install()
        assert flash_compat.standing_in()
        from flash_attn import flash_attn_func
        from flash_attn.flash_attn_interface import flash_attn_varlen_qkvpacked_func

        assert flash_attn_func is flash_compat.flash_attn_func
        assert flash_attn_varlen_qkvpacked_func is flash_compat.flash_attn_varlen_qkvpacked_func
        assert sys.modules["flash_attn"].__spec__.name == "flash_attn"  # transformers asks for it
        assert not flash_compat.install()  # once
    finally:
        for name in [n for n in sys.modules if n == "flash_attn" or n.startswith("flash_attn.")]:
            del sys.modules[name]
