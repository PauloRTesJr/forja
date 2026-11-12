"""Hunyuan3D-2: Paint (multiview diffusion texturing with delighting) and 2mv (shape from up
to four views of the subject).

Licensed under the Tencent Hunyuan 3D 2.0 Community License (not in the EU, UK or South
Korea; separate terms above 1M monthly active users). Upstream code is fetched at install
time and never committed to Forja.

Paint's output is base color only, with constant roughness and metallic (docs/decisions.md, 6).
Upstream unwraps UVs with xatlas; a mesh that already has UVs (a preset's UV stage, such as
Blender's Smart UV Project) keeps them.

Upstream draws its six views at 512 px with a fixed seed. Forja seeds that diffusion with the
job's seed and, when the Real-ESRGAN file is installed (upscale.py), enlarges each view 4x
before upstream resizes it to the render size.
"""

from __future__ import annotations

import gc
import math
import os
from typing import Any

import trimesh
from PIL import Image

from forja_engine.ops import mesh as mesh_ops
from forja_engine.sdk import (
    VIEWS,
    Adapter,
    ArtifactKind,
    Cancelled,
    Capabilities,
    Device,
    StageContext,
    StageOutput,
    StageType,
)

from .upscale import WEIGHTS as UPSCALER_WEIGHTS
from .upscale import SharpenedViews, Upscaler

GB = 1 << 30
DELIGHT = "hunyuan3d-delight-v2-0"
PAINT_TURBO = "hunyuan3d-paint-v2-0-turbo"
MV_TURBO = "hunyuan3d-dit-v2-mv-turbo"
NOT_SHARPENED = "The texture was not sharpened: {why}"
UPSCALER_MISSING = "repair Hunyuan3D-2 Paint on the Models screen to fetch its upscaler."
UPSCALER_FAILED = "the upscaler failed on this computer's GPU (see the engine log)."


def texture_image(mesh: trimesh.Trimesh) -> Image.Image:
    """The baked texture from upstream's SimpleMaterial (or a PBR material, if that changes)."""
    material = getattr(mesh.visual, "material", None)
    for attr in ("image", "baseColorTexture"):
        img = getattr(material, attr, None)
        if img is not None:
            return img.convert("RGB")
    raise RuntimeError("hunyuan3d2: painted mesh has no texture image")


def rotate_y(mesh: trimesh.Trimesh, degrees: float) -> None:
    if degrees:
        mesh.apply_transform(trimesh.transformations.rotation_matrix(math.radians(degrees), [0, 1, 0]))


def keep_uvs(wrap: Any) -> Any:
    """Upstream's unwrap, skipped for a mesh that brings its own UVs."""
    if getattr(wrap, "keeps_uvs", False):
        return wrap

    def unwrap(mesh: Any) -> Any:
        uv = getattr(getattr(mesh, "visual", None), "uv", None)
        if uv is not None and len(uv) == len(mesh.vertices):
            return mesh
        return wrap(mesh)

    unwrap.keeps_uvs = True  # type: ignore[attr-defined]
    return unwrap


class SeededPipeline:
    """The multiview diffusion pipeline, drawing its noise from the job's seed.

    Upstream's Multiview_Diffusion_Net passes `generator=Generator(...).manual_seed(0)` on every
    call, so a job's seed never changed the paint. This replaces that generator with one on the
    same device, seeded by `seed`; every other attribute is the wrapped pipeline's.
    """

    def __init__(self, pipeline: Any, torch: Any) -> None:
        object.__setattr__(self, "_pipeline", pipeline)
        object.__setattr__(self, "_torch", torch)
        object.__setattr__(self, "seed", 0)

    def __getattr__(self, name: str) -> Any:
        return getattr(self._pipeline, name)

    def __setattr__(self, name: str, value: Any) -> None:
        if name == "seed":
            object.__setattr__(self, name, value)
        else:
            setattr(self._pipeline, name, value)

    def __call__(self, *args: Any, **kwargs: Any) -> Any:
        if (upstream := kwargs.get("generator")) is not None:
            kwargs["generator"] = self._torch.Generator(device=upstream.device).manual_seed(self.seed)
        return self._pipeline(*args, **kwargs)


