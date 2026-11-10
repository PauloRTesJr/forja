"""TRELLIS.2 and Pixal3D adapter glue, tested against fake upstream modules.

The real pipelines need CUDA, compiled extensions and tens of gigabytes of weights; these tests
pin down what Forja owns around them: building from local files only, the steps of upstream's
run kept and the texture ones skipped, cameras, frames, materials, progress and cancellation.
"""

import contextlib
import json
import math
import os
import sys
import types
from pathlib import Path

import jsonschema
import numpy as np
import pytest
import trimesh
from fastapi.testclient import TestClient
from PIL import Image

from forja_engine import catalog, checks, families, install
from forja_engine.api.app import EngineConfig, create_app
from forja_engine.ops import mesh as mesh_ops
from forja_engine.registry import Registry
from forja_engine.scheduler import Scheduler
from forja_engine.sdk import ArtifactKind, Cancelled, Device, StageContext, StageInput, StageType


def ctx(tmp_path, task, inputs, **params):
    """inputs are (path, kind) or (path, kind, view)."""
    return StageContext(
        "s", task, [StageInput(Path(i[0]), i[1], "", *i[2:]) for i in inputs], params, 7, tmp_path
    )


def subject(tmp_path, name="subject.png", size=(300, 200), box=(100, 50, 200, 150)) -> Path:
    img = Image.new("RGBA", size, (0, 0, 0, 0))
    img.paste((200, 50, 50, 255), box)
    path = tmp_path / name
    img.save(path)
    return path


class T:
    """A tensor's surface as the adapters use it."""

    def __init__(self, a):
        self.a = np.asarray(a)

    def cpu(self):
        return self

    def numpy(self):
        return self.a

    def __len__(self):
        return len(self.a)


class Decoded:
    """Upstream's Mesh: a box, Z up, its long side along Y (upstream's depth)."""

    def __init__(self):
        box = trimesh.creation.box((1.0, 2.0, 0.5))
        self.vertices, self.faces = T(box.vertices), T(box.faces)
        self.filled = False
        self.simplified = None

    def fill_holes(self):
        self.filled = True

    def simplify(self, target):
        self.simplified = target


@pytest.fixture
def torch(monkeypatch):
    t = types.ModuleType("torch")
    t.float32 = np.float32
    t.seeds = []
    t.manual_seed = t.seeds.append
    t.no_grad = contextlib.nullcontext
    t.device = lambda name: name
    t.cuda = types.SimpleNamespace(empty_cache=lambda: None)
    t.full = lambda shape, value, dtype=None: np.full(shape, value, dtype=np.float32)
    t.tensor = lambda x, dtype=None: np.asarray(x, dtype=np.float32)
    t.stack = np.stack
    t.from_numpy = lambda a: a
    t.loaded = []
    t.load = lambda path, map_location=None, weights_only=False: (
        t.loaded.append((Path(path), weights_only)) or {}
    )
    monkeypatch.setitem(sys.modules, "torch", t)
    return t


def install_module(monkeypatch, name, **attrs):
    parts = name.split(".")
    for i in range(1, len(parts) + 1):
        mod = ".".join(parts[:i])
        if mod not in sys.modules or not getattr(sys.modules[mod], "_fake", False):
            m = types.ModuleType(mod)
            m._fake = True
            monkeypatch.setitem(sys.modules, mod, m)
            if i > 1:
                setattr(sys.modules[".".join(parts[: i - 1])], parts[i - 1], m)
    for k, v in attrs.items():
        setattr(sys.modules[name], k, v)


SAMPLER = {"name": "FlowEulerGuidanceIntervalSampler", "args": {"sigma_min": 1e-5}, "params": {"steps": 12}}


