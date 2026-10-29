"""TRELLIS.2 (MIT), Microsoft: shape from one image, and PBR painting of a given mesh.

The upstream source is fetched at install time and put on the path; weights come from
microsoft/TRELLIS.2-4B at a pinned revision, plus the two its configuration loads from other
repositories: TRELLIS's sparse structure decoder, and Meta's DINOv3 image encoder (gated on
Hugging Face, under the DINOv3 License).

The pipelines are built here from those local files rather than with upstream's
from_pretrained, which would fetch them from the Hub and also load RMBG-2.0 (non-commercial,
remote code) as a background remover. Forja's cut-out already has transparency, so it is not
needed.

The painter makes base color, roughness and metallic maps; the bake keeps them
(docs/decisions.md, 6).
"""

from __future__ import annotations

import gc
import json
import math
import os
from pathlib import Path
from typing import Any

import numpy as np
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
DINO = "dinov3"  # the manifests' folder for Meta's DINOv3, inside the weights folder
SHAPE_MODELS = (
    "sparse_structure_flow_model",
    "sparse_structure_decoder",
    "shape_slat_flow_model_512",
    "shape_slat_flow_model_1024",
    "shape_slat_decoder",
)
TEXTURE_MODELS = (
    "shape_slat_encoder",
    "tex_slat_decoder",
    "tex_slat_flow_model_512",
    "tex_slat_flow_model_1024",
)
# The sparse structure resolution each pipeline type samples at, as upstream's run.
STRUCTURE = {"512": 32, "1024": 64, "1024_cascade": 32, "1536_cascade": 32}