class Hunyuan3D2Paint(Adapter):
    # 3: the job's seed reaches the multiview diffusion, and its 512 px views are upscaled 4x
    # (Real-ESRGAN) before baking. 2: keeps the UVs of a mesh that has them instead of unwrapping
    # it again.
    version = "3"

    def __init__(self) -> None:
        self.pipe: Any = None
        self.torch: Any = None
        self.offloaded = False
        self.sizes: tuple[int, int] | None = None
        self.seeded: SeededPipeline | None = None
        self.sharpened: SharpenedViews | None = None
        self.upscaler: Upscaler | None = None

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.TEXTURE,), devices=(Device.CUDA,), vram_bytes=int(12.5 * GB))

    def load(self, device: Device, dtype: str = "fp16") -> None:
        import torch
        from hy3dgen.texgen import pipelines
        from hy3dgen.texgen.pipelines import Hunyuan3DPaintPipeline, Hunyuan3DTexGenConfig

        if self.weights_dir is None:
            raise RuntimeError("hunyuan3d2: no weights folder; run forja models install")
        self.torch = torch
        # The pipeline calls the module's mesh_uv_wrap on every mesh it paints.
        pipelines.mesh_uv_wrap = keep_uvs(pipelines.mesh_uv_wrap)
        # Built from local pinned weights so from_pretrained never reaches the network.
        config = Hunyuan3DTexGenConfig(
            str(self.weights_dir / DELIGHT), str(self.weights_dir / PAINT_TURBO), PAINT_TURBO
        )
        if self.offload:
            # Build both diffusion pipelines in RAM: upstream moves them to config.device at
            # construction, and that full load is what overflows a 12 GB card.
            config.device = "cpu"
        self.pipe = Hunyuan3DPaintPipeline(config)
        # Before the loop below and the offload hooks: they reach the wrappers, which pass
        # `.device` and `.pipeline` on to the objects upstream built.
        net = self.pipe.models["multiview_model"]
        self.seeded = SeededPipeline(net.pipeline, torch)
        net.pipeline = self.seeded
        self.upscaler = Upscaler(self.weights_dir / UPSCALER_WEIGHTS)
        self.sharpened = SharpenedViews(net, self.upscaler)
        self.pipe.models["multiview_model"] = self.sharpened
        if self.offload:
            for net in self.pipe.models.values():
                net.device = "cuda"  # inputs go where the offload hooks run each model
            self.pipe.enable_model_cpu_offload()
            self.offloaded = True
        self.sizes = (config.render_size, config.texture_size)

    def unload(self) -> None:
        self.pipe = self.seeded = self.sharpened = self.upscaler = None
        self.offloaded = False
        gc.collect()
        if self.torch is not None:
            self.torch.cuda.empty_cache()

    def _sharpen(self, wanted: bool) -> str:
        """Turns the upscaler on for this run; returns why it is off when the person asked for it."""
        assert self.sharpened is not None and self.upscaler is not None
        self.sharpened.failed = ""
        self.sharpened.enabled = wanted and self.upscaler.ready()
        return UPSCALER_MISSING if wanted and not self.sharpened.enabled else ""

    def _configure(self, render_size: int, texture_size: int) -> None:
        if self.sizes == (render_size, texture_size):
            return
        from hy3dgen.texgen.differentiable_renderer.mesh_render import MeshRender

        self.pipe.config.render_size = render_size
        self.pipe.config.texture_size = texture_size
        self.pipe.render = MeshRender(default_resolution=render_size, texture_size=texture_size)
        self.sizes = (render_size, texture_size)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        if ctx.params.get("offload") and not self.offloaded:
            # Set by the VRAM manager in fit-first mode when the model exceeds the budget.
            self.pipe.enable_model_cpu_offload()
            self.offloaded = True
        resolution = int(ctx.params.get("resolution", 2048))
        self._configure(int(ctx.params.get("render_size", 2048)), resolution)

        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        views = ctx.views()
        # Upstream wraps a single image in a list, so the front alone paints as it always did.
        images = [Image.open(views[v].path).convert("RGBA") for v in VIEWS if v in views]
        yaw = float(ctx.params.get("yaw_degrees", 0))
        rotate_y(mesh, yaw)
        ctx.progress(1, 3)
        ctx.check_cancel()

        assert self.seeded is not None and self.sharpened is not None
        self.seeded.seed = ctx.seed
        why = self._sharpen(bool(ctx.params.get("upscale", True)))
        painted = self.pipe(mesh, image=images)
        why = why or (UPSCALER_FAILED if self.sharpened.failed else "")
        ctx.progress(2, 3)
        ctx.check_cancel()

        rotate_y(painted, -yaw)
        out = mesh_ops.textured(
            painted,
            texture_image(painted),
            roughness=float(ctx.params.get("roughness", 0.8)),
            metallic=float(ctx.params.get("metallic", 0.0)),
        )
        path = mesh_ops.save(out, ctx.out_dir / "textured.glb")
        ctx.progress(3, 3)
        return [StageOutput(path, ArtifactKind.MESH_GLB, note=NOT_SHARPENED.format(why=why) if why else "")]


