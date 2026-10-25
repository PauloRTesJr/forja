"""Example model family: extrudes the subject's silhouette into a slab.

It stands in for a real model so the template runs anywhere, on the CPU, in seconds.
To adapt it, replace `Extruder` with the upstream pipeline and keep the glue around it:
reading inputs and params, reporting progress, checking for cancellation, writing a GLB.
"""

from __future__ import annotations

from collections.abc import Callable
from pathlib import Path

import numpy as np
from forja_sdk import Adapter, ArtifactKind, Capabilities, Device, StageContext, StageOutput, StageType
from forja_sdk.artifacts import write_glb
from numpy.typing import NDArray
from PIL import Image

Mesh = tuple[NDArray[np.float64], NDArray[np.int64]]


def silhouette(img: Image.Image, resolution: int) -> NDArray[np.bool_]:
    """The subject's mask on a square grid, from the cut-out's alpha channel."""
    alpha = img.convert("RGBA").getchannel("A").resize((resolution, resolution), Image.Resampling.BILINEAR)
    return np.asarray(alpha) > 127


class Extruder:
    """The "upstream model". A real family builds its pipeline here: it imports the upstream
    package (fetched at install time from the pinned commit in family.yaml) and loads the
    weights install put in `weights_dir`, on `device`."""

    def __init__(self, weights_dir: Path | None, device: Device) -> None:
        self.device = device

    def __call__(self, mask: NDArray[np.bool_], depth: float, step: Callable[[], None]) -> Mesh:
        rows, cols = mask.shape
        width = cols + 1
        grid_r, grid_c = np.mgrid[0 : rows + 1, 0 : cols + 1]
        xy = np.stack([grid_c / cols - 0.5, 0.5 - grid_r / rows], axis=-1).reshape(-1, 2)
        ones = np.ones((len(xy), 1))
        verts = np.concatenate([np.hstack([xy, ones * depth / 2]), np.hstack([xy, ones * -depth / 2])])
        back = len(xy)
        r, c = np.nonzero(mask)
        c00, c01, c10, c11 = r * width + c, r * width + c + 1, (r + 1) * width + c, (r + 1) * width + c + 1
        faces = [
            np.stack([c00, c10, c11], 1),
            np.stack([c00, c11, c01], 1),
            np.stack([c00 + back, c11 + back, c10 + back], 1),
            np.stack([c00 + back, c01 + back, c11 + back], 1),
        ]
        step()  # a real pipeline steps once per diffusion step, so cancel can land in between
        padded = np.pad(mask, 1)
        # A wall wherever a filled cell meets an empty one; each edge p -> q runs
        # counter-clockwise seen from the front, so the walls face outwards.
        for (dr, dc), (p, q) in (
            ((1, 0), (c10, c11)),
            ((0, 1), (c11, c01)),
            ((-1, 0), (c01, c00)),
            ((0, -1), (c00, c10)),
        ):
            edge = ~padded[r + 1 + dr, c + 1 + dc]
            pe, qe = p[edge], q[edge]
            faces += [np.stack([qe, pe, pe + back], 1), np.stack([qe, pe + back, qe + back], 1)]
        tris = np.concatenate(faces)
        used = np.unique(tris)
        remap = np.zeros(len(verts), dtype=np.int64)
        remap[used] = np.arange(len(used))
        return verts[used], remap[tris]


class ExampleExtrude(Adapter):
    #: Bump when the output changes for the same inputs; the manifest's adapter_version mirrors it.
    version = "1"

    def __init__(self) -> None:
        self.pipeline: Callable[[NDArray[np.bool_], float, Callable[[], None]], Mesh] | None = None

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CPU,))

    def load(self, device: Device, dtype: str = "fp16") -> None:
        # A real adapter loads its weights onto the device here, once; the worker keeps the
        # model warm between jobs. With self.offload set, take the low-memory path.
        self.pipeline = Extruder(self.weights_dir, device)

    def unload(self) -> None:
        self.pipeline = None  # a torch adapter also calls torch.cuda.empty_cache()

    def run(self, ctx: StageContext) -> list[StageOutput]:
        if self.pipeline is None:
            raise RuntimeError("example-extrude: load() was not called")
        resolution = int(ctx.params.get("resolution", 64))
        depth = float(ctx.params.get("depth", 0.2))
        # ctx.seed would seed the pipeline's generator; this one has no randomness.
        mask = silhouette(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path), resolution)
        if not mask.any():
            raise ValueError("the cut-out is empty: nothing to extrude")
        steps, done = 3, 1

        def step() -> None:
            nonlocal done
            done += 1
            ctx.progress(done, steps)
            ctx.check_cancel()

        ctx.progress(done, steps)
        ctx.check_cancel()
        verts, faces = self.pipeline(mask, depth, step)
        out = write_glb(ctx.out_dir / "shape.glb", verts.tolist(), faces.tolist())
        ctx.progress(steps, steps)
        # preview: the app shows the untextured mesh as soon as this stage ends.
        return [StageOutput(out, ArtifactKind.MESH_GLB, preview=True)]
