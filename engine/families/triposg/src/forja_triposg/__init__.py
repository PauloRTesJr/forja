"""TripoSG image-to-shape (MIT), VAST-AI-Research.

The upstream source is fetched at install time and put on the path; weights come from
VAST-AI/TripoSG at a pinned revision. CUDA only: its mesh extraction needs the diso kernel.
"""

from __future__ import annotations

import gc
import math
from typing import Any

import numpy as np
import trimesh
from PIL import Image

from forja_engine.ops import image as img_ops
from forja_engine.ops import mesh as mesh_ops
from forja_engine.sdk import (
    Adapter,
    ArtifactKind,
    Cancelled,
    Capabilities,
    Device,
    StageContext,
    StageOutput,
    StageType,
)

GB = 1 << 30


def prepare(img: Image.Image, padding_ratio: float = 0.1) -> Image.Image:
    """Match upstream preprocessing: subject on white, cropped, padded to a centered square."""
    rgba = img.convert("RGBA")
    x0, y0, x1, y1 = img_ops.alpha_bbox(rgba, threshold=1)
    subject = rgba.crop((x0, y0, x1, y1))
    w, h = subject.size
    side = int(max(w, h) * (1 + 2 * padding_ratio))
    canvas = Image.new("RGBA", (side, side), (255, 255, 255, 255))
    canvas.alpha_composite(subject, ((side - w) // 2, (side - h) // 2))
    return canvas.convert("RGB")


def yaw(mesh: trimesh.Trimesh, degrees: float) -> trimesh.Trimesh:
    if degrees:
        mesh.apply_transform(trimesh.transformations.rotation_matrix(math.radians(degrees), [0, 1, 0]))
    return mesh


class TripoSG(Adapter):
    version = "1"

    def __init__(self) -> None:
        self.pipe: Any = None
        self.torch: Any = None

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CUDA,), vram_bytes=8 * GB)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        import torch
        from triposg.pipelines.pipeline_triposg import TripoSGPipeline

        if self.weights_dir is None:
            raise RuntimeError("triposg: no weights folder; run forja models install")
        self.torch = torch
        torch_dtype = torch.bfloat16 if dtype == "bf16" else torch.float16
        self.pipe = TripoSGPipeline.from_pretrained(str(self.weights_dir)).to("cuda", torch_dtype)

    def unload(self) -> None:
        self.pipe = None
        gc.collect()
        if self.torch is not None:
            self.torch.cuda.empty_cache()

    def run(self, ctx: StageContext) -> list[StageOutput]:
        torch = self.torch
        steps = int(ctx.params.get("steps", 50))
        image = prepare(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))

        def on_step(_pipe: Any, i: int, _t: Any, _kwargs: dict[str, Any]) -> dict[str, Any]:
            ctx.progress(i + 1, steps)
            if ctx.cancelled():
                raise Cancelled(ctx.stage_id)
            return {}

        with torch.inference_mode():
            out = self.pipe(
                image=image,
                generator=torch.Generator(device=self.pipe.device).manual_seed(ctx.seed),
                num_inference_steps=steps,
                guidance_scale=float(ctx.params.get("guidance_scale", 7.0)),
                flash_octree_depth=int(ctx.params.get("octree_depth", 9)),
                use_flash_decoder=True,
                callback_on_step_end=on_step,
            )
        vertices, faces = out.samples[0]
        mesh = trimesh.Trimesh(
            np.asarray(vertices, dtype=np.float32), np.ascontiguousarray(faces), process=True
        )
        # TripoSG's output frame is checked during M0; yaw corrects the facing if needed.
        yaw(mesh, float(ctx.params.get("yaw_degrees", 0)))
        path = mesh_ops.save(mesh, ctx.out_dir / "shape.glb")
        return [StageOutput(path, ArtifactKind.MESH_GLB, preview=True)]
