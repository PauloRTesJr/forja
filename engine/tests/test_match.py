import numpy as np
import pytest
import trimesh
from PIL import Image
from scipy import ndimage

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


def _noise(seed: int, size: int = 256) -> Image.Image:
    """Smooth random color blobs: every side of a box painted with it looks different."""
    rng = np.random.default_rng(seed)
    chans = [ndimage.gaussian_filter(rng.uniform(0, 1, (size, size)), 6, mode="wrap") for _ in range(3)]
    a = np.stack(chans, -1)
    a = (a - a.min((0, 1))) / (a.max((0, 1)) - a.min((0, 1)))
    return Image.fromarray(((0.1 + 0.8 * a) * 255).astype(np.uint8), "RGB")


VIEW_YAW = {"front": 0.0, "back": 180.0, "left": -90.0, "right": 90.0}


class Scene:
    """A box whose four sides differ, a blurred and tinted paint of it, and its true references."""

    def __init__(self) -> None:
        self.box = m.unwrap(trimesh.creation.box((1.0, 1.6, 0.5)))
        true = _noise(1)
        blurred = ndimage.gaussian_filter(np.asarray(true, dtype=np.float64), (7, 7, 0)) * [1.15, 0.85, 1.0]
        self.painted = Image.fromarray(np.clip(blurred, 0, 255).astype(np.uint8), "RGB")
        self.refs = {v: _render(self.box, true, yaw) for v, yaw in VIEW_YAW.items()}
        self.sides = {v: self.refs[v] for v in ("back", "left", "right")}

    def run(self, sides=None):
        return match.match(self.box, self.painted, self.refs["front"], align=False, sides=sides)

    def error(self, result: match.MatchResult, view: str) -> float:
        """Mean color distance from the reference, seen from that view's camera."""
        got = np.asarray(_render(self.box, result.texture, VIEW_YAW[view]), dtype=np.float64)[..., :3]
        ref = np.asarray(self.refs[view], dtype=np.float64)[..., :3]
        inner = ndimage.binary_erosion(np.asarray(self.refs[view])[..., 3] > 0, iterations=6)
        return float(np.abs(got - ref)[inner].mean() / 255)


@pytest.fixture(scope="module")
def scene():
    s = Scene()
    swapped = {"back": s.sides["back"], "left": s.sides["right"], "right": s.sides["left"]}
    return s, {"front": s.run(), "sides": s.run(s.sides), "swapped": s.run(swapped)}


def test_match_without_side_views_is_what_it_was(scene):
    s, runs = scene
    again = s.run({})
    assert again.sides == {} and runs["front"].sides == {}
    assert again.texture.tobytes() == runs["front"].texture.tobytes()
    assert runs["front"].reprojected > 0.1 and runs["front"].iou > 0.95


def test_side_views_pull_the_back_and_sides_toward_their_references(scene):
    s, runs = scene
    for view in ("back", "left", "right"):
        before, after = s.error(runs["front"], view), s.error(runs["sides"], view)
        assert after < 0.5 * before, (view, before, after)
        assert after < 0.03, (view, after)
    assert set(runs["sides"].sides) == {"back", "left", "right"}
    for iou, share, as_labelled in runs["sides"].sides.values():
        assert iou > 0.95 and share > 0.05 and as_labelled
    # The front wins where it sees a texel: its own view comes out as it does without sides.
    assert s.error(runs["sides"], "front") <= s.error(runs["front"], "front") + 1e-3
    front_view = np.asarray(_render(s.box, runs["sides"].texture, 0.0), dtype=np.int64)
    alone = np.asarray(_render(s.box, runs["front"].texture, 0.0), dtype=np.int64)
    inner = ndimage.binary_erosion(alone[..., 3] > 0, iterations=3)  # the rim is shared with the sides
    assert np.abs(front_view - alone).max(-1)[inner].max() <= 1


def test_swapped_side_labels_are_used_the_way_the_views_fit(scene):
    s, runs = scene
    for view in ("back", "left", "right"):
        assert s.error(runs["swapped"], view) < 0.5 * s.error(runs["front"], view)
        assert s.error(runs["swapped"], view) < s.error(runs["sides"], view) + 0.01
    sides = runs["swapped"].sides
    assert sides["back"][2] and not sides["left"][2] and not sides["right"][2]


def test_a_side_view_that_does_not_fit_is_skipped(scene, caplog):
    s, runs = scene
    wide = reference(checker(200, 8)[:60, :], at=(28, 100))  # nothing like the box's narrow side
    with caplog.at_level("WARNING"):
        r = s.run({"left": wide})
    iou, share, _ = r.sides["left"]
    assert iou < 0.8 and share == 0.0
    assert r.texture.tobytes() == runs["front"].texture.tobytes()
    assert "left" in caplog.text and "skipped" in caplog.text


def test_stage_projects_the_side_views_it_is_given(tmp_path, scene, caplog):
    s, runs = scene
    glb = m.save(m.textured(s.box, s.painted, roughness=0.5, metallic=0.0), tmp_path / "painted.glb")
    inputs = [StageInput(glb, ArtifactKind.MESH_GLB, "m")]
    for view in ("front", "back", "left", "right"):
        path = tmp_path / f"{view}.png"
        s.refs[view].save(path)
        inputs.append(StageInput(path, ArtifactKind.IMAGE_PNG, view, None if view == "front" else view))
    out = tmp_path / "out"
    out.mkdir()
    ctx = StageContext("match", StageType.TEXTURE_MATCH, inputs, {"align": False}, 0, out)
    with caplog.at_level("INFO"):
        result = TextureMatch().run(ctx)[0].path
    assert caplog.text.count("view fits at IoU") == 3 and "projected onto" in caplog.text
    got = m.load(result).visual.material.baseColorTexture
    assert got is not None and TextureMatch.version == "3"
    shown = match.MatchResult(got, runs["sides"].view, 1.0, (0, 0, 0), 0.0)
    assert s.error(shown, "back") < 0.5 * s.error(runs["front"], "back")
