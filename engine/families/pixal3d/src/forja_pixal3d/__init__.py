"""Pixal3D (MIT), Tencent ARC and Tsinghua University: TRELLIS.2 conditioned on image features
lifted pixel by pixel into its voxel grid, so the shape lines up with the reference.

The upstream source and NAF (Apache-2.0), the feature upsampler it uses, are fetched at
install time and put on the path; weights come from TencentARC/Pixal3D at a pinned revision,
plus Meta's DINOv3 image encoder (gated on Hugging Face, under the DINOv3 License) and NAF's
checkpoint. The pipelines are built here from those local files: upstream's from_pretrained
and NAF loader fetch them at run time, and would also load RMBG-2.0 (non-commercial, remote
code) to remove a background Forja's cut-out no longer has.

Upstream estimates the camera of a single photo with MoGe-2. References for game assets are
mostly drawn without perspective, so the camera here is a narrow field of view instead
(`fov_degrees`, upstream's own suggestion when an estimate distorts), which spares a second
geometry model and its dependencies.
"""

from __future__ import annotations

import gc
import json
import math
import os
from collections.abc import Callable
from pathlib import Path
from typing import Any, ClassVar

import numpy as np
import trimesh
from PIL import Image

from forja_engine.ops import mesh as mesh_ops
from forja_engine.sdk import (
    VIEWS,
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
NAF = Path("naf") / "naf_release.pth"
SHAPE_MODELS = (
    "sparse_structure_flow_model",
    "sparse_structure_decoder",
    "shape_slat_flow_model_512",
    "shape_slat_flow_model_1024",
    "shape_slat_decoder",
)
# Upstream's image encoders: (image size, grid resolution, NAF target size or 0 for none). The
# texture stage's is left out with the texture models: Forja paints the low-poly instead.
ENCODERS = {
    "image_cond_model_ss": (512, 16, 0),
    "image_cond_model_shape_512": (512, 32, 512),
    "image_cond_model_shape_1024": (1024, 64, 512),
}
#: Where each view's camera sits, in degrees about upstream's Z-up world: the front camera
#: looks along +Y, and "left" (the subject's left side) is at +X, as in its example renders.
AZIMUTH = {"front": 0, "left": 90, "back": 180, "right": 270}
MARGIN = 1.1  # upstream's preprocess_image pads the subject's square by a tenth


def bbox(rgba: Image.Image) -> tuple[float, float, float, float]:
    ys, xs = np.nonzero(np.asarray(rgba)[:, :, 3] > 0.8 * 255)
    if len(xs) == 0:
        raise ValueError("pixal3d: a view shows no subject")
    return float(np.min(xs)), float(np.min(ys)), float(np.max(xs)), float(np.max(ys))


def fit(img: Image.Image) -> Image.Image:
    """At most 1024 px on a side, as upstream's preprocessing."""
    rgba = img.convert("RGBA")
    scale = min(1.0, 1024 / max(rgba.size))
    if scale < 1:
        rgba = rgba.resize((int(rgba.width * scale), int(rgba.height * scale)), Image.Resampling.LANCZOS)
    return rgba


def square(rgba: Image.Image, side: int) -> Image.Image:
    """A `side`-pixel square around the subject's box, transparent outside the image."""
    x0, y0, x1, y1 = bbox(rgba)
    cx, cy = (x0 + x1) / 2, (y0 + y1) / 2
    return rgba.crop((cx - side // 2, cy - side // 2, cx + side // 2, cy + side // 2))


def premultiplied(rgba: Image.Image) -> Image.Image:
    a = np.asarray(rgba).astype(np.float32) / 255
    return Image.fromarray((np.clip(a[:, :, :3] * a[:, :, 3:4], 0, 1) * 255).astype(np.uint8))


def prepare(img: Image.Image) -> Image.Image:
    """Upstream's preprocess_image for a cut-out: the subject's padded square on black."""
    rgba = fit(img)
    x0, y0, x1, y1 = bbox(rgba)
    return premultiplied(square(rgba, int(max(x1 - x0, y1 - y0) * MARGIN)))


def prepare_views(images: dict[str, Image.Image]) -> dict[str, Image.Image]:
    """Each view's padded square, all at one scale: the subject's height is the same from every
    side, so it sets the pixels per unit, and the widest view sets the padding."""
    fitted = {v: fit(img) for v, img in images.items()}
    boxes = {v: bbox(img) for v, img in fitted.items()}
    heights = {v: max(y1 - y0, 1.0) for v, (_, y0, _, y1) in boxes.items()}
    k = MARGIN * max(max(x1 - x0, y1 - y0) / heights[v] for v, (x0, y0, x1, y1) in boxes.items())
    return {v: square(fitted[v], int(k * heights[v])) for v in fitted}


def distance(fov: float) -> float:
    """Upstream's distance_from_fov for its defaults: the unit cube's half-width reaches the
    image's edge."""
    return 0.5 / math.tan(fov / 2)


def camera(view: str, d: float) -> list[list[float]]:
    """Camera-to-world matrix of a view (Blender convention: looks along -Z, +Y up)."""
    t = math.radians(AZIMUTH[view] - 90)
    c, s = math.cos(t), math.sin(t)
    return [[-s, 0.0, c, d * c], [c, 0.0, s, d * s], [0.0, 1.0, 0.0, 0.0], [0.0, 0.0, 0.0, 1.0]]


def to_gltf(vertices: np.ndarray) -> np.ndarray:
    """Upstream's export: its GLB axis swap, then the turn inference.py applies to the file."""
    return np.stack([-vertices[:, 0], vertices[:, 1], -vertices[:, 2]], axis=1)


def rotate_y(mesh: trimesh.Trimesh, degrees: float) -> None:
    if degrees:
        mesh.apply_transform(trimesh.transformations.rotation_matrix(math.radians(degrees), [0, 1, 0]))


class _Absent:
    """Stands in for the texture models upstream's run checks for but the shape never reaches."""

    def eval(self) -> _Absent:
        return self

    def to(self, *_: Any, **__: Any) -> _Absent:
        return self

    def cpu(self) -> _Absent:
        return self

    def cuda(self) -> _Absent:
        return self


def shape_only(base: type) -> type:
    """Upstream's pipeline, stopped once the shape is decoded, reporting each step."""

    class ShapeOnly(base):  # type: ignore[valid-type, misc]
        on_step: Callable[[int], None] = staticmethod(lambda step: None)

        def sample_sparse_structure(self, *args: Any, **kwargs: Any) -> Any:
            out = super().sample_sparse_structure(*args, **kwargs)
            self.on_step(1)
            return out

        def sample_shape_slat(self, *args: Any, **kwargs: Any) -> Any:
            out = super().sample_shape_slat(*args, **kwargs)
            self.on_step(2)
            return out

        def get_proj_cond_shape(self, image_cond_model: Any, *args: Any, **kwargs: Any) -> Any:
            if image_cond_model is self.image_cond_model_tex_1024:
                return None
            return super().get_proj_cond_shape(image_cond_model, *args, **kwargs)

        def sample_tex_slat(self, *args: Any, **kwargs: Any) -> Any:
            self.on_step(3)
            return None

        def decode_latent(self, shape_slat: Any, tex_slat: Any, resolution: int) -> Any:
            meshes, _subs = self.decode_shape_slat(shape_slat, resolution)
            for m in meshes:
                m.fill_holes()
            return meshes

    return ShapeOnly


class _Pixal3D(Adapter):
    version = "1"
    config = "pipeline.json"
    pipeline = "Pixal3DImageTo3DPipeline"
    encoder = "DinoV3ProjFeatureExtractor"
    encoder_args: ClassVar[dict[str, Any]] = {}

    def __init__(self) -> None:
        self.pipe: Any = None
        self.torch: Any = None

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CUDA,), vram_bytes=18 * GB)

    def load(self, device: Device, dtype: str = "fp16") -> None:
        # Read when upstream's attention modules are first imported.
        os.environ.setdefault("ATTN_BACKEND", "xformers")
        os.environ.setdefault("PYTORCH_CUDA_ALLOC_CONF", "expandable_segments:True")
        import torch
        from pixal3d import models, pipelines
        from pixal3d.pipelines import samplers
        from pixal3d.trainers.flow_matching.mixins import image_conditioned_proj
        from src.model.naf import NAF as Upsampler  # NAF's code, from its pinned archive

        if self.weights_dir is None:
            raise RuntimeError("pixal3d: no weights folder; run forja models install")
        self.torch = torch
        weights = self.weights_dir
        args = json.loads((weights / self.config).read_text(encoding="utf-8"))["args"]

        def encoder(size: int, grid: int, naf: int) -> Any:
            model = getattr(image_conditioned_proj, self.encoder)(
                str(weights / DINO),
                image_size=size,
                grid_resolution=grid,
                use_naf_upsample=bool(naf),
                naf_target_size=naf or None,
                **self.encoder_args,
            )
            model.eval()
            if naf:
                upsampler = Upsampler()
                state = torch.load(weights / NAF, map_location="cpu", weights_only=True)
                upsampler.load_state_dict(state)
                upsampler.eval()
                upsampler.requires_grad_(False)
                model.naf_model = upsampler
            return model

        def sampler(key: str) -> dict[str, Any]:
            spec = args[key]
            return {key: getattr(samplers, spec["name"])(**spec["args"]), f"{key}_params": spec["params"]}

        loaded = {n: models.from_pretrained(str(weights / args["models"][n])) for n in SHAPE_MODELS}
        loaded["tex_slat_flow_model_1024"] = _Absent()
        low_vram = self.offload
        self.pipe = shape_only(getattr(pipelines, self.pipeline))(
            models=loaded,
            **sampler("sparse_structure_sampler"),
            **sampler("shape_slat_sampler"),
            **sampler("tex_slat_sampler"),
            shape_slat_normalization=args["shape_slat_normalization"],
            tex_slat_normalization=args["tex_slat_normalization"],
            **{name: encoder(*spec) for name, spec in ENCODERS.items()},
            image_cond_model_tex_1024=_Absent(),
            rembg_model=None,
            low_vram=low_vram,
            default_pipeline_type="1536_cascade",
        )
        if low_vram:
            # Each model visits the GPU only for its own step: upstream's --low_vram.
            self.pipe._device = torch.device("cuda")
        else:
            self.pipe.cuda()
            for name in ENCODERS:
                getattr(self.pipe, name).cuda()

    def unload(self) -> None:
        self.pipe = None
        gc.collect()
        if self.torch is not None:
            self.torch.cuda.empty_cache()

    def _low_vram(self) -> None:
        """Set by the VRAM manager in fit-first mode when the model exceeds the budget."""
        if self.pipe.low_vram:
            return
        for model in self.pipe.models.values():
            model.cpu()
        for name in ENCODERS:
            getattr(self.pipe, name).cpu()
        self.pipe.low_vram = True
        self.torch.cuda.empty_cache()

    def _generate(self, ctx: StageContext, run: Callable[[dict[str, Any]], Any]) -> list[StageOutput]:
        if ctx.params.get("offload"):
            self._low_vram()
        resolution = int(ctx.params.get("resolution", 1536))
        if self.pipe.low_vram:
            resolution = min(resolution, 1024)  # upstream's low-VRAM default
        if resolution not in (1024, 1536):
            raise ValueError(f"pixal3d: resolution must be 1024 or 1536, not {resolution}")
        total = 5

        def on_step(step: int) -> None:
            ctx.progress(step, total)
            ctx.check_cancel()

        self.pipe.on_step = on_step
        steps = int(ctx.params.get("steps", 12))
        with self.torch.no_grad():
            meshes = run(
                {
                    "seed": ctx.seed,
                    "sparse_structure_sampler_params": {"steps": steps},
                    "shape_slat_sampler_params": {"steps": steps},
                    "pipeline_type": f"{resolution}_cascade",
                    "max_num_tokens": int(ctx.params.get("max_tokens", 49152)),
                }
            )
        on_step(4)

        out = meshes[0]
        max_faces = int(ctx.params.get("max_faces", 1_000_000))
        if len(out.faces) > max_faces:
            out.simplify(max_faces)
        vertices = to_gltf(out.vertices.cpu().numpy().astype(np.float64))
        mesh = trimesh.Trimesh(vertices, out.faces.cpu().numpy(), process=True)
        rotate_y(mesh, float(ctx.params.get("yaw_degrees", 0)))
        path = mesh_ops.save(mesh, ctx.out_dir / "shape.glb")
        ctx.progress(total, total)
        return [StageOutput(path, ArtifactKind.MESH_GLB, preview=True)]


class Pixal3D(_Pixal3D):
    """From the front view alone."""

    def run(self, ctx: StageContext) -> list[StageOutput]:
        image = prepare(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))
        fov = math.radians(float(ctx.params.get("fov_degrees", 11.5)))
        cameras = {"camera_angle_x": fov, "distance": distance(fov), "mesh_scale": 1.0}
        return self._generate(
            ctx, lambda kw: self.pipe.run(image, camera_params=cameras, preprocess_image=False, **kw)
        )