def prepare(img: Image.Image) -> Image.Image:
    """Upstream's preprocessing of a cut-out: at most 1024 px, a square around the opaque
    subject, color premultiplied onto black. A fully opaque image is used whole."""
    rgba = img.convert("RGBA")
    scale = min(1.0, 1024 / max(rgba.size))
    if scale < 1:
        rgba = rgba.resize((int(rgba.width * scale), int(rgba.height * scale)), Image.Resampling.LANCZOS)
    ys, xs = np.nonzero(np.asarray(rgba)[:, :, 3] > 0.8 * 255)
    if len(xs) == 0:
        raise ValueError("trellis2: the cut-out shows no subject")
    x0, x1, y0, y1 = int(np.min(xs)), int(np.max(xs)), int(np.min(ys)), int(np.max(ys))
    cx, cy = (x0 + x1) / 2, (y0 + y1) / 2
    size = int(max(x1 - x0, y1 - y0))
    square = np.asarray(rgba.crop((cx - size // 2, cy - size // 2, cx + size // 2, cy + size // 2)))
    out = square.astype(np.float32) / 255
    return Image.fromarray((out[:, :, :3] * out[:, :, 3:4] * 255).astype(np.uint8))


def rotate_y(mesh: trimesh.Trimesh, degrees: float) -> None:
    if degrees:
        mesh.apply_transform(trimesh.transformations.rotation_matrix(math.radians(degrees), [0, 1, 0]))


def checkpoint(weights: Path, ref: str) -> str:
    """A model path from upstream's configuration, in the weights folder. A reference into
    another repository ("microsoft/TRELLIS-image-large/ckpts/...") is in that repository's
    folder, named after it."""
    local = weights / ref
    if not Path(f"{local}.json").is_file():
        _org, repo, *rest = ref.split("/")
        local = weights / repo / Path(*rest)
    return str(local)


def build(cls: Any, weights: Path, config: str, names: tuple[str, ...]) -> Any:
    """Upstream's pipeline `cls` from the local weights, with only the models in `names`."""
    from trellis2 import models
    from trellis2.modules import image_feature_extractor
    from trellis2.pipelines import samplers

    args = json.loads((weights / config).read_text(encoding="utf-8"))["args"]

    def sampler(key: str) -> dict[str, Any]:
        if key not in args:
            return {}
        spec = args[key]
        return {key: getattr(samplers, spec["name"])(**spec["args"]), f"{key}_params": spec["params"]}

    encoder = args["image_cond_model"]
    kwargs: dict[str, Any] = {
        "models": {n: models.from_pretrained(checkpoint(weights, args["models"][n])) for n in names},
        **sampler("sparse_structure_sampler"),
        **sampler("shape_slat_sampler"),
        **sampler("tex_slat_sampler"),
        "shape_slat_normalization": args["shape_slat_normalization"],
        "tex_slat_normalization": args["tex_slat_normalization"],
        "image_cond_model": getattr(image_feature_extractor, encoder["name"])(str(weights / DINO)),
        "rembg_model": None,
        # Upstream's default: each model visits the GPU only for its own step.
        "low_vram": True,
    }
    if "default_pipeline_type" in args:
        kwargs["default_pipeline_type"] = args["default_pipeline_type"]
    pipe = cls(**kwargs)
    pipe.cuda()
    return pipe


class _Trellis2(Adapter):
    def __init__(self) -> None:
        self.pipe: Any = None
        self.torch: Any = None

    def _environment(self) -> None:
        # Read when upstream's attention modules are first imported.
        os.environ.setdefault("ATTN_BACKEND", "xformers")
        os.environ.setdefault("PYTORCH_CUDA_ALLOC_CONF", "expandable_segments:True")

    def unload(self) -> None:
        self.pipe = None
        gc.collect()
        if self.torch is not None:
            self.torch.cuda.empty_cache()


class Trellis2Shape(_Trellis2):
    version = "1"

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CUDA,), vram_bytes=24 * GB)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        self._environment()
        import torch
        from trellis2.pipelines import Trellis2ImageTo3DPipeline

        if self.weights_dir is None:
            raise RuntimeError("trellis2: no weights folder; run forja models install")
        self.torch = torch
        self.pipe = build(Trellis2ImageTo3DPipeline, self.weights_dir, "pipeline.json", SHAPE_MODELS)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        torch, pipe = self.torch, self.pipe
        kind = str(ctx.params.get("pipeline_type", "1024_cascade"))
        if kind not in STRUCTURE:
            raise ValueError(f"trellis2: pipeline_type {kind!r} is not one of {', '.join(STRUCTURE)}")
        sampling = {"steps": int(ctx.params.get("steps", 12))}
        image = prepare(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))
        total = 4

        # Upstream's run, without the texture it would also sample: Forja paints the low-poly.
        torch.manual_seed(ctx.seed)
        with torch.no_grad():
            cond_512 = pipe.get_cond([image], 512)
            cond_1024 = pipe.get_cond([image], 1024) if kind != "512" else None
            coords = pipe.sample_sparse_structure(cond_512, STRUCTURE[kind], 1, sampling)
            ctx.progress(1, total)
            ctx.check_cancel()
            if kind in ("512", "1024"):
                res = int(kind)
                cond = cond_512 if kind == "512" else cond_1024
                flow = pipe.models[f"shape_slat_flow_model_{kind}"]
                slat = pipe.sample_shape_slat(cond, flow, coords, sampling)
            else:
                slat, res = pipe.sample_shape_slat_cascade(
                    cond_512,
                    cond_1024,
                    pipe.models["shape_slat_flow_model_512"],
                    pipe.models["shape_slat_flow_model_1024"],
                    512,
                    1536 if kind == "1536_cascade" else 1024,
                    coords,
                    sampling,
                    int(ctx.params.get("max_tokens", 49152)),
                )
            ctx.progress(2, total)
            ctx.check_cancel()
            torch.cuda.empty_cache()
            meshes, _subs = pipe.decode_shape_slat(slat, res)
        ctx.progress(3, total)
        ctx.check_cancel()

        out = meshes[0]
        out.fill_holes()
        # Simplified on the GPU, as upstream's GLB export does, so the CPU stages after it
        # start from a mesh they handle quickly.
        max_faces = int(ctx.params.get("max_faces", 1_000_000))
        if len(out.faces) > max_faces:
            out.simplify(max_faces)
        v = out.vertices.cpu().numpy().astype(np.float64)
        # Upstream's frame is Z up and faces -Y; its GLB export turns it Y up, facing +Z.
        vertices = np.stack([v[:, 0], v[:, 2], -v[:, 1]], axis=1)
        mesh = trimesh.Trimesh(vertices, out.faces.cpu().numpy(), process=True)
        rotate_y(mesh, float(ctx.params.get("yaw_degrees", 0)))
        path = mesh_ops.save(mesh, ctx.out_dir / "shape.glb")
        ctx.progress(4, total)
        return [StageOutput(path, ArtifactKind.MESH_GLB, preview=True)]


