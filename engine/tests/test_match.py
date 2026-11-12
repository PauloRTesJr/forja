import numpy as np
import pytest
import trimesh
from PIL import Image
from scipy import ndimage

from forja_engine.adapters.builtin import TextureMatch
from forja_engine.ops import image as img_ops
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
    assert got is not None and TextureMatch.version == "4"
    shown = match.MatchResult(got, runs["sides"].view, 1.0, (0, 0, 0), 0.0)
    assert s.error(shown, "back") < 0.5 * s.error(runs["front"], "back")


def _zbuffer_loop(vertices, faces, proj, size):
    """The z-buffer as it was before it was batched: one triangle at a time."""
    tri_id = np.full((size, size), -1, dtype=np.int64)
    bary = np.zeros((size, size, 3), dtype=np.float32)
    depth = np.full((size, size), -np.inf)
    tri = proj(vertices)[faces] - 0.5
    tz = vertices[:, 2][faces]
    lo = np.clip(np.floor(tri.min(axis=1)).astype(int), 0, size - 1)
    hi = np.clip(np.ceil(tri.max(axis=1)).astype(int), 0, size - 1)
    for t in range(len(faces)):
        (x0, y0), (x1, y1) = lo[t], hi[t]
        xs, ys = np.meshgrid(np.arange(x0, x1 + 1), np.arange(y0, y1 + 1))
        a, b, c = tri[t]
        det = (b[1] - c[1]) * (a[0] - c[0]) + (c[0] - b[0]) * (a[1] - c[1])
        if abs(det) < 1e-12:
            continue
        w0 = ((b[1] - c[1]) * (xs - c[0]) + (c[0] - b[0]) * (ys - c[1])) / det
        w1 = ((c[1] - a[1]) * (xs - c[0]) + (a[0] - c[0]) * (ys - c[1])) / det
        w2 = 1.0 - w0 - w1
        z = w0 * tz[t, 0] + w1 * tz[t, 1] + w2 * tz[t, 2]
        win = (w0 >= -1e-4) & (w1 >= -1e-4) & (w2 >= -1e-4) & (z > depth[ys, xs])
        yy, xx = ys[win], xs[win]
        tri_id[yy, xx] = t
        depth[yy, xx] = z[win]
        bary[yy, xx] = np.stack([w0[win], w1[win], w2[win]], axis=-1)
    return tri_id, bary, depth


def test_zbuffer_draws_what_the_triangle_by_triangle_loop_did(monkeypatch):
    rng = np.random.default_rng(3)
    # Many overlapping triangles at random depths, some degenerate, some off screen, and a pair
    # of identical faces: equally near, so the lower id must win. Tiny batches cross-check the merge.
    verts = rng.uniform(-0.3, 1.3, (600, 3))
    faces = rng.integers(0, 600, (400, 3))
    faces[5] = faces[4]
    faces[7] = (faces[7, 0], faces[7, 0], faces[7, 1])
    verts[:, 2] = np.round(verts[:, 2], 1)  # coarse depths make ties common
    monkeypatch.setattr(match, "_ZBUFFER_BATCH", 1 << 9)
    proj = match.Projection(60.0, 2.0, 62.0)
    for got, want in zip(
        match.zbuffer(verts, faces, proj, 64), _zbuffer_loop(verts, faces, proj, 64), strict=True
    ):
        assert np.array_equal(got, want)
    box = trimesh.creation.box((1.0, 1.6, 0.5))
    v = np.asarray(box.vertices) @ match.View(25.0, 10.0, match.Projection(1, 0, 0)).rotation().T
    proj = match.Projection(80.0, 64.0, 64.0)
    for got, want in zip(
        match.zbuffer(v, np.asarray(box.faces), proj, 128),
        _zbuffer_loop(v, np.asarray(box.faces), proj, 128),
        strict=True,
    ):
        assert np.array_equal(got, want)


def test_match_is_what_it_was_before_the_batched_zbuffer(scene, monkeypatch):
    s, runs = scene
    now = match.match(plane(), gradient(128), reference(checker(200, 8)))
    sided = s.run(s.sides)
    monkeypatch.setattr(match, "zbuffer", _zbuffer_loop)
    assert (
        match.match(plane(), gradient(128), reference(checker(200, 8))).texture.tobytes()
        == now.texture.tobytes()
    )
    assert s.run(s.sides).texture.tobytes() == sided.texture.tobytes() == runs["sides"].texture.tobytes()