class Hunyuan3D2MV(Adapter):
    """Hunyuan3D-2mv turbo: shape from the front plus any of the left, back and right views.

    Its output is in the frame Paint expects (upstream's multi-view example paints it unrotated).
    """

    version = "1"

    def __init__(self) -> None:
        self.pipe: Any = None
        self.torch: Any = None
        self.device = Device.CUDA

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CUDA, Device.MPS), vram_bytes=6 * GB)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        if device is Device.MPS:
            # Upstream's image encoder resizes on the device with antialias=True, and torch 2.5.1
            # has no MPS kernel for aten::_upsample_bilinear2d_aa: that op (and any other without
            # one) runs on the CPU instead of failing the stage. torch reads this when it loads.
            os.environ.setdefault("PYTORCH_ENABLE_MPS_FALLBACK", "1")
        import torch
        from hy3dgen.shapegen.pipelines import Hunyuan3DDiTFlowMatchingPipeline

        if self.weights_dir is None:
            raise RuntimeError("hunyuan3d2-mv: no weights folder; run forja models install")
        self.torch = torch
        self.device = device
        folder = self.weights_dir / MV_TURBO
        # From the pinned files: from_pretrained resolves the model through the Hub. fp16 on
        # every device: the weights are fp16, and bf16 needs macOS 14 on MPS.
        self.pipe = Hunyuan3DDiTFlowMatchingPipeline.from_single_file(
            str(folder / "model.fp16.safetensors"),
            str(folder / "config.yaml"),
            device="mps" if device is Device.MPS else "cuda",
            dtype=torch.float16,
            use_safetensors=True,
        )

    def unload(self) -> None:
        self.pipe = None
        gc.collect()
        if self.torch is not None:
            (self.torch.mps if self.device is Device.MPS else self.torch.cuda).empty_cache()

    def run(self, ctx: StageContext) -> list[StageOutput]:
        torch = self.torch
        views = ctx.views()
        if "front" not in views:
            raise ValueError("hunyuan3d2-mv needs a front view")
        images = {v: Image.open(views[v].path).convert("RGBA") for v in VIEWS if v in views}
        steps = int(ctx.params.get("steps", 5))

        def on_step(i: int, _t: Any, _outputs: Any) -> None:
            ctx.progress(i + 1, steps)
            if ctx.cancelled():
                raise Cancelled(ctx.stage_id)

        # Diffusers draws an MPS pipeline's noise on the CPU when the generator is there, which
        # gives a seed the same noise on every Mac.
        noise_on = "cpu" if self.device is Device.MPS else self.pipe.device
        meshes = self.pipe(
            image=images,
            num_inference_steps=steps,
            guidance_scale=float(ctx.params.get("guidance_scale", 5.0)),
            octree_resolution=int(ctx.params.get("octree_resolution", 380)),
            num_chunks=20000,
            generator=torch.Generator(device=noise_on).manual_seed(ctx.seed),
            output_type="trimesh",
            callback=on_step,
            callback_steps=1,
        )
        mesh = meshes[0] if meshes else None
        if mesh is None:
            raise RuntimeError("hunyuan3d2-mv found no surface; check that each view shows the subject")
        rotate_y(mesh, float(ctx.params.get("yaw_degrees", 0)))
        path = mesh_ops.save(mesh, ctx.out_dir / "shape.glb")
        return [StageOutput(path, ArtifactKind.MESH_GLB, preview=True)]
