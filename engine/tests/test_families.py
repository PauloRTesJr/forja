"""Adapter glue for the real model families, tested against fake upstream modules.

The real models need CUDA and gigabytes of weights; these tests pin down everything Forja
owns around them: preprocessing, progress, cancellation, orientation, materials, offload.
"""

import contextlib
import math
import sys
import types
from pathlib import Path

import numpy as np
import pytest
import trimesh
import xatlas
from PIL import Image

from forja_engine.ops import mesh as mesh_ops
from forja_engine.sdk import ArtifactKind, Cancelled, Device, StageContext, StageInput, StageType

FIXTURE = Path(__file__).parent / "fixtures" / "chest.png"


def ctx(tmp_path, task, inputs, **params):
    return StageContext("s", task, [StageInput(Path(p), k, "") for p, k in inputs], params, 7, tmp_path)


def rgba_subject(tmp_path) -> Path:
    img = Image.new("RGBA", (300, 200), (0, 0, 0, 0))
    img.paste((200, 50, 50, 255), (100, 50, 200, 150))
    path = tmp_path / "subject.png"
    img.save(path)
    return path


# --- BiRefNet ---------------------------------------------------------------------------


def test_birefnet_preprocess_and_alpha():
    from forja_birefnet import preprocess, to_alpha

    x = preprocess(Image.new("RGB", (64, 32), (124, 116, 104)), 1024)
    assert x.shape == (1, 3, 1024, 1024) and x.dtype == np.float32
    assert abs(float(x.mean())) < 0.05  # ImageNet mean color normalizes to about zero
    logits = np.full((1, 1, 8, 8), -10.0, dtype=np.float32)
    logits[..., 2:6, 2:6] = 10.0
    alpha = np.asarray(to_alpha(logits, 80, 80))
    assert alpha.shape == (80, 80) and alpha[40, 40] > 250 and alpha[2, 2] < 5


def test_birefnet_runs_session_and_keeps_existing_alpha(tmp_path):
    from forja_birefnet import BiRefNet

    class Session:
        calls = 0

        def run(self, _outputs, feeds):
            Session.calls += 1
            (x,) = feeds.values()
            assert x.shape == (1, 3, 1024, 1024)
            return [np.zeros((1, 1, 1024, 1024)), np.full((1, 1, 1024, 1024), 10.0)]

    a = BiRefNet()
    a.session, a.input_name = Session(), "input_image"
    out = a.run(ctx(tmp_path, StageType.BACKGROUND_REMOVAL, [(FIXTURE, ArtifactKind.IMAGE_PNG)]))
    res = Image.open(out[0].path)
    assert res.mode == "RGBA" and np.asarray(res.getchannel("A")).min() > 250  # last output used
    a.run(ctx(tmp_path, StageType.BACKGROUND_REMOVAL, [(rgba_subject(tmp_path), ArtifactKind.IMAGE_PNG)]))
    assert Session.calls == 1  # an image with real alpha skips the model


# --- fakes for torch and upstream packages ------------------------------------------------


@pytest.fixture
def fake_torch(monkeypatch):
    torch = types.ModuleType("torch")
    torch.float16, torch.bfloat16 = "fp16", "bf16"

    class Generator:
        def __init__(self, device):
            self.device = device

        def manual_seed(self, seed):
            self.seed = seed
            return self

    torch.Generator = Generator
    torch.inference_mode = contextlib.nullcontext
    torch.cuda = types.SimpleNamespace(empty_cache=lambda: None, is_available=lambda: False)
    monkeypatch.setitem(sys.modules, "torch", torch)
    return torch


def install_module(monkeypatch, name, **attrs):
    parts = name.split(".")
    for i in range(1, len(parts) + 1):
        mod_name = ".".join(parts[:i])
        if mod_name not in sys.modules:
            monkeypatch.setitem(sys.modules, mod_name, types.ModuleType(mod_name))
    for k, v in attrs.items():
        setattr(sys.modules[name], k, v)


# --- TripoSG ------------------------------------------------------------------------------


@pytest.fixture
def triposg(monkeypatch, fake_torch):
    seen = {}

    class Pipeline:
        device = "cuda"

        @classmethod
        def from_pretrained(cls, path):
            seen["path"] = path
            return cls()

        def to(self, device, dtype):
            seen["dtype"] = dtype
            return self

        def __call__(self, image, generator, num_inference_steps, callback_on_step_end, **kw):
            seen.update(image=image, seed=generator.seed, kw=kw)
            for i in range(num_inference_steps):
                callback_on_step_end(self, i, None, {})
            box = trimesh.creation.box((1.0, 2.0, 0.5))
            return types.SimpleNamespace(samples=[(box.vertices, box.faces)])

    install_module(monkeypatch, "triposg.pipelines.pipeline_triposg", TripoSGPipeline=Pipeline)
    from forja_triposg import TripoSG

    a = TripoSG()
    a.weights_dir = Path("/weights/triposg")
    a.load(Device.CUDA)
    return a, seen


def test_triposg_prepares_like_upstream():
    from forja_triposg import prepare

    out = prepare(Image.open(FIXTURE).convert("RGBA"))
    assert out.mode == "RGB" and out.width == out.height
    assert out.getpixel((0, 0)) == (255, 255, 255)  # subject composited on white