LO, HI = 1024, 2048


def _fine_reference() -> tuple[Image.Image, Image.Image]:
    """The same picture at 1024 and 2048 px: a 2 px checker, and what it averages to, flat gray."""
    hi = reference(checker(1600, 800), HI, at=(224, 224))
    flat = reference(np.full((800, 800), 0.525), LO, at=(112, 112))  # the checker's mean, 0.9 and 0.15
    return flat, hi


def _detail(texture: Image.Image) -> float:
    """Energy of what a 1.5 px blur removes, in the middle of the texture where the plane is."""
    gray = np.asarray(texture.convert("L"), dtype=np.float64)
    fine = (gray - ndimage.gaussian_filter(gray, 1.5))[512:1536, 512:1536]
    return float(fine.std())


def test_a_larger_reference_puts_its_detail_into_a_large_texture():
    lo, hi = _fine_reference()
    assert np.asarray(lo.convert("L"))[300:700, 300:700].std() < 1.5  # flat at 1024
    tex = gradient(HI)
    plain = match.match(plane(), tex, lo, align=False)
    sharp = match.match(plane(), tex, lo, align=False, reference_hi=hi)
    assert _detail(plain.texture) < 0.5
    assert _detail(sharp.texture) > 20 * _detail(plain.texture) and _detail(sharp.texture) > 10
    # The camera and the colors come from the small picture: the same fit and the same average color.
    assert sharp.view == plain.view and sharp.iou == plain.iou and sharp.shift_lab == plain.shift_lab

    def mean(r: match.MatchResult) -> np.ndarray:
        return np.asarray(r.texture, dtype=np.float64)[512:1536, 512:1536].mean((0, 1))

    assert np.abs(mean(sharp) - mean(plain)).max() < 8


@pytest.mark.parametrize("name", ["same size", "smaller", "another picture"])
def test_a_larger_reference_that_cannot_be_used_is_ignored(name, caplog):
    lo, _ = _fine_reference()
    bar = reference(checker(1600, 800)[:, :300], HI, at=(224, 224))  # larger, but not this silhouette
    other = {"same size": lo, "smaller": lo.resize((512, 512)), "another picture": bar}[name]
    tex = gradient(256)
    base = match.match(plane(), tex, lo)
    with caplog.at_level("WARNING"):
        got = match.match(plane(), tex, lo, reference_hi=other)
    assert got.texture.tobytes() == base.texture.tobytes() and got.reprojected == base.reprojected
    assert ("not the same picture" in caplog.text) == (name == "another picture")


def test_the_alignment_widths_follow_the_picture_size(monkeypatch):
    seen = []

    def spy(rendered, reference, mask, block=48, stride=24, radius=16, **kw):
        seen.append((rendered.shape[0], block, stride, radius))
        return np.zeros((*rendered.shape, 2))

    monkeypatch.setattr(match, "flow", spy)
    lo, hi = _fine_reference()
    match.match(plane(), gradient(64), reference(checker(200, 8)))
    match.match(plane(), gradient(64), lo)
    match.match(plane(), gradient(64), lo, reference_hi=hi)
    # Unchanged up to 1024 px (smaller pictures keep the widths too); twice as wide at 2048.
    assert seen == [(256, 48, 24, 16), (1024, 48, 24, 16), (2048, 96, 48, 32)]


def test_flow_reaches_as_far_at_2048_as_at_1024():
    rng = np.random.default_rng(0)
    rendered = ndimage.gaussian_filter(rng.uniform(0, 1, (HI, HI)), 8)
    target = np.roll(rendered, (6, 28), axis=(0, 1))  # target(p) shows rendered(p - d)
    mask = np.zeros((HI, HI), dtype=bool)
    mask[512:1536, 512:1536] = True
    moved = match.flow(rendered, target, mask, 96, 48, 32)[640:1408, 640:1408]
    assert abs(np.median(moved[..., 0]) - 28) < 2 and abs(np.median(moved[..., 1]) - 6) < 2
    # 28 px at 2048 is 14 px at 1024: what the unscaled widths reach at 1024, and not at 2048.
    narrow = match.flow(rendered, target, mask)[640:1408, 640:1408]
    assert abs(np.median(narrow[..., 0]) - 28) > 6


