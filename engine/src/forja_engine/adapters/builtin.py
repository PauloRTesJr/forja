"""Built-in CPU stages. They run in the supervisor's process pool, never in a model worker."""

from __future__ import annotations

import json
import logging
import shutil
import subprocess

import numpy as np
import trimesh
from PIL import Image
from scipy.spatial import cKDTree

from forja_engine import blender
from forja_engine.ops import bake, blender_mesh
from forja_engine.ops import image as img_ops
from forja_engine.ops import match as match_ops
from forja_engine.ops import mesh as mesh_ops
from forja_engine.ops import skin as skin_ops
from forja_engine.ops import views as views_ops
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

CPU = (Device.CPU,)
log = logging.getLogger(__name__)


class Framing(Adapter):
    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.FRAMING,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        src = img_ops.normalize(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))
        out = ctx.out_dir / "framed.png"
        img_ops.frame(src, int(ctx.params.get("size", 512))).save(out, optimize=False)
        return [StageOutput(out, ArtifactKind.IMAGE_PNG)]


class Cleanup(Adapter):
    # 2: floaters are dropped by face mask instead of split and joined (keeps textures).
    version = "2"

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.MESH_CLEANUP,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        mesh = mesh_ops.cleanup(mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path))
        return [StageOutput(mesh_ops.save(mesh, ctx.out_dir / "clean.glb"), ArtifactKind.MESH_GLB)]


class Decimate(Adapter):
    version = "2"

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


class Remesh(Adapter):
    """Quad remesh by Blender's QuadriFlow: an alternative to decimation with even, quad-based
    topology, at most `target_tris` triangles (the budget of the decimation it replaces).

    Not deterministic: QuadriFlow in Blender 5.2 makes a different mesh on each run for the same
    seed, even on one thread. The stage cache keeps the first result, so a job re-run reuses it.
    When QuadriFlow fails on every try, the stage decimates instead and says so in its output's note.
    """

    blender = True
    # 2: sliver edges merged before QuadriFlow, and coarser voxel rebuilds tried before failing.
    version = "2"

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.MESH_REMESH,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        target = int(ctx.params.get("target_tris", 5000))
        ctx.progress(1, 2)
        ctx.check_cancel()
        note = ""
        try:
            low = blender_mesh.quad_remesh(
                mesh, target, seed=ctx.seed, preserve_sharp=bool(ctx.params.get("preserve_sharp", False))
            )
        except blender.BlenderError as e:
            if blender.problem() is not None:
                raise  # Blender is gone, which is no fault of this shape's
            # Some shapes defeat QuadriFlow on every try. The job still needs a low-poly, and the
            # decimation this stage replaces makes one; the note tells the person it is not quads.
            log.warning("quad remesh fell back to decimation: %s", e)
            ctx.check_cancel()
            low = mesh_ops.decimate(mesh, target)
            note = (
                "QuadriFlow could not remesh this shape, so it was simplified the usual way instead: "
                "triangles, not quads"
            )
        ctx.check_cancel()
        ctx.progress(2, 2)
        out = mesh_ops.save(low, ctx.out_dir / "remeshed.glb")
        return [StageOutput(out, ArtifactKind.MESH_GLB, note=note)]


class SmartUnwrap(Adapter):
    """UVs by Blender's Smart UV Project: fewer, straighter islands than xatlas on hard surfaces."""

    blender = True

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.MESH_UV_SMART,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        out = blender_mesh.smart_uv(
            mesh,
            angle_limit=float(ctx.params.get("angle_limit", 66.0)),
            margin=float(ctx.params.get("margin", 0.004)),
        )
        return [StageOutput(mesh_ops.save(out, ctx.out_dir / "uv.glb"), ArtifactKind.MESH_GLB)]


def _factor(material: object, name: str, default: float) -> float:
    value = getattr(material, name, None)
    return default if value is None else float(value)


