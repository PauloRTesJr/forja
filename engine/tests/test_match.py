import numpy as np
import trimesh
from PIL import Image

from forja_engine.adapters.builtin import TextureMatch
from forja_engine.ops import match
from forja_engine.ops import mesh as m
from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType


def plane(n: int = 8) -> trimesh.Trimesh:
    """A unit grid in XY facing +Z, with UVs = XY, so image right = +X and image up = +Y."""
    g = np.linspace(0, 1, n + 1)
    xs, ys = np.meshgrid(g, g)
    verts = np.stack([xs.ravel(), ys.ravel(), np.zeros(xs.size)], axis=1)
    idx = np.arange(verts.shape[0]).reshape(n + 1, n + 1)
    a, b, c, d = idx[:-1, :-1].ravel(), idx[:-1, 1:].ravel(), idx[1:, :-1].ravel(), idx[1:, 1:].ravel()
    faces = np.concatenate([np.stack([a, b, d], 1), np.stack([a, d, c], 1)])
    mesh = trimesh.Trimesh(verts, faces, process=False)
    mesh.visual = trimesh.visual.TextureVisuals(uv=verts[:, :2])
    return mesh


def gradient(size: int) -> Image.Image:
    """A smooth, colorful texture: painters never output one flat color."""
    u, v = np.meshgrid(np.linspace(0, 1, size), np.linspace(0, 1, size))
    rgb = np.stack([0.3 + 0.4 * u, 0.5 - 0.2 * v, 0.4 + 0.2 * u * v], -1)
    return Image.fromarray((rgb * 255).astype(np.uint8), "RGB")


def checker(size: int, cells: int) -> np.ndarray:
    yy, xx = np.mgrid[0:size, 0:size] * cells // size
    return np.where((xx + yy) % 2 == 0, 0.9, 0.15)


def reference(subject: np.ndarray, size: int = 256, at: tuple[int, int] = (28, 28)) -> Image.Image:
    """The subject (gray levels) on a transparent canvas, like the framing stage's output."""
    out = np.zeros((size, size, 4))
    h, w = subject.shape
    out[at[1] : at[1] + h, at[0] : at[0] + w, :3] = subject[..., None]
    out[at[1] : at[1] + h, at[0] : at[0] + w, 3] = 1
    return Image.fromarray((out * 255).astype(np.uint8), "RGBA")


def test_lab_round_trip():
    rgb = np.random.default_rng(0).uniform(0, 1, (1000, 3))
    assert np.abs(match.lab_to_srgb(match.srgb_to_lab(rgb)) - rgb).max() < 1e-6


def test_color_transfer_corrects_one_material_and_leaves_another():
    rng = np.random.default_rng(0)
    robe, robe_ref, fur = np.array([40.0, -24, 4]), np.array([40.0, -12, -2]), np.array([70.0, 6, 20])
    rendered = np.concatenate([rng.normal(robe, 1.0, (20_000, 3)), rng.normal(fur, 1.0, (20_000, 3))])
    target = rendered.copy()
    target[:20_000] += robe_ref - robe  # the painter made the robe too green and saturated
    back = np.array([40.0, -32, 5])  # the back drifted further than the front
    out = match.color_transfer(rendered, target, np.stack([robe, fur, back]), clusters=2)
    assert np.abs(out[0] - robe_ref).max() < 1.0
    assert np.abs(out[1] - fur).max() < 1.0
    # A chroma scale, not an offset: the more saturated back comes out about as far from gray.
    assert np.hypot(*out[2, 1:]) < 0.6 * np.hypot(*back[1:])


def test_color_transfer_survives_a_single_flat_color():
    flat = np.tile([[50.0, 0.0, 0.0]], (1000, 1))
    out = match.color_transfer(flat, flat + np.array([5.0, 0, 0]), flat[:3], lightness=1.0)
    assert np.isfinite(out).all() and np.allclose(out[:, 0], 55)


def test_match_projects_the_reference_onto_the_surface_facing_it():
    pattern = checker(200, 8)
    r = match.match(plane(), gradient(128), reference(pattern), align=False)
    assert r.iou > 0.97 and abs(r.view.yaw) < 3 and abs(r.view.pitch) < 3
    got = np.asarray(r.texture.convert("L"), dtype=np.float64)[16:112, 16:112]
    want = np.asarray(Image.fromarray((pattern * 255).astype(np.uint8)).resize((128, 128)), dtype=np.float64)
    assert np.corrcoef(got.ravel(), want[16:112, 16:112].ravel())[0, 1] > 0.9