def weights_folder(root: Path, config: str, models: dict[str, str]) -> Path:
    args = {
        "models": models,
        "sparse_structure_sampler": SAMPLER,
        "shape_slat_sampler": SAMPLER,
        "tex_slat_sampler": SAMPLER,
        "shape_slat_normalization": {"mean": [0], "std": [1]},
        "tex_slat_normalization": {"mean": [0], "std": [1]},
        "image_cond_model": {"name": "DinoV3FeatureExtractor", "args": {"model_name": "facebook/dinov3"}},
        "rembg_model": {"name": "BiRefNet", "args": {"model_name": "briaai/RMBG-2.0"}},
        "default_pipeline_type": "1024_cascade",
    }
    root.mkdir(parents=True, exist_ok=True)
    (root / config).write_text(json.dumps({"args": args}))
    for ref in models.values():
        local = root / ref if not ref.startswith("microsoft/") else root / Path(*ref.split("/")[1:])
        local.parent.mkdir(parents=True, exist_ok=True)
        Path(f"{local}.json").write_text("{}")
    return root


def fake_samplers(monkeypatch, package):
    class Sampler:
        def __init__(self, **kw):
            self.args = kw

    install_module(monkeypatch, f"{package}.pipelines.samplers", FlowEulerGuidanceIntervalSampler=Sampler)


# --- TRELLIS.2 ----------------------------------------------------------------------------


TRELLIS_MODELS = {
    "sparse_structure_decoder": "microsoft/TRELLIS-image-large/ckpts/ss_dec",
    "sparse_structure_flow_model": "ckpts/ss_flow",
    "shape_slat_decoder": "ckpts/shape_dec",
    "shape_slat_flow_model_512": "ckpts/shape_512",
    "shape_slat_flow_model_1024": "ckpts/shape_1024",
    "tex_slat_decoder": "ckpts/tex_dec",
    "tex_slat_flow_model_512": "ckpts/tex_512",
    "tex_slat_flow_model_1024": "ckpts/tex_1024",
}


@pytest.fixture
def trellis(monkeypatch, torch, tmp_path):
    log = {"loaded": [], "calls": []}
    monkeypatch.delenv("ATTN_BACKEND", raising=False)
    install_module(
        monkeypatch, "trellis2.models", from_pretrained=lambda path: log["loaded"].append(path) or path
    )
    install_module(
        monkeypatch,
        "trellis2.modules.image_feature_extractor",
        DinoV3FeatureExtractor=lambda path: ("dino", path),
    )
    fake_samplers(monkeypatch, "trellis2")

    class Pipeline:
        def __init__(self, **kw):
            log["kwargs"] = kw
            log["attn"] = os.environ.get("ATTN_BACKEND")
            self.models = kw["models"]

        def cuda(self):
            log["cuda"] = True

        def get_cond(self, images, resolution):
            assert images[0].mode == "RGB"
            log["calls"].append(("cond", resolution))
            return {"res": resolution}

        def sample_sparse_structure(self, cond, resolution, num_samples, params):
            log["calls"].append(("structure", resolution, params["steps"]))
            return "coords"

        def sample_shape_slat_cascade(self, lr, hr, flow_lr, flow_hr, lr_res, res, coords, params, tokens):
            log["calls"].append(("cascade", flow_lr, flow_hr, lr_res, res, tokens))
            return "slat", res

        def sample_shape_slat(self, cond, flow, coords, params):
            log["calls"].append(("slat", flow, cond["res"]))
            return "slat"

        def decode_shape_slat(self, slat, res):
            log["decoded"] = Decoded()
            return [log["decoded"]], None

        # The texturing pipeline's steps.
        def preprocess_mesh(self, mesh):
            v = np.asarray(mesh.vertices)
            lo, hi = v.min(0), v.max(0)
            v = (v - (lo + hi) / 2) * 0.99999 / (hi - lo).max()
            return trimesh.Trimesh(np.stack([v[:, 0], -v[:, 2], v[:, 1]], 1), mesh.faces, process=False)

        def encode_shape_slat(self, mesh, resolution):
            log["uv_kept"] = getattr(mesh.visual, "uv", None) is not None
            return "shape"

        def sample_tex_slat(self, cond, flow, shape, params):
            log["calls"].append(("tex", flow, cond["res"]))
            return "tex"

        def decode_tex_slat(self, slat):
            return "voxels"

        def postprocess_mesh(self, mesh, voxels, resolution, texture_size):
            v = np.asarray(mesh.vertices)
            out = trimesh.Trimesh(np.stack([v[:, 0], v[:, 2], -v[:, 1]], 1), mesh.faces, process=False)
            uv = mesh_ops.unwrap(out).visual.uv if not log["uv_kept"] else mesh.visual.uv
            out = mesh_ops.unwrap(out) if not log["uv_kept"] else out
            material = trimesh.visual.material.PBRMaterial(
                baseColorTexture=Image.new("RGBA", (texture_size,) * 2, (10, 200, 30, 255)),
                metallicRoughnessTexture=Image.new("RGB", (texture_size,) * 2, (0, 180, 20)),
                metallicFactor=1.0,
                roughnessFactor=1.0,
            )
            out.visual = trimesh.visual.TextureVisuals(uv=uv, material=material)
            return out

    install_module(
        monkeypatch,
        "trellis2.pipelines",
        Trellis2ImageTo3DPipeline=Pipeline,
        Trellis2TexturingPipeline=Pipeline,
    )
    weights = weights_folder(tmp_path / "w", "pipeline.json", TRELLIS_MODELS)
    texturing = {
        "shape_slat_encoder": "ckpts/shape_enc",
        "tex_slat_decoder": "ckpts/tex_dec",
        "tex_slat_flow_model_512": "ckpts/tex_512",
        "tex_slat_flow_model_1024": "ckpts/tex_1024",
    }
    weights_folder(weights, "texturing_pipeline.json", texturing)
    return log, weights