def _stage(tmp_path, inputs, **params):
    glb = m.save(m.textured(plane(), gradient(128), roughness=0.5, metallic=0.1), tmp_path / "painted.glb")
    out = tmp_path / "out"
    out.mkdir(exist_ok=True)
    ctx = StageContext(
        "match",
        StageType.TEXTURE_MATCH,
        [StageInput(glb, ArtifactKind.MESH_GLB, "m"), *inputs],
        params,
        0,
        out,
    )
    return ctx


def _png(tmp_path, name, image, view=None):
    path = tmp_path / f"{name}.png"
    image.save(path)
    return StageInput(path, ArtifactKind.IMAGE_PNG, name, view)


@pytest.fixture
def spy(monkeypatch):
    """Runs of match() as the adapter calls them: the larger reference it was given."""
    given = []
    real = match.match

    def record(*args, **kwargs):
        given.append(kwargs.get("reference_hi"))
        return real(*args, **kwargs)

    monkeypatch.setattr("forja_engine.adapters.builtin.match_ops.match", record)
    return given


CUTOUT = reference(checker(800, 8), 1000, at=(100, 100))  # framed: int(800 * 1.16) = 927 px
FRAMED = img_ops.frame(CUTOUT, 256)  # what the framing stage made of it


@pytest.mark.parametrize(
    ("front_view", "cutout_view"), [(None, None), ("front", None), (None, "front"), ("front", "front")]
)
def test_stage_finds_the_cutout_with_or_without_view_tags(tmp_path, spy, front_view, cutout_view):
    inputs = [_png(tmp_path, "frame", FRAMED, front_view), _png(tmp_path, "bg", CUTOUT, cutout_view)]
    TextureMatch().run(_stage(tmp_path, inputs))
    assert spy[0] is not None and spy[0].size == (927, 927)
    # It is the cut-out framed like the reference, only larger.
    assert np.asarray(spy[0].resize((256, 256), Image.Resampling.BOX).getchannel("A")).mean() > 0


def test_stage_takes_the_cutout_beside_side_views(tmp_path, spy):
    inputs = [
        _png(tmp_path, "frame", FRAMED, "front"),
        _png(tmp_path, "bg", CUTOUT),
        _png(tmp_path, "frame-left", reference(checker(200, 8)[:, :60], at=(98, 28)), "left"),
    ]
    TextureMatch().run(_stage(tmp_path, inputs))
    assert spy[0] is not None and spy[0].size == (927, 927)


def test_stage_without_a_cutout_is_what_it_was(tmp_path, spy):
    framed = [_png(tmp_path, "frame", FRAMED)]
    TextureMatch().run(_stage(tmp_path, framed))
    assert spy == [None]
    (tmp_path / "sides").mkdir()
    only_sides = [*framed, _png(tmp_path, "frame-left", FRAMED, "left")]
    TextureMatch().run(_stage(tmp_path / "sides", only_sides))
    assert spy[1] is None  # a side view is not a cut-out


def test_stage_uses_the_cutout_only_when_it_has_more_detail(tmp_path, spy):
    small = reference(checker(150, 8), 200, at=(25, 25))  # natural size 174 px, under the framed 256
    inputs = [_png(tmp_path, "frame", FRAMED), _png(tmp_path, "bg", small)]
    TextureMatch().run(_stage(tmp_path, inputs))
    assert spy == [None]


def test_stage_caps_the_larger_reference_at_reference_size(tmp_path, spy):
    inputs = [_png(tmp_path, "frame", FRAMED), _png(tmp_path, "bg", CUTOUT)]
    TextureMatch().run(_stage(tmp_path, inputs, reference_size=512))
    TextureMatch().run(_stage(tmp_path, inputs, reference_size=256))  # not larger than the framed one
    assert spy[0] is not None and spy[0].size == (512, 512) and spy[1] is None