class Pixal3DMV(_Pixal3D):
    """From the front plus any of the left, back and right views, each placed at its side."""

    config = "pipeline_mv.json"
    pipeline = "Pixal3DMVImageTo3DPipeline"
    encoder = "DinoV3ProjMultiViewFeatureExtractor"
    encoder_args: ClassVar[dict[str, Any]] = {"multiview_fusion": "average"}

    def run(self, ctx: StageContext) -> list[StageOutput]:
        torch = self.torch
        given = ctx.views()
        if "front" not in given:
            raise ValueError("pixal3d-mv needs a front view")
        # The front comes first: upstream's main view, the frame the shape is made in.
        order = [v for v in VIEWS if v in given]
        squares = prepare_views({v: Image.open(given[v].path) for v in order})
        fov = math.radians(float(ctx.params.get("fov_degrees", 11.5)))
        d = distance(fov)

        def tensor(img: Image.Image, size: int) -> Any:
            rgba = np.asarray(img.resize((size, size), Image.Resampling.LANCZOS)).astype(np.float32) / 255
            return torch.from_numpy(
                np.ascontiguousarray((rgba[:, :, :3] * rgba[:, :, 3:4]).transpose(2, 0, 1))
            )

        n = len(order)
        views = {
            "images": {s: torch.stack([tensor(squares[v], s) for v in order])[None] for s in (512, 1024)},
            "camera_angle_x": torch.full((1, n), fov, dtype=torch.float32),
            "camera_distance": torch.full((1, n), d, dtype=torch.float32),
            "transform_matrix": torch.tensor([[camera(v, d) for v in order]], dtype=torch.float32),
            "mesh_scale": 1.0,
            "view_names": order,
        }
        return self._generate(ctx, lambda kw: self.pipe.run_mv(views, **kw))