def test_trellis2_builds_from_local_files_without_rmbg(trellis):
    from forja_trellis2 import Trellis2Shape

    log, weights = trellis
    a = Trellis2Shape()
    a.weights_dir = weights
    a.load(Device.CUDA)
    assert log["attn"] == "xformers"  # set before upstream's attention modules load
    assert sorted(log["loaded"]) == sorted(
        [
            str(weights / "TRELLIS-image-large" / "ckpts" / "ss_dec"),
            str(weights / "ckpts" / "ss_flow"),
            str(weights / "ckpts" / "shape_dec"),
            str(weights / "ckpts" / "shape_512"),
            str(weights / "ckpts" / "shape_1024"),
        ]
    )
    kw = log["kwargs"]
    assert kw["rembg_model"] is None and kw["image_cond_model"] == ("dino", str(weights / "dinov3"))
    assert kw["low_vram"] and kw["default_pipeline_type"] == "1024_cascade" and log["cuda"]
    assert kw["sparse_structure_sampler"].args == {"sigma_min": 1e-5}


def test_trellis2_prepares_like_upstream():
    from forja_trellis2 import prepare

    img = Image.new("RGBA", (300, 200), (0, 0, 0, 0))
    img.paste((200, 50, 50, 255), (100, 50, 200, 150))
    out = prepare(img)
    assert out.mode == "RGB" and out.size == (98, 98)  # upstream halves the side twice, rounding down
    assert out.getpixel((50, 50)) == (200, 50, 50) and prepare(Image.new("RGB", (8, 8), (9, 9, 9))).size == (
        6,
        6,
    )


