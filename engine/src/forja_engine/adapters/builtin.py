"""Built-in CPU stages. They run in the supervisor's process pool, never in a model worker."""

from __future__ import annotations

import shutil
import subprocess

import numpy as np
import trimesh
from PIL import Image
from scipy.spatial import cKDTree

from forja_engine.ops import image as img_ops
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

CPU = (Device.CPU,)


class Framing(Adapter):
    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.FRAMING,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        src = img_ops.normalize(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))
        out = ctx.out_dir / "framed.png"
        img_ops.frame(src, int(ctx.params.get("size", 512))).save(out, optimize=False)
        return [StageOutput(out, ArtifactKind.IMAGE_PNG)]


class Cleanup(Adapter):
    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.MESH_CLEANUP,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        mesh = mesh_ops.cleanup(mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path))
        return [StageOutput(mesh_ops.save(mesh, ctx.out_dir / "clean.glb"), ArtifactKind.MESH_GLB)]


class Decimate(Adapter):
    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.MESH_DECIMATE,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        low = mesh_ops.decimate(mesh, int(ctx.params.get("target_tris", 5000)))
        return [StageOutput(mesh_ops.save(low, ctx.out_dir / "lowpoly.glb"), ArtifactKind.MESH_GLB)]


class Unwrap(Adapter):
    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.MESH_UV,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        mesh = mesh_ops.unwrap(mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path))
        return [StageOutput(mesh_ops.save(mesh, ctx.out_dir / "uv.glb"), ArtifactKind.MESH_GLB)]


class Lods(Adapter):
    """LOD1..n by decimating LOD0's geometry and transferring UVs from LOD0's surface.

    All levels share LOD0's material, so LODs cost no extra texture memory.
    """

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.MESH_LODS,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        lod0 = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        name = str(ctx.params.get("name", "asset"))
        scene = trimesh.Scene()
        scene.add_geometry(lod0, node_name=f"{name}_LOD0", geom_name=f"{name}_LOD0")
        ratios = ctx.params.get("ratios", [0.5, 0.25])
        centroids = cKDTree(lod0.triangles_center)
        for level, ratio in enumerate(ratios, start=1):
            ctx.check_cancel()
            lod = mesh_ops.decimate(lod0, max(4, int(len(lod0.faces) * float(ratio))))
            _, tri_ids = centroids.query(lod.vertices)
            bary = np.clip(
                trimesh.triangles.points_to_barycentric(lod0.triangles[tri_ids], lod.vertices), 0, 1
            )
            bary /= np.maximum(bary.sum(axis=1, keepdims=True), 1e-9)
            uv0 = mesh_ops.uvs_of(lod0)[lod0.faces[tri_ids]]
            uv = np.einsum("ij,ijk->ik", bary, uv0)
            lod.visual = trimesh.visual.TextureVisuals(uv=uv, material=getattr(lod0.visual, "material", None))
            scene.add_geometry(lod, node_name=f"{name}_LOD{level}", geom_name=f"{name}_LOD{level}")
            ctx.progress(level, len(ratios))
        return [StageOutput(mesh_ops.save(scene, ctx.out_dir / "lods.glb"), ArtifactKind.MESH_GLB)]


class Export(Adapter):
    """Final GLB. Meshopt-compresses with gltfpack when it is on PATH."""

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.EXPORT,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        fmt = ctx.params.get("format", "glb")
        if fmt != "glb":
            raise ValueError(f"format {fmt!r} is not supported yet; GLB only until M6")
        src = ctx.input(ArtifactKind.MESH_GLB).path
        out = ctx.out_dir / "asset.glb"
        gltfpack = shutil.which("gltfpack")
        if ctx.params.get("compression") == "meshopt" and gltfpack:
            subprocess.run([gltfpack, "-i", str(src), "-o", str(out), "-cc"], check=True, capture_output=True)
        else:
            shutil.copyfile(src, out)
        return [StageOutput(out, ArtifactKind.MESH_GLB)]


BUILTINS: dict[StageType, type[Adapter]] = {
    StageType.FRAMING: Framing,
    StageType.MESH_CLEANUP: Cleanup,
    StageType.MESH_DECIMATE: Decimate,
    StageType.MESH_UV: Unwrap,
    StageType.MESH_LODS: Lods,
    StageType.EXPORT: Export,
}