class TextureMatch(Adapter):
    """Pulls a painted base color back toward the reference it was painted from.

    Inputs: the painted mesh and the framed reference. Geometry, UVs, roughness and metallic
    pass through, so the bake stage after it works unchanged. See ops/match.py for the method.
    """

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.TEXTURE_MATCH,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        mesh = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB).path)
        material = getattr(mesh.visual, "material", None)
        base = getattr(material, "baseColorTexture", None)
        if base is None:
            raise ValueError("texture.match needs a painted mesh with a base color texture")
        ctx.progress(1, 3)
        result = match_ops.match(
            mesh,
            base,
            Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path),
            color=float(ctx.params.get("color", 1.0)),
            lightness=float(ctx.params.get("lightness", 0.0)),
            reproject=float(ctx.params.get("reproject", 1.0)),
            min_iou=float(ctx.params.get("min_iou", 0.8)),
            align=bool(ctx.params.get("align", True)),
            yaw=float(ctx.params.get("yaw_degrees", 0.0)),
            seed=ctx.seed,
        )
        ctx.progress(2, 3)
        ctx.check_cancel()
        if result.iou < float(ctx.params.get("min_iou", 0.8)):
            log.warning(
                "texture.match %s: the shape covers the reference silhouette at IoU %.2f only; "
                "matched colors but skipped reprojection",
                ctx.stage_id,
                result.iou,
            )
        out = mesh_ops.textured(
            mesh,
            result.texture,
            roughness=_factor(material, "roughnessFactor", 0.8),
            metallic=_factor(material, "metallicFactor", 0.0),
        )
        path = mesh_ops.save(out, ctx.out_dir / "matched.glb")
        ctx.progress(3, 3)
        return [StageOutput(path, ArtifactKind.MESH_GLB)]


class Bake(Adapter):
    """Normal and AO maps from the high-poly onto the painted low-poly (docs/decisions.md, 6).

    Inputs: the textured low-poly first, the high-poly second. Roughness and metallic stay
    constant factors; the ORM map's G and B are white so those factors pass through.

    When the low-poly was not painted (a model did shape and texture in one pass, so the paint
    stage was skipped), it is unwrapped here and the high-poly's colors are baked as its base
    color (docs/decisions.md, 16).
    """

    # 3: MikkTSpace tangents, measured cage, 4 samples per texel, padded gutters, smoother AO.
    version = "3"

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.BAKE,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        # The maps are baked against the low-poly's normals, so the GLB must carry those same ones.
        low = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB, 0).path, keep_normals=True)
        high = mesh_ops.load(ctx.input(ArtifactKind.MESH_GLB, 1).path)
        if getattr(low.visual, "uv", None) is None:
            low = mesh_ops.unwrap(low)
        material = getattr(low.visual, "material", None)
        base = getattr(material, "baseColorTexture", None)
        size = int(ctx.params.get("resolution") or (base.size[0] if base is not None else 1024))
        cage = ctx.params.get("cage")
        ctx.progress(1, 3)
        result = bake.bake(
            low,
            high,
            size,
            cage=None if cage is None else float(cage),
            ao_samples=int(ctx.params.get("ao_samples", 32)),
            ao_radius=float(ctx.params.get("ao_radius", 0.15)),
            seed=ctx.seed,
            color=base is None,
            samples=int(ctx.params.get("samples", 4)),
        )
        if base is None:
            base = result.color
        ctx.progress(2, 3)
        ctx.check_cancel()
        if result.hit_ratio < 0.95:
            log.warning(
                "bake %s: rays from a cage at %.1f%% of the size reached the high-poly for only %.0f%% of "
                "texels; raise bake.cage",
                ctx.stage_id,
                result.cage * 100,
                result.hit_ratio * 100,
            )
        low = result.mesh
        out = low.copy()
        out.visual = trimesh.visual.TextureVisuals(
            uv=mesh_ops.uvs_of(low),
            material=trimesh.visual.material.PBRMaterial(
                baseColorTexture=base,
                normalTexture=result.normal,
                occlusionTexture=result.orm,
                metallicRoughnessTexture=result.orm,
                roughnessFactor=_factor(material, "roughnessFactor", 0.8),
                metallicFactor=_factor(material, "metallicFactor", 0.0),
            ),
        )
        out.vertex_normals = low.vertex_normals  # type: ignore[method-assign]  # trimesh property setter
        out.metadata[mesh_ops.TANGENTS] = result.tangents
        path = mesh_ops.save(out, ctx.out_dir / "baked.glb")
        ctx.progress(3, 3)
        return [StageOutput(path, ArtifactKind.MESH_GLB)]