class Trellis2Texture(_Trellis2):
    version = "1"

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.TEXTURE,), devices=(Device.CUDA,), vram_bytes=24 * GB)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        self._environment()
        import torch
        from trellis2.pipelines import Trellis2TexturingPipeline

        if self.weights_dir is None:
            raise RuntimeError("trellis2-texture: no weights folder; run forja models install")
        self.torch = torch
        self.pipe = build(
            Trellis2TexturingPipeline, self.weights_dir, "texturing_pipeline.json", TEXTURE_MODELS
        )

    def run(self, ctx: StageContext) -> list[StageOutput]:
        torch, pipe = self.torch, self.pipe
        resolution = int(ctx.params.get("resolution", 1024))
        if resolution not in (512, 1024):
            raise ValueError(f"trellis2-texture: resolution must be 512 or 1024, not {resolution}")
        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        image = prepare(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))
        yaw = float(ctx.params.get("yaw_degrees", 0))
        rotate_y(mesh, yaw)
        total = 4

        # Upstream's preprocess_mesh fits the mesh in its unit cube; the painted mesh comes back
        # there, so the same center and scale take it back to the input's size.
        lo, hi = mesh.vertices.min(axis=0), mesh.vertices.max(axis=0)
        center, scale = (lo + hi) / 2, 0.99999 / float((hi - lo).max())
        fitted = pipe.preprocess_mesh(trimesh.Trimesh(mesh.vertices, mesh.faces, process=False))
        uv = getattr(mesh.visual, "uv", None)
        if uv is not None and len(uv) == len(mesh.vertices):
            # Upstream unwraps only a mesh without UVs: a preset's UV stage keeps its own.
            fitted.visual = trimesh.visual.TextureVisuals(uv=np.asarray(uv))
        ctx.progress(1, total)
        ctx.check_cancel()

        torch.manual_seed(ctx.seed)
        with torch.no_grad():
            cond = pipe.get_cond([image], resolution)
            shape_slat = pipe.encode_shape_slat(fitted, resolution)
            ctx.progress(2, total)
            ctx.check_cancel()
            tex_slat = pipe.sample_tex_slat(
                cond,
                pipe.models[f"tex_slat_flow_model_{resolution}"],
                shape_slat,
                {"steps": int(ctx.params.get("steps", 12))},
            )
            voxels = pipe.decode_tex_slat(tex_slat)
            ctx.progress(3, total)
            ctx.check_cancel()
            painted = pipe.postprocess_mesh(
                fitted, voxels, resolution, int(ctx.params.get("texture_size", 2048))
            )

        painted.vertices = np.asarray(painted.vertices) / scale + center
        rotate_y(painted, -yaw)
        material = painted.visual.material
        out = mesh_ops.textured(
            painted,
            material.baseColorTexture.convert("RGB"),
            roughness=1.0,
            metallic=1.0,
            metallic_roughness=material.metallicRoughnessTexture.convert("RGB"),
        )
        path = mesh_ops.save(out, ctx.out_dir / "textured.glb")
        ctx.progress(4, total)
        return [StageOutput(path, ArtifactKind.MESH_GLB)]
