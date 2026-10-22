"""Hunyuan3D-2 Paint: multiview diffusion texturing with delighting.

Licensed under the Tencent Hunyuan 3D 2.0 Community License (not in the EU, UK or South
Korea; separate terms above 1M monthly active users). Upstream code is fetched at install
time and never committed to Forja.

Output is base color only, with constant roughness and metallic (docs/decisions.md, 6).
Upstream re-unwraps UVs with xatlas, so the input mesh's UVs are replaced.
"""

from __future__ import annotations

import gc
import math
from typing import Any

import trimesh
from PIL import Image

from forja_engine.ops import mesh as mesh_ops
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
DELIGHT = "hunyuan3d-delight-v2-0"
PAINT_TURBO = "hunyuan3d-paint-v2-0-turbo"


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


class Hunyuan3D2Paint(Adapter):
    version = "1"

    def __init__(self) -> None:
        self.pipe: Any = None
        self.torch: Any = None
        self.offloaded = False
        self.sizes: tuple[int, int] | None = None

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.TEXTURE,), devices=(Device.CUDA,), vram_bytes=10 * GB)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        import torch
        from hy3dgen.texgen.pipelines import Hunyuan3DPaintPipeline, Hunyuan3DTexGenConfig

        if self.weights_dir is None:
            raise RuntimeError("hunyuan3d2: no weights folder; run forja models install")
        self.torch = torch
        # Built from local pinned weights so from_pretrained never reaches the network.
        config = Hunyuan3DTexGenConfig(
            str(self.weights_dir / DELIGHT), str(self.weights_dir / PAINT_TURBO), PAINT_TURBO
        )
        self.pipe = Hunyuan3DPaintPipeline(config)
        self.sizes = (config.render_size, config.texture_size)

    def unload(self) -> None:
        self.pipe = None
        self.offloaded = False
        gc.collect()
        if self.torch is not None:
            self.torch.cuda.empty_cache()

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
        image = Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path).convert("RGBA")
        yaw = float(ctx.params.get("yaw_degrees", 0))
        rotate_y(mesh, yaw)
        ctx.progress(1, 3)
        ctx.check_cancel()

        painted = self.pipe(mesh, image=image)
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
        return [StageOutput(path, ArtifactKind.MESH_GLB)]