def test_triposg_run(tmp_path, triposg):
    adapter, seen = triposg
    steps = []
    c = ctx(tmp_path, StageType.SHAPE, [(rgba_subject(tmp_path), ArtifactKind.IMAGE_PNG)], steps=4)
    c.progress = lambda i, n: steps.append((i, n))
    out = adapter.run(c)
    assert seen["path"] == str(Path("/weights/triposg")) and seen["dtype"] == "fp16" and seen["seed"] == 7
    assert steps == [(1, 4), (2, 4), (3, 4), (4, 4)]
    assert out[0].preview and mesh_ops.load(out[0].path).extents[1] == pytest.approx(2.0)


def test_triposg_yaw_and_cancel(tmp_path, triposg):
    adapter, _ = triposg
    c = ctx(
        tmp_path, StageType.SHAPE, [(rgba_subject(tmp_path), ArtifactKind.IMAGE_PNG)], steps=2, yaw_degrees=90
    )
    ext = mesh_ops.load(adapter.run(c)[0].path).extents
    assert ext[0] == pytest.approx(0.5) and ext[2] == pytest.approx(1.0)
    c.cancelled = lambda: True
    with pytest.raises(Cancelled):
        adapter.run(c)


# --- Hunyuan3D-2 Paint --------------------------------------------------------------------


@pytest.fixture
def hunyuan(monkeypatch, fake_torch):
    log = {"offload": 0, "renders": []}

    class Config:
        def __init__(self, delight, multiview, subfolder):
            log["paths"] = (delight, multiview, subfolder)
            self.render_size = self.texture_size = 2048
            self.device = "cuda"

    class Net:
        def __init__(self, config):
            self.device = config.device

    class Pipeline:
        def __init__(self, config):
            self.config = config
            log["built_on"] = config.device
            self.models = {"delight_model": Net(config), "multiview_model": Net(config)}

        def enable_model_cpu_offload(self):
            log["offload"] += 1

        def __call__(self, mesh, image):
            assert image.mode == "RGBA"
            vmap, faces, uvs = xatlas.parametrize(mesh.vertices, mesh.faces)
            mesh.vertices, mesh.faces = mesh.vertices[vmap], faces
            tex = Image.new("RGB", (self.config.texture_size,) * 2, (10, 200, 30))
            mat = trimesh.visual.texture.SimpleMaterial(image=tex)
            mesh.visual = trimesh.visual.TextureVisuals(uv=uvs, image=tex, material=mat)
            return mesh

    class MeshRender:
        def __init__(self, default_resolution, texture_size):
            log["renders"].append((default_resolution, texture_size))

    install_module(
        monkeypatch, "hy3dgen.texgen.pipelines", Hunyuan3DPaintPipeline=Pipeline, Hunyuan3DTexGenConfig=Config
    )
    install_module(monkeypatch, "hy3dgen.texgen.differentiable_renderer.mesh_render", MeshRender=MeshRender)
    from forja_hunyuan3d2 import Hunyuan3D2Paint

    a = Hunyuan3D2Paint()
    a.weights_dir = Path("/w")
    a.load(Device.CUDA)
    return a, log


def paint_inputs(tmp_path):
    mesh = trimesh.creation.box((1.0, 1.0, 0.2))
    return [
        (mesh_ops.save(mesh, tmp_path / "low.glb"), ArtifactKind.MESH_GLB),
        (rgba_subject(tmp_path), ArtifactKind.IMAGE_PNG),
    ]


def test_hunyuan_paint_writes_pbr_glb(tmp_path, hunyuan):
    adapter, log = hunyuan
    assert log["paths"] == (
        str(Path("/w/hunyuan3d-delight-v2-0")),
        str(Path("/w/hunyuan3d-paint-v2-0-turbo")),
        "hunyuan3d-paint-v2-0-turbo",
    )
    out = adapter.run(
        ctx(tmp_path, StageType.TEXTURE, paint_inputs(tmp_path), resolution=1024, roughness=0.6)
    )
    mesh = mesh_ops.load(out[0].path)
    mat = mesh.visual.material
    assert mat.roughnessFactor == pytest.approx(0.6) and mat.metallicFactor == pytest.approx(0.0)
    assert mat.baseColorTexture.size == (1024, 1024)
    assert log["renders"] == [(2048, 1024)]


def test_hunyuan_offloads_once_and_yaw_round_trips(tmp_path, hunyuan):
    adapter, log = hunyuan
    for _ in range(2):
        out = adapter.run(
            ctx(tmp_path, StageType.TEXTURE, paint_inputs(tmp_path), offload=True, yaw_degrees=90)
        )
    assert log["offload"] == 1
    ext = mesh_ops.load(out[0].path).extents
    assert ext[0] == pytest.approx(1.0) and ext[2] == pytest.approx(0.2)  # rotated back after painting
    assert math.isclose(adapter.sizes[1], 2048)


def test_hunyuan_offload_at_load_builds_in_ram(tmp_path, hunyuan):
    from forja_hunyuan3d2 import Hunyuan3D2Paint

    _, log = hunyuan
    assert log["built_on"] == "cuda" and log["offload"] == 0
    a = Hunyuan3D2Paint()
    a.weights_dir, a.offload = Path("/w"), True
    a.load(Device.CUDA)
    assert log["built_on"] == "cpu" and log["offload"] == 1
    assert {n.device for n in a.pipe.models.values()} == {"cuda"}
    a.run(ctx(tmp_path, StageType.TEXTURE, paint_inputs(tmp_path), offload=True))
    assert log["offload"] == 1  # not enabled a second time at run