def test_trellis2_shape_skips_the_texture_and_turns_y_up(tmp_path, trellis, torch):
    from forja_trellis2 import Trellis2Shape

    log, weights = trellis
    a = Trellis2Shape()
    a.weights_dir = weights
    a.load(Device.CUDA)
    steps = []
    c = ctx(tmp_path, StageType.SHAPE, [(subject(tmp_path), ArtifactKind.IMAGE_PNG)], max_faces=10)
    c.progress = lambda i, n: steps.append((i, n))
    out = a.run(c)
    assert torch.seeds == [7] and steps == [(1, 4), (2, 4), (3, 4), (4, 4)]
    assert log["calls"] == [
        ("cond", 512),
        ("cond", 1024),
        ("structure", 32, 12),
        (
            "cascade",
            str(weights / "ckpts" / "shape_512"),
            str(weights / "ckpts" / "shape_1024"),
            512,
            1024,
            49152,
        ),
    ]
    assert log["decoded"].filled and log["decoded"].simplified == 10
    mesh = mesh_ops.load(out[0].path)
    assert out[0].preview and mesh.extents == pytest.approx([1.0, 0.5, 2.0])  # upstream's Z up is Y up

    log["calls"].clear()
    a.run(ctx(tmp_path, StageType.SHAPE, [(subject(tmp_path), ArtifactKind.IMAGE_PNG)], pipeline_type="512"))
    assert log["calls"][-1] == ("slat", str(weights / "ckpts" / "shape_512"), 512)
    c.cancelled = lambda: True
    with pytest.raises(Cancelled):
        a.run(c)


def test_trellis2_paints_pbr_maps_at_the_inputs_size_and_keeps_its_uvs(tmp_path, trellis):
    from forja_trellis2 import Trellis2Texture

    log, weights = trellis
    a = Trellis2Texture()
    a.weights_dir = weights
    a.load(Device.CUDA)
    assert sorted(Path(p).name for p in log["loaded"]) == ["shape_enc", "tex_1024", "tex_512", "tex_dec"]
    box = trimesh.creation.box((2.0, 4.0, 1.0))
    box.apply_translation([5.0, 0.0, 0.0])
    for uv in (False, True):
        low = mesh_ops.unwrap(box.copy()) if uv else box.copy()
        inputs = [
            (mesh_ops.save(low, tmp_path / "low.glb"), ArtifactKind.MESH_GLB),
            (subject(tmp_path), ArtifactKind.IMAGE_PNG),
        ]
        out = mesh_ops.load(
            a.run(ctx(tmp_path, StageType.TEXTURE, inputs, texture_size=64, yaw_degrees=90))[0].path
        )
        assert log["uv_kept"] is uv
        assert out.extents == pytest.approx([2.0, 4.0, 1.0]) and out.bounds[0][0] == pytest.approx(4.0)
        mat = out.visual.material
        assert mat.baseColorTexture.mode == "RGB" and mat.baseColorTexture.size == (64, 64)
        assert mat.metallicRoughnessTexture.convert("RGB").getpixel((5, 5)) == (0, 180, 20)
        assert mat.roughnessFactor == pytest.approx(1.0) and mat.metallicFactor == pytest.approx(1.0)
    assert log["calls"][-1] == ("tex", str(weights / "ckpts" / "tex_1024"), 1024)


# --- Pixal3D ------------------------------------------------------------------------------


PIXAL_MODELS = {k: v for k, v in TRELLIS_MODELS.items() if k != "tex_slat_flow_model_512"}
PIXAL_MODELS["sparse_structure_decoder"] = "ckpts/ss_dec"