class Lods(Adapter):
    """LOD1..n by decimating LOD0's geometry and transferring UVs from LOD0's surface.

    All levels share LOD0's material, so LODs cost no extra texture memory. A rigged LOD0's
    skin goes to every level the same way as its UVs, so all levels bend with one skeleton.
    """

    # 3 came twice, on two branches: keeping a rigged mesh's skin, and MikkTSpace tangents with
    # LOD0 keeping the normals its maps were baked against. 4 is both.
    version = "4"

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.MESH_LODS,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        src = ctx.input(ArtifactKind.MESH_GLB).path
        lod0 = skin_ops.attach(mesh_ops.load(src, keep_normals=True), src)
        weights = lod0.metadata.get(skin_ops.SKIN)
        skeleton = lod0.metadata.get(skin_ops.SKELETON)
        name = str(ctx.params.get("name", "asset"))
        normal_mapped = getattr(getattr(lod0.visual, "material", None), "normalTexture", None) is not None
        if normal_mapped:
            # Same mesh, same normals, same algorithm: the tangents the maps were baked with.
            lod0.metadata[mesh_ops.TANGENTS] = mesh_ops.vertex_tangents(lod0, mesh_ops.uvs_of(lod0))
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
            if normal_mapped:
                lod.metadata[mesh_ops.TANGENTS] = mesh_ops.vertex_tangents(lod, uv)
            if weights is not None and skeleton is not None:
                lod.metadata[skin_ops.SKIN] = skin_ops.interpolate(
                    weights, len(skeleton), lod0.faces[tri_ids], bary
                )
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


class ViewsDetect(Adapter):
    """Says whether the cut-out is a sheet of several views of one subject (ops/views.py)."""

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.VIEWS_DETECT,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        cutout = img_ops.normalize(Image.open(ctx.input(ArtifactKind.IMAGE_PNG).path))
        out = ctx.out_dir / "sheet.json"
        out.write_text(json.dumps(views_ops.detect(cutout), sort_keys=True), encoding="utf-8")
        return [StageOutput(out, ArtifactKind.JSON)]


class ViewsSplit(Adapter):
    """Cuts the chosen views out of a sheet: inputs are the original and its cut-out.

    `views` lists {"view", "box"} in the cut-out's pixels; outputs come in the same order.
    """

    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.VIEWS_SPLIT,), devices=CPU)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        wanted = ctx.params.get("views") or []
        names = [w.get("view") for w in wanted]
        if "front" not in names or len(set(names)) != len(names) or not set(names) <= set(VIEWS):
            raise ValueError(f"views.split needs a front and each view at most once, got {names}")
        original = img_ops.normalize(Image.open(ctx.input(ArtifactKind.IMAGE_PNG, 0).path))
        cutout = img_ops.normalize(Image.open(ctx.input(ArtifactKind.IMAGE_PNG, 1).path))
        boxes = [(int(x0), int(y0), int(x1), int(y1)) for x0, y0, x1, y1 in (w["box"] for w in wanted)]
        outputs = []
        for name, img in zip(names, views_ops.split(original, cutout, boxes), strict=True):
            path = ctx.out_dir / f"{name}.png"
            img.save(path, optimize=False)
            outputs.append(StageOutput(path, ArtifactKind.IMAGE_PNG))
        return outputs


BUILTINS: dict[StageType, type[Adapter]] = {
    StageType.FRAMING: Framing,
    StageType.MESH_CLEANUP: Cleanup,
    StageType.MESH_DECIMATE: Decimate,
    StageType.MESH_REMESH: Remesh,
    StageType.MESH_UV: Unwrap,
    StageType.MESH_UV_SMART: SmartUnwrap,
    StageType.TEXTURE_MATCH: TextureMatch,
    StageType.BAKE: Bake,
    StageType.MESH_LODS: Lods,
    StageType.EXPORT: Export,
    StageType.VIEWS_DETECT: ViewsDetect,
    StageType.VIEWS_SPLIT: ViewsSplit,
}


def offered() -> tuple[dict[str, str], dict[str, str]]:
    """The built-in stages this engine runs, by adapter version, and why it runs no others.

    A Blender-backed stage is offered only where Blender is found, and its version names the
    Blender too: another Blender makes other quads and UVs, so it must key the cache apart.
    """
    found: blender.Blender | None = None
    problem: str | None = None
    stages: dict[str, str] = {}
    missing: dict[str, str] = {}
    for task, cls in BUILTINS.items():
        if not getattr(cls, "blender", False):
            stages[str(task)] = cls.version
            continue
        if found is None and problem is None:
            try:
                found = blender.find()
            except blender.BlenderError as e:
                problem = str(e)
        if found is None:
            missing[str(task)] = str(problem)
        else:
            stages[str(task)] = f"{cls.version}+blender-{found.version}"
    return stages, missing
