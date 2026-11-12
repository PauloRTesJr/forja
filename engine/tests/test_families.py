"""Adapter glue for the real model families, tested against fake upstream modules.

The real models need CUDA and gigabytes of weights; these tests pin down everything Forja
owns around them: preprocessing, progress, cancellation, orientation, materials, offload.
"""

import contextlib
import importlib
import math
import os
import re
import sys
import types
from pathlib import Path

import numpy as np
import pytest
import trimesh
import xatlas
from PIL import Image

from forja_engine.ops import mesh as mesh_ops
from forja_engine.registry import Registry
from forja_engine.sdk import ArtifactKind, Cancelled, Device, StageContext, StageInput, StageType

FIXTURE = Path(__file__).parent / "fixtures" / "chest.png"


def ctx(tmp_path, task, inputs, **params):
    """inputs are (path, kind) or (path, kind, view)."""
    return StageContext(
        "s", task, [StageInput(Path(i[0]), i[1], "", *i[2:]) for i in inputs], params, 7, tmp_path
    )


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
    log = {"offload": 0, "renders": [], "inner_offload": [], "generators": []}

    class Config:
        def __init__(self, delight, multiview, subfolder):
            log["paths"] = (delight, multiview, subfolder)
            self.render_size = self.texture_size = 2048
            self.device = "cuda"

    class Diffusion:
        """What upstream calls `net.pipeline`: a diffusers pipeline."""

        device = "cuda"

        def __init__(self, name):
            self.name = name

        def enable_model_cpu_offload(self, **kwargs):
            log["inner_offload"].append(self.name)

        def __call__(self, images, num_inference_steps, generator, **kwargs):
            log["generators"].append((generator.device, generator.seed))
            return types.SimpleNamespace(
                images=[Image.new("RGB", (512, 512), (40 * i, 0, 0)) for i in range(6)]
            )

    class Net:
        def __init__(self, config, name):
            self.device = config.device
            self.pipeline = Diffusion(name)

        def __call__(self, images, controls, camera_info):
            # As Multiview_Diffusion_Net.__call__ does: its generator is always seeded with 0.
            seed0 = sys.modules["torch"].Generator(device=self.pipeline.device).manual_seed(0)
            return self.pipeline(images, num_inference_steps=30, generator=seed0).images

    class Pipeline:
        def __init__(self, config):
            self.config = config
            log["built_on"] = config.device
            self.models = {
                "delight_model": Net(config, "delight"),
                "multiview_model": Net(config, "multiview"),
            }

        def enable_model_cpu_offload(self):
            log["offload"] += 1
            for net in self.models.values():
                net.pipeline.enable_model_cpu_offload()

        def __call__(self, mesh, image):
            assert all(i.mode == "RGBA" for i in image)
            log["images"] = len(image)
            views = self.models["multiview_model"](image, [], [])
            log["view_sizes"] = [v.size for v in views]
            log["baked_from"] = [v.resize((self.config.render_size,) * 2) for v in views]  # as upstream does
            mesh = sys.modules["hy3dgen.texgen.pipelines"].mesh_uv_wrap(mesh)  # as upstream does
            tex = Image.new("RGB", (self.config.texture_size,) * 2, (10, 200, 30))
            mat = trimesh.visual.texture.SimpleMaterial(image=tex)
            mesh.visual = trimesh.visual.TextureVisuals(uv=mesh.visual.uv, image=tex, material=mat)
            return mesh

    def mesh_uv_wrap(mesh):
        log["unwrapped"] = log.get("unwrapped", 0) + 1
        vmap, faces, uvs = xatlas.parametrize(mesh.vertices, mesh.faces)
        mesh.vertices, mesh.faces = mesh.vertices[vmap], faces
        mesh.visual = trimesh.visual.TextureVisuals(uv=uvs)
        return mesh

    class MeshRender:
        def __init__(self, default_resolution, texture_size):
            log["renders"].append((default_resolution, texture_size))

    install_module(
        monkeypatch,
        "hy3dgen.texgen.pipelines",
        Hunyuan3DPaintPipeline=Pipeline,
        Hunyuan3DTexGenConfig=Config,
        mesh_uv_wrap=mesh_uv_wrap,
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


def test_hunyuan_paint_keeps_the_uvs_of_a_mesh_that_has_them(tmp_path, hunyuan):
    adapter, log = hunyuan
    adapter.run(ctx(tmp_path, StageType.TEXTURE, paint_inputs(tmp_path)))
    assert log["unwrapped"] == 1  # a decimated mesh has none: upstream unwraps it
    unwrapped = mesh_ops.unwrap(trimesh.creation.box((1.0, 1.0, 0.2)))
    inputs = [
        (mesh_ops.save(unwrapped, tmp_path / "uv.glb"), ArtifactKind.MESH_GLB),
        paint_inputs(tmp_path)[1],
    ]
    out = mesh_ops.load(adapter.run(ctx(tmp_path, StageType.TEXTURE, inputs))[0].path)
    assert log["unwrapped"] == 1
    assert np.allclose(
        np.sort(mesh_ops.uvs_of(out), axis=0), np.sort(mesh_ops.uvs_of(unwrapped), axis=0), atol=1e-6
    )
    adapter.load(Device.CUDA)  # loading again wraps upstream's unwrap once, not twice
    adapter.run(ctx(tmp_path, StageType.TEXTURE, paint_inputs(tmp_path)))
    assert log["unwrapped"] == 2


def test_hunyuan_paint_reads_every_view(tmp_path, hunyuan):
    adapter, log = hunyuan
    adapter.run(ctx(tmp_path, StageType.TEXTURE, paint_inputs(tmp_path)))
    assert log["images"] == 1
    side = (rgba_subject(tmp_path), ArtifactKind.IMAGE_PNG, "left")
    adapter.run(ctx(tmp_path, StageType.TEXTURE, [*paint_inputs(tmp_path), side]))
    assert log["images"] == 2


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


def paint_seeded(adapter, tmp_path, seed, **params):
    c = ctx(tmp_path, StageType.TEXTURE, paint_inputs(tmp_path), **params)
    c.seed = seed
    return adapter.run(c)[0]


def test_hunyuan_paint_draws_its_views_from_the_job_seed(tmp_path, hunyuan):
    adapter, log = hunyuan
    paint_seeded(adapter, tmp_path, 7)
    paint_seeded(adapter, tmp_path, 8)
    paint_seeded(adapter, tmp_path, 7)
    # Upstream asked for seed 0 every time; the generator keeps its device.
    assert log["generators"] == [("cuda", 7), ("cuda", 8), ("cuda", 7)]
    assert log["inner_offload"] == []  # nothing offloaded unless asked


class Upscaler:
    """Stands in for the Real-ESRGAN network: the real one needs torch and a GPU."""

    def __init__(self):
        self.calls, self.placed = [], []

    def half(self):
        return self

    def to(self, device):
        self.placed.append(device)
        return self


@pytest.fixture
def upscaler(monkeypatch, hunyuan):
    from forja_hunyuan3d2 import upscale

    adapter, log = hunyuan
    net = Upscaler()
    monkeypatch.setattr(upscale, "load_network", lambda path: net)

    def enlarge(self, network, image):
        net.calls.append(image.size)
        return image.resize((image.width * 4, image.height * 4))

    monkeypatch.setattr(upscale.Upscaler, "_one", enlarge)
    return adapter, log, net


def test_hunyuan_paint_sharpens_each_view_before_baking(tmp_path, upscaler):
    adapter, log, net = upscaler
    out = paint_seeded(adapter, tmp_path, 7)
    assert log["view_sizes"] == [(2048, 2048)] * 6  # 512 px views, 4x
    assert net.calls == [(512, 512)] * 6
    assert net.placed == ["cuda", "cpu"]  # on the GPU for the pass only
    assert [v.size for v in log["baked_from"]] == [(2048, 2048)] * 6
    assert out.note == ""
    assert adapter.sharpened.enabled


def test_hunyuan_paint_can_leave_its_views_as_they_are(tmp_path, upscaler):
    adapter, log, net = upscaler
    out = paint_seeded(adapter, tmp_path, 7, upscale=False)
    assert log["view_sizes"] == [(512, 512)] * 6 and net.calls == [] and out.note == ""
    paint_seeded(adapter, tmp_path, 7)  # and the next job asks again
    assert len(net.calls) == 6


def test_hunyuan_paint_says_when_it_has_no_upscaler(tmp_path, hunyuan, caplog):
    adapter, log = hunyuan  # /w/realesrgan/RealESRGAN_x4plus.pth does not exist
    out = paint_seeded(adapter, tmp_path, 7)
    assert log["view_sizes"] == [(512, 512)] * 6
    assert out.note == (
        "The texture was not sharpened: repair Hunyuan3D-2 Paint on the Models screen to fetch its upscaler."
    )
    assert "will not sharpen" in caplog.text
    assert paint_seeded(adapter, tmp_path, 7, upscale=False).note == ""  # asked not to: nothing to say


def test_hunyuan_paint_skips_an_upscaler_whose_weights_do_not_load(tmp_path, hunyuan, monkeypatch):
    from forja_hunyuan3d2 import upscale

    adapter, log = hunyuan

    def refuse(path):
        raise upscale.Unavailable("not a Real-ESRGAN x4plus checkpoint")

    monkeypatch.setattr(upscale, "load_network", refuse)
    out = paint_seeded(adapter, tmp_path, 7)
    assert log["view_sizes"] == [(512, 512)] * 6 and "upscaler" in out.note


def test_hunyuan_paint_keeps_its_views_when_the_upscaler_runs_out_of_memory(tmp_path, upscaler, monkeypatch):
    from forja_hunyuan3d2 import upscale

    adapter, log, net = upscaler

    def oom(self, network, image):
        raise RuntimeError("CUDA out of memory")

    monkeypatch.setattr(upscale.Upscaler, "_one", oom)
    out = paint_seeded(adapter, tmp_path, 7)
    assert log["view_sizes"] == [(512, 512)] * 6  # the diffusion is not thrown away
    assert out.note.startswith("The texture was not sharpened") and net.placed == ["cuda", "cpu"]
    assert paint_seeded(adapter, tmp_path, 7, upscale=False).note == ""  # the flag resets per run


def test_hunyuan_paint_finds_an_upscaler_fetched_after_it_loaded(tmp_path, hunyuan, monkeypatch):
    from forja_hunyuan3d2 import upscale

    adapter, _ = hunyuan
    adapter.weights_dir = tmp_path
    adapter.upscaler.path = tmp_path / upscale.WEIGHTS
    net = Upscaler()

    def load(path):
        if not path.is_file():
            raise upscale.Unavailable(f"{path} is missing")
        return net

    monkeypatch.setattr(upscale, "load_network", load)
    assert paint_seeded(adapter, tmp_path, 7).note  # no file yet
    adapter.upscaler.path.parent.mkdir(parents=True)
    adapter.upscaler.path.write_bytes(b"weights")  # a repair
    monkeypatch.setattr(upscale.Upscaler, "_one", lambda self, network, image: image)
    assert paint_seeded(adapter, tmp_path, 7).note == ""


def test_the_rrdbnet_enlarges_4x_and_names_its_weights_like_the_checkpoint(tmp_path):
    torch = pytest.importorskip("torch")
    from forja_hunyuan3d2 import upscale

    net = upscale.rrdbnet().eval()
    with torch.inference_mode():
        assert tuple(net(torch.rand(1, 3, 16, 16)).shape) == (1, 3, 64, 64)
    names = set(net.state_dict())
    block = r"body\.\d+\.rdb[123]\.conv[1-5]\.(weight|bias)"
    top = ("conv_first", "conv_body", "conv_up1", "conv_up2", "conv_hr", "conv_last")
    assert all(re.fullmatch(block, n) or n.split(".")[0] in top for n in names)
    assert len(names) == 23 * 3 * 5 * 2 + len(top) * 2
    # The file as Real-ESRGAN releases it: weights under params_ema.
    path = tmp_path / "x4.pth"
    torch.save({"params_ema": net.state_dict()}, path)
    loaded = upscale.load_network(path)
    assert not loaded.training and not any(p.requires_grad for p in loaded.parameters())
    torch.save({"params": net.state_dict()}, path)
    with pytest.raises(upscale.Unavailable, match="not a Real-ESRGAN"):
        upscale.load_network(path)
    with pytest.raises(upscale.Unavailable, match="missing"):
        upscale.load_network(tmp_path / "nothing.pth")
    # The upscaler end to end on the CPU: any image, 4x, RGB.
    torch.save({"params_ema": net.state_dict()}, path)
    out = upscale.Upscaler(path, device="cpu")(
        [Image.new("RGB", (16, 8), (200, 30, 30)), Image.new("RGBA", (8, 8), (0, 0, 0, 0))]
    )
    assert [(o.size, o.mode) for o in out] == [((64, 32), "RGB"), ((32, 32), "RGB")]


def test_the_wrappers_pass_the_offload_hooks_and_the_device_on(tmp_path, hunyuan):
    from forja_hunyuan3d2 import Hunyuan3D2Paint

    _, log = hunyuan
    a = Hunyuan3D2Paint()
    a.weights_dir, a.offload = Path("/w"), True
    a.load(Device.CUDA)
    # What upstream's enable_model_cpu_offload reaches: each model's own diffusers pipeline.
    assert log["inner_offload"] == ["delight", "multiview"]
    models = a.pipe.models
    assert models["multiview_model"] is a.sharpened
    assert {n.device for n in models.values()} == {"cuda"}  # the loop set it through the wrapper
    inner = a.sharpened._net
    assert inner.device == "cuda" and inner.pipeline is a.seeded
    assert a.seeded.device == "cuda" and a.seeded._pipeline.device == "cuda"
    a.unload()
    assert a.pipe is None and a.upscaler is None


# --- Hunyuan3D-2mv -------------------------------------------------------------------------


@pytest.fixture
def hunyuan_mv(monkeypatch, fake_torch):
    seen = {}

    class Pipeline:
        device = "cuda"

        @classmethod
        def from_single_file(cls, ckpt, config, device, dtype, use_safetensors):
            seen.update(ckpt=ckpt, config=config, device=device, dtype=dtype, safetensors=use_safetensors)
            return cls()

        def __call__(self, image, num_inference_steps, generator, callback, callback_steps, **kw):
            seen.update(views=list(image), modes={i.mode for i in image.values()}, kw=kw)
            seen.update(seed=generator.seed, noise_on=generator.device)
            for i in range(num_inference_steps):
                callback(i, None, None)
            return [trimesh.creation.box((1.0, 2.0, 0.5))]

    install_module(monkeypatch, "hy3dgen.shapegen.pipelines", Hunyuan3DDiTFlowMatchingPipeline=Pipeline)
    from forja_hunyuan3d2 import Hunyuan3D2MV

    a = Hunyuan3D2MV()
    a.weights_dir = Path("/w")
    a.load(Device.CUDA)
    return a, seen


def test_hunyuan_mv_loads_the_pinned_files_and_reads_views_in_order(tmp_path, hunyuan_mv):
    adapter, seen = hunyuan_mv
    folder = Path("/w/hunyuan3d-dit-v2-mv-turbo")
    assert seen["ckpt"] == str(folder / "model.fp16.safetensors") and seen["config"] == str(
        folder / "config.yaml"
    )
    assert seen["safetensors"] and seen["dtype"] == "fp16"
    img = rgba_subject(tmp_path)
    steps = []
    c = ctx(
        tmp_path,
        StageType.SHAPE,
        [
            (img, ArtifactKind.IMAGE_PNG, "back"),
            (img, ArtifactKind.IMAGE_PNG),
            (img, ArtifactKind.IMAGE_PNG, "left"),
        ],
        steps=3,
    )
    c.progress = lambda i, n: steps.append((i, n))
    out = adapter.run(c)
    assert seen["views"] == ["front", "left", "back"] and seen["modes"] == {"RGBA"} and seen["seed"] == 7
    assert seen["device"] == "cuda" and seen["noise_on"] == "cuda"
    assert steps == [(1, 3), (2, 3), (3, 3)]
    assert out[0].preview and mesh_ops.load(out[0].path).extents[1] == pytest.approx(2.0)


def test_hunyuan_mv_on_an_apple_gpu(tmp_path, hunyuan_mv, monkeypatch):
    from forja_hunyuan3d2 import Hunyuan3D2MV

    _, seen = hunyuan_mv
    monkeypatch.delenv("PYTORCH_ENABLE_MPS_FALLBACK", raising=False)
    cleared = []
    sys.modules["torch"].mps = types.SimpleNamespace(empty_cache=lambda: cleared.append("mps"))
    a = Hunyuan3D2MV()
    a.weights_dir = Path("/w")
    a.load(Device.MPS)
    assert seen["device"] == "mps" and seen["dtype"] == "fp16"
    # Set before torch loads, so ops without an MPS kernel run on the CPU instead of failing.
    assert os.environ["PYTORCH_ENABLE_MPS_FALLBACK"] == "1"
    a.run(ctx(tmp_path, StageType.SHAPE, [(rgba_subject(tmp_path), ArtifactKind.IMAGE_PNG)], steps=1))
    assert seen["noise_on"] == "cpu" and seen["seed"] == 7  # the same noise on every Mac
    a.unload()
    assert cleared == ["mps"]


def test_hunyuan_mv_needs_a_front_and_stops_when_cancelled(tmp_path, hunyuan_mv):
    adapter, _ = hunyuan_mv
    img = rgba_subject(tmp_path)
    with pytest.raises(ValueError, match="front"):
        adapter.run(ctx(tmp_path, StageType.SHAPE, [(img, ArtifactKind.IMAGE_PNG, "left")]))
    c = ctx(tmp_path, StageType.SHAPE, [(img, ArtifactKind.IMAGE_PNG)], steps=2)
    c.cancelled = lambda: True
    with pytest.raises(Cancelled):
        adapter.run(c)


def test_every_manifest_mirrors_its_adapter_version():
    """The core reads a model's adapter_version from its manifest, so it must follow the class."""
    for m in Registry.load().models.values():
        module, _, name = m.adapter.partition(":")
        version = getattr(importlib.import_module(module), name).version
        assert m.adapter_version == version, f"{m.id}: set adapter_version: {version!r} in its manifest"


def test_license_restrictions_stay_whole():
    from forja_engine.registry import Registry

    # A YAML flow list splits at commas inside an unquoted item.
    restrictions = Registry.load().models["hunyuan3d2-paint"].license.restrictions
    assert restrictions[0] == "not licensed in the EU, UK or South Korea"