@pytest.fixture
def pixal(monkeypatch, torch, tmp_path):
    log = {"loaded": [], "encoders": [], "steps": []}

    class Model:
        def __init__(self, path):
            log["loaded"].append(path)
            self.device = "cuda"

        def eval(self):
            return self

        def cpu(self):
            self.device = "cpu"

    install_module(monkeypatch, "pixal3d.models", from_pretrained=Model)
    fake_samplers(monkeypatch, "pixal3d")

    class Encoder:
        def __init__(self, path, image_size, grid_resolution, use_naf_upsample, naf_target_size, **kw):
            self.spec = (Path(path).name, image_size, grid_resolution, naf_target_size, kw)
            self.naf_model = None
            self.device = "cpu"
            log["encoders"].append(self)

        def eval(self):
            return self

        def cuda(self):
            self.device = "cuda"

        def cpu(self):
            self.device = "cpu"

    class Upsampler:
        def load_state_dict(self, state):
            self.loaded = True

        def eval(self):
            return self

        def requires_grad_(self, flag):
            return self

    install_module(
        monkeypatch,
        "pixal3d.trainers.flow_matching.mixins.image_conditioned_proj",
        DinoV3ProjFeatureExtractor=Encoder,
        DinoV3ProjMultiViewFeatureExtractor=Encoder,
    )
    install_module(monkeypatch, "src.model.naf", NAF=Upsampler)

    class Pipeline:
        """Upstream's run in outline: the calls the shape-only subclass relies on."""

        def __init__(self, models, low_vram, **kw):
            self.models, self.low_vram, self.kw = models, low_vram, kw
            for name, value in kw.items():
                setattr(self, name, value)
            for m in models.values():
                m.eval() if hasattr(m, "eval") else None

        def cuda(self):
            log["cuda"] = True

        def sample_sparse_structure(self, *a):
            log["steps"].append("structure")

        def sample_shape_slat(self, *a):
            log["steps"].append("shape 512")

        def get_proj_cond_shape(self, encoder, *a, **kw):
            log["steps"].append("cond")
            return {}

        def sample_tex_slat(self, *a):
            raise AssertionError("the texture is never sampled")

        def decode_shape_slat(self, slat, res):
            log["decoded"] = Decoded()
            return [log["decoded"]], None

        def run(self, image, camera_params, seed, pipeline_type, preprocess_image, **kw):
            log["run"] = dict(
                camera_params=camera_params, seed=seed, pipeline_type=pipeline_type, image=image, **kw
            )
            assert not preprocess_image and "tex_slat_flow_model_1024" in self.models
            self.sample_sparse_structure()
            self.get_proj_cond_shape(self.image_cond_model_shape_512)
            self.sample_shape_slat()
            self.get_proj_cond_shape(self.image_cond_model_shape_1024)
            cond = self.get_proj_cond_shape(self.image_cond_model_tex_1024)
            tex = self.sample_tex_slat(cond, self.models["tex_slat_flow_model_1024"])
            return self.decode_latent("slat", tex, int(pipeline_type.split("_")[0]))

    class MVPipeline(Pipeline):
        def sample_tex_slat(self, *a):
            raise AssertionError("the texture is never sampled")

        def run_mv(self, views, **kw):
            log["views"] = views
            return self.run(views, camera_params={}, preprocess_image=False, **kw)

    install_module(
        monkeypatch,
        "pixal3d.pipelines",
        Pixal3DImageTo3DPipeline=Pipeline,
        Pixal3DMVImageTo3DPipeline=MVPipeline,
    )
    weights = weights_folder(tmp_path / "w", "pipeline.json", PIXAL_MODELS)
    (weights / "pipeline_mv.json").write_text((weights / "pipeline.json").read_text())
    return log, weights


def test_pixal3d_loads_local_encoders_and_naf_and_leaves_the_texture_out(pixal, torch):
    from forja_pixal3d import Pixal3D

    log, weights = pixal
    a = Pixal3D()
    a.weights_dir = weights
    a.load(Device.CUDA)
    assert len(log["loaded"]) == 5 and not any("tex" in p for p in log["loaded"])
    assert [e.spec[:4] for e in log["encoders"]] == [
        ("dinov3", 512, 16, None),
        ("dinov3", 512, 32, 512),
        ("dinov3", 1024, 64, 512),
    ]
    assert [e.naf_model is not None for e in log["encoders"]] == [False, True, True]
    assert torch.loaded == [(weights / "naf" / "naf_release.pth", True)] * 2
    assert not a.pipe.low_vram and log["cuda"] and {e.device for e in log["encoders"]} == {"cuda"}
    assert a.pipe.kw["rembg_model"] is None

    low = Pixal3D()
    low.weights_dir, low.offload = weights, True
    low.load(Device.CUDA)
    assert low.pipe.low_vram and low.pipe._device == "cuda"