def test_match_finds_the_side_view_the_reference_was_drawn_from():
    # A box textured by position, drawn from 25 degrees to the side: its own render is the reference.
    box = trimesh.creation.box((1.0, 1.6, 0.5))
    box = m.unwrap(box)
    tex = gradient(128)
    seen = match.match(box, tex, _render(box, tex, yaw=25.0), reproject=0.0)
    assert abs(seen.view.yaw - 25.0) < 4 and seen.iou > 0.95


def test_match_only_recolors_when_the_shape_does_not_fit():
    bar = reference(checker(200, 8)[:, :40], at=(108, 28))  # a thin bar, nothing like a square
    r = match.match(plane(), gradient(128), bar)
    assert r.iou < 0.8 and r.reprojected == 0.0


def test_stage_keeps_geometry_uvs_and_material_factors(tmp_path):
    painted = m.textured(plane(), gradient(128), roughness=0.5, metallic=0.1)
    glb = m.save(painted, tmp_path / "painted.glb")
    ref = tmp_path / "ref.png"
    reference(checker(200, 8)).save(ref)
    outputs = []
    for i in range(2):
        out = tmp_path / f"out{i}"
        out.mkdir()
        ctx = StageContext(
            stage_id="match",
            task=StageType.TEXTURE_MATCH,
            inputs=[
                StageInput(glb, ArtifactKind.MESH_GLB, "a"),
                StageInput(ref, ArtifactKind.IMAGE_PNG, "b"),
            ],
            params={},
            seed=0,
            out_dir=out,
        )
        outputs.append(TextureMatch().run(ctx)[0].path)
    assert outputs[0].read_bytes() == outputs[1].read_bytes()  # cacheable: same inputs, same bytes
    before, after = m.load(glb), m.load(outputs[0])
    assert np.allclose(before.vertices, after.vertices) and np.allclose(m.uvs_of(before), m.uvs_of(after))
    material = after.visual.material
    assert material.roughnessFactor == 0.5 and material.metallicFactor == 0.1
    assert np.asarray(material.baseColorTexture).tobytes() != np.asarray(gradient(128)).tobytes()


def test_stage_keeps_a_painted_roughness_and_metallic_map(tmp_path):
    maps = Image.new("RGB", (64, 64), (0, 200, 40))
    painted = m.textured(plane(), gradient(128), roughness=1.0, metallic=1.0, metallic_roughness=maps)
    glb = m.save(painted, tmp_path / "painted.glb")
    ref = tmp_path / "ref.png"
    reference(checker(200, 8)).save(ref)
    ctx = StageContext(
        stage_id="match",
        task=StageType.TEXTURE_MATCH,
        inputs=[StageInput(glb, ArtifactKind.MESH_GLB, "a"), StageInput(ref, ArtifactKind.IMAGE_PNG, "b")],
        params={},
        seed=0,
        out_dir=tmp_path,
    )
    kept = m.load(TextureMatch().run(ctx)[0].path).visual.material.metallicRoughnessTexture
    assert kept is not None and kept.convert("RGB").getpixel((10, 10)) == (0, 200, 40)


def _render(mesh: trimesh.Trimesh, tex: Image.Image, yaw: float, size: int = 256) -> Image.Image:
    """The textured mesh from `yaw`, framed like the framing stage: what a reference would show."""
    rot = match.View(yaw, 0.0, match.Projection(1, 0, 0)).rotation()
    v = np.asarray(mesh.vertices) @ rot.T
    lo, hi = v[:, :2].min(0), v[:, :2].max(0)
    s = 0.8 * size / float(max(hi - lo))
    proj = match.Projection(s, size / 2 - s * (lo[0] + hi[0]) / 2, size / 2 + s * (lo[1] + hi[1]) / 2)
    tri, bary, _ = match.zbuffer(v, np.asarray(mesh.faces), proj, size)
    hit = tri >= 0
    uv = m.uvs_of(mesh)[np.asarray(mesh.faces)[tri[hit]]]
    puv = np.einsum("ij,ijk->ik", bary[hit].astype(np.float64), uv)
    arr = np.asarray(tex, dtype=np.float64) / 255
    px = np.clip((puv[:, 0] * arr.shape[1]).astype(int), 0, arr.shape[1] - 1)
    py = np.clip(((1 - puv[:, 1]) * arr.shape[0]).astype(int), 0, arr.shape[0] - 1)
    out = np.zeros((size, size, 4))
    out[hit, :3] = arr[py, px]
    out[hit, 3] = 1
    return Image.fromarray((out * 255).astype(np.uint8), "RGBA")