def test_pixal3d_runs_the_shape_with_a_narrow_camera(tmp_path, pixal):
    from forja_pixal3d import Pixal3D

    log, weights = pixal
    a = Pixal3D()
    a.weights_dir = weights
    a.load(Device.CUDA)
    steps = []
    c = ctx(tmp_path, StageType.SHAPE, [(subject(tmp_path), ArtifactKind.IMAGE_PNG)], steps=8)
    c.progress = lambda i, n: steps.append((i, n))
    out = a.run(c)
    run = log["run"]
    fov = math.radians(11.5)
    assert run["camera_params"] == pytest.approx(
        {"camera_angle_x": fov, "distance": 0.5 / math.tan(fov / 2), "mesh_scale": 1.0}
    )
    assert run["pipeline_type"] == "1536_cascade" and run["seed"] == 7
    assert run["sparse_structure_sampler_params"] == {"steps": 8}
    assert run["image"].mode == "RGB" and run["image"].size == (108, 108)  # padded by a tenth
    assert steps == [(1, 5), (2, 5), (3, 5), (4, 5), (5, 5)]
    assert log["decoded"].filled
    mesh = mesh_ops.load(out[0].path)
    assert out[0].preview and mesh.extents == pytest.approx([1.0, 2.0, 0.5])
    assert mesh.vertices[:, 0].max() == pytest.approx(0.5)

    a.run(ctx(tmp_path, StageType.SHAPE, [(subject(tmp_path), ArtifactKind.IMAGE_PNG)], offload=True))
    assert a.pipe.low_vram and log["run"]["pipeline_type"] == "1024_cascade"
    assert {e.device for e in log["encoders"]} == {"cpu"}
    assert {m.device for m in a.pipe.models.values() if hasattr(m, "device")} == {"cpu"}
    c.cancelled = lambda: True
    with pytest.raises(Cancelled):
        a.run(c)


def test_pixal3d_glb_frame_is_upstreams_export():
    from forja_pixal3d import to_gltf

    # inference.py: to_glb's (x, z, -y), then its rotation [[-1,0,0],[0,0,-1],[0,-1,0]].
    v = np.array([[1.0, 2.0, 3.0]])
    swapped = np.stack([v[:, 0], v[:, 2], -v[:, 1]], 1)
    rot = np.array([[-1, 0, 0], [0, 0, -1], [0, -1, 0]], dtype=float)
    assert np.allclose(to_gltf(v), swapped @ rot.T)


def test_pixal3d_mv_places_each_view_and_frames_them_alike(tmp_path, pixal):
    from forja_pixal3d import Pixal3DMV, camera

    log, weights = pixal
    a = Pixal3DMV()
    a.weights_dir = weights
    a.load(Device.CUDA)
    assert log["encoders"][0].spec[4] == {"multiview_fusion": "average"}
    front = subject(tmp_path, "front.png", (300, 300), (100, 50, 200, 250))  # 100 wide, 200 tall
    side = subject(tmp_path, "side.png", (800, 800), (100, 100, 700, 500))  # 600 wide, 400 tall
    inputs = [(side, ArtifactKind.IMAGE_PNG, "left"), (front, ArtifactKind.IMAGE_PNG)]
    a.run(ctx(tmp_path, StageType.SHAPE, inputs))
    views = log["views"]
    assert views["view_names"] == ["front", "left"]
    assert views["images"][512].shape == (1, 2, 3, 512, 512) and views["images"][1024].shape[-1] == 1024
    d = 0.5 / math.tan(math.radians(11.5) / 2)
    assert np.allclose(views["camera_distance"], d)
    canonical = [[1, 0, 0, 0], [0, 0, -1, -d], [0, 1, 0, 0], [0, 0, 0, 1]]  # upstream's front camera
    assert np.allclose(views["transform_matrix"][0, 0], canonical, atol=1e-9)
    assert np.allclose(views["transform_matrix"][0, 1], camera("left", d)) and camera("left", d)[0][
        3
    ] == pytest.approx(d)

    # The same height fills the same share of each view: the side's width set the padding.
    def rows(image):
        return len(np.nonzero((image.sum(axis=0) > 0.5).any(axis=1))[0])

    left, ahead = rows(views["images"][512][0, 1]), rows(views["images"][512][0, 0])
    assert abs(left - ahead) <= 4 and left < 512 / 1.5
    with pytest.raises(ValueError, match="front"):
        a.run(ctx(tmp_path, StageType.SHAPE, [(side, ArtifactKind.IMAGE_PNG, "left")]))


# --- install ------------------------------------------------------------------------------


def test_pixal3d_install_builds_without_deps_where_asked_and_puts_naf_on_the_path(tmp_path, monkeypatch):
    monkeypatch.setattr(families, "current_os", lambda: "linux")  # the one OS its family builds on
    commands = []
    monkeypatch.setattr(
        install, "_run", lambda cmd, log, env=None, cwd=None, **kw: commands.append([str(c) for c in cmd])
    )
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")
    monkeypatch.setattr(install.toolchain, "build_env", lambda backend, log: {})
    fetched = []
    monkeypatch.setattr(
        install, "fetch_verified", lambda url, sha, cache, log, **kw: fetched.append(url) or Path("/c") / sha
    )
    monkeypatch.setattr(install, "extract_tarball", lambda archive, dest, **kw: dest)
    site = tmp_path / "site"
    site.mkdir()
    monkeypatch.setattr(install, "site_packages", lambda py: site)
    monkeypatch.setattr(install.Installer, "weights", lambda self, m: None)

    install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None).install(
        "pixal3d", lane="cuda"
    )
    assert any("TencentARC/Pixal3D" in u for u in fetched) and any("valeoai/NAF" in u for u in fetched)
    builds = [c for c in commands if "--no-build-isolation" in c]
    o_voxel = next(c for c in builds if "o_voxel" in c[-1])
    assert o_voxel[-2] == "--no-deps" and o_voxel[-1].startswith("o_voxel @ git+")
    assert all("--no-deps" not in c for c in builds if c is not o_voxel)
    env = tmp_path / "envs" / "pixal3d"
    assert (site / "forja-pixal3d-upstream.pth").read_text().splitlines() == [
        str((env / "upstream").resolve()),
        str((env / "upstream-naf").resolve()),
    ]


def test_a_build_entry_takes_no_other_option(tmp_path):
    fam = tmp_path / "x"
    (fam / "requirements").mkdir(parents=True)
    (fam / "family.yaml").write_text("name: x\nlanes: [cpu]\nbuild: ['--pre torch']\n")
    with pytest.raises(ValueError, match="--no-deps"):
        families.load("x", fam)
    assert families.split_build("--no-deps a @ git+https://x/a.git@1 ; sys_platform == 'linux'") == (
        ["--no-deps"],
        "a @ git+https://x/a.git@1 ; sys_platform == 'linux'",
    )


def test_weights_come_from_every_pinned_source(tmp_path, monkeypatch):
    specs = []
    monkeypatch.setattr(install, "_run", lambda cmd, log, **kw: specs.append(json.loads(cmd[-1])))
    blob = tmp_path / "blob"
    blob.write_bytes(b"naf")
    monkeypatch.setattr(install, "fetch_verified", lambda url, sha, cache, log, **kw: blob)
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    w = inst.manifest("pixal3d").weights
    pinned = {
        f["path"]: f["sha256"] for src in (w, *w["extra"]) for f in src.get("files", []) if "sha256" in f
    }
    monkeypatch.setattr(
        install,
        "sha256_file",
        # As posix: the pinned paths use "/", and on Windows str(path) does not.
        lambda path, **kw: next((h for p, h in pinned.items() if Path(path).as_posix().endswith(p)), "?"),
    )
    dest = inst.weights(inst.manifest("pixal3d"))
    assert [(s["repo"], Path(s["dest"])) for s in specs] == [
        ("TencentARC/Pixal3D", dest),
        ("facebook/dinov3-vitl16-pretrain-lvd1689m", dest / "dinov3"),
    ]
    assert "ckpts/slat_flow_img2shape_dit_1_3B_1024_bf16.safetensors" in specs[0]["allow"]
    assert (dest / "naf" / "naf_release.pth").read_bytes() == b"naf"


ONLY_LINUX = "can only be installed on Linux: run it on another computer or a cloud GPU"


def test_pixal3d_is_not_offered_where_its_family_cannot_be_built(tmp_path, monkeypatch):
    """NATTEN does not build on Windows. A Windows PC with the right GPU was told Pixal3D fits, and
    first-run Setup recommended a 26 GB install that failed at the build."""
    reg = Registry.load().bind(tmp_path / "envs", tmp_path / "models")
    fam = families.load("pixal3d")
    scheduler = Scheduler({"cuda:0": 11 << 30, "cpu": 8 << 30})
    started = []
    monkeypatch.setattr(install, "_run", lambda cmd, log, **kw: started.append(cmd))

    monkeypatch.setattr(families, "current_os", lambda: "windows")
    for model in ("pixal3d", "pixal3d-mv"):
        m = reg.models[model]
        assert scheduler.fit(m) == (None, "unsupported")
        check = checks.check_runs_here(m, fam, "cuda")
        assert check is not None and not check.ok
        assert (check.found, check.need) == ("Windows", "Linux")
        assert check.why == f"{m.name} {ONLY_LINUX}." and not check.fix.command
    assert scheduler.fit(reg.models["triposg"])[0] == "cuda:0"  # a family with no `os` builds anywhere
    inst = install.Installer(reg, tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    with pytest.raises(install.InstallError, match=f"Pixal3D {ONLY_LINUX}"):
        inst.install("pixal3d", lane="cuda")
    assert started == []  # refused before anything is fetched or built
    app = create_app(EngineConfig(store_root=tmp_path / "store"))
    with TestClient(app) as client:
        refused = client.post("/v1/models/pixal3d/install")
        assert refused.status_code == 409 and refused.json()["detail"] == f"Pixal3D {ONLY_LINUX}"
        listed = {m["id"]: m for m in client.get("/v1/models").json()}
        assert (listed["pixal3d"]["runs_on"], listed["pixal3d"]["fit"]) == (None, "unsupported")

    monkeypatch.setattr(families, "current_os", lambda: "linux")
    assert scheduler.fit(reg.models["pixal3d"]) == ("cuda:0", "offload")
    assert checks.check_runs_here(reg.models["pixal3d"], fam, "cuda") is None

    # A Mac is told what it was told before: the hardware, since the family has no lane for it.
    monkeypatch.setattr(families, "current_os", lambda: "macos")
    on_mac = checks.check_runs_here(reg.models["pixal3d"], fam, "mps")
    assert on_mac is not None and "hardware" in on_mac.why


def test_a_family_says_where_it_builds_and_the_schema_knows_the_field(tmp_path):
    assert families.load("pixal3d").os == ("linux",) and families.load("trellis2").os == ()
    schema = json.loads((catalog.SCHEMAS / "family.schema.json").read_text(encoding="utf-8"))
    validator = jsonschema.Draft202012Validator(schema)
    doc = {"name": "example", "lanes": ["cuda"], "os": ["linux", "windows"]}
    assert list(validator.iter_errors(doc)) == []
    assert list(validator.iter_errors({**doc, "os": ["beos"]}))
    root = tmp_path / "example"
    root.mkdir()
    (root / "family.yaml").write_text("name: example\nlanes: [cuda]\nos: [beos]\n", encoding="utf-8")
    with pytest.raises(ValueError, match="os names beos"):
        families.load("example", root)
