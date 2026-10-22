"""Pull a painted texture back toward the reference image it was painted from.

Multiview painters regenerate every view, the one the reference shows included, so colors
drift per material and detail the reference has is lost. Seen from the reference's own camera,
the reference is ground truth. Shape models return a canonical pose, not the image's view
(concept art is often drawn from a three-quarter angle), so this module first fits that
camera, then:

1. learns per-color corrections in CIELAB on the surface the camera sees and applies them to
   every texel by color, so the back follows the front;
2. blends the reference onto texels that face the camera and are visible, feathered by how
   directly they face it and by the distance to the silhouette edge, after bending it a few
   pixels onto the surface it belongs to.

Cameras are orthographic along -Z after the view rotation (glTF front is +Z); image right is
+X and image up is +Y.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import trimesh
from numpy.typing import NDArray
from PIL import Image
from scipy import ndimage, signal
from scipy.cluster.vq import kmeans2

from forja_engine.ops import bake
from forja_engine.ops import mesh as mesh_ops

F64 = NDArray[np.float64]

_XYZ = np.array(
    [[0.4124564, 0.3575761, 0.1804375], [0.2126729, 0.7151522, 0.0721750], [0.0193339, 0.1191920, 0.9503041]]
)
_WHITE = np.array([0.95047, 1.0, 1.08883])
_D = 6 / 29
_LUMA = np.array([0.2126, 0.7152, 0.0722])


def srgb_to_lab(rgb: F64) -> F64:
    """sRGB in 0..1 (any leading shape, last axis 3) to CIELAB under D65."""
    lin = np.where(rgb <= 0.04045, rgb / 12.92, ((rgb + 0.055) / 1.055) ** 2.4)
    t = (lin @ _XYZ.T) / _WHITE
    f = np.where(t > _D**3, np.cbrt(t), t / (3 * _D**2) + 4 / 29)
    return np.stack([116 * f[..., 1] - 16, 500 * (f[..., 0] - f[..., 1]), 200 * (f[..., 1] - f[..., 2])], -1)


def lab_to_srgb(lab: F64) -> F64:
    fy = (lab[..., 0] + 16) / 116
    f = np.stack([fy + lab[..., 1] / 500, fy, fy - lab[..., 2] / 200], -1)
    t = np.where(f > _D, f**3, 3 * _D**2 * (f - 4 / 29)) * _WHITE
    lin = np.clip(t @ np.linalg.inv(_XYZ).T, 0, 1)
    return np.where(lin <= 0.0031308, lin * 12.92, 1.055 * lin ** (1 / 2.4) - 0.055)


@dataclass(frozen=True)
class Projection:
    """Front orthographic camera: pixel = (x * scale + ox, -y * scale + oy)."""

    scale: float
    ox: float
    oy: float

    def __call__(self, xyz: F64) -> F64:
        return np.stack([xyz[:, 0] * self.scale + self.ox, -xyz[:, 1] * self.scale + self.oy], -1)

    def resized(self, factor: float) -> Projection:
        return Projection(self.scale * factor, self.ox * factor, self.oy * factor)


@dataclass(frozen=True)
class View:
    """Where the reference was seen from: the mesh is turned by yaw (about +Y), then tilted by
    pitch (about +X, positive shows the top), then projected orthographically along -Z."""

    yaw: float
    pitch: float
    proj: Projection

    def rotation(self) -> F64:
        y, p = np.radians(self.yaw), np.radians(self.pitch)
        ry = np.array([[np.cos(y), 0, np.sin(y)], [0, 1, 0], [-np.sin(y), 0, np.cos(y)]])
        rx = np.array([[1, 0, 0], [0, np.cos(p), -np.sin(p)], [0, np.sin(p), np.cos(p)]])
        return rx @ ry


@dataclass(frozen=True)
class MatchResult:
    texture: Image.Image
    view: View
    iou: float  # silhouette vs reference alpha in the fitted view; low means a bad fit
    shift_lab: tuple[float, float, float]  # mean change applied to L, a, b
    reprojected: float  # share of texels that took any of the reference


def _iou(a: NDArray[np.bool_], b: NDArray[np.bool_]) -> float:
    union = int(np.count_nonzero(a | b))
    return int(np.count_nonzero(a & b)) / union if union else 0.0


def _rot(yaw: float, pitch: float) -> F64:
    return View(yaw, pitch, Projection(1, 0, 0)).rotation()


def _splat(
    xyz: F64, nz: F64, values: F64, proj: Projection, size: int
) -> tuple[NDArray[np.bool_], F64, NDArray[np.bool_]]:
    """Silhouette of all points, plus the mean value of camera-facing points per pixel.

    No depth test: the silhouette does not need one, and averaging only facing points is close
    enough to the visible surface for scoring. Skipping the sort keeps each candidate cheap.
    """
    px = proj(xyz)
    ix, iy = np.floor(px[:, 0]).astype(int), np.floor(px[:, 1]).astype(int)
    ok = (ix >= 0) & (ix < size) & (iy >= 0) & (iy < size)
    pix = iy[ok] * size + ix[ok]
    covered = np.bincount(pix, minlength=size * size) > 0
    front = nz[ok] > 0.2
    count = np.bincount(pix[front], minlength=size * size)
    total = np.bincount(pix[front], weights=values[ok][front], minlength=size * size)
    mean = np.where(count > 0, total / np.maximum(count, 1), 0.0)
    return covered.reshape(size, size), mean.reshape(size, size), (count > 0).reshape(size, size)


def _ncc(a: F64, b: F64, where: NDArray[np.bool_]) -> float:
    if np.count_nonzero(where) < 64:
        return 0.0
    x, y = a[where], b[where]
    x, y = x - x.mean(), y - y.mean()
    return float(np.sum(x * y) / max(np.sqrt(np.sum(x * x) * np.sum(y * y)), 1e-12))


def fit(
    points: F64,
    normals: F64,
    luma: F64,
    alpha: NDArray[np.bool_],
    ref_luma: F64,
    yaw0: float = 0.0,
    yaw_range: float = 45.0,
    pitch_range: tuple[float, float] = (-10.0, 30.0),
    work: int = 256,
) -> tuple[View, float]:
    """Camera that best overlays the mesh on the reference: yaw and pitch by coarse-to-fine
    search, scale and offset from the bounding box, scored by silhouette overlap plus
    correlation of luminance with the painted surface (which places eyes, straps and edges).

    Points are surface samples, so the cost does not grow with the triangle count.
    """
    size = alpha.shape[0]
    k = work / size
    target = np.asarray(Image.fromarray(alpha).resize((work, work), Image.Resampling.BOX)) > 0
    target_luma = ndimage.gaussian_filter(
        np.asarray(Image.fromarray(ref_luma.astype(np.float32)).resize((work, work), Image.Resampling.BOX)),
        1.0,
    )
    ys, xs = np.nonzero(alpha)
    top, bottom = int(np.min(ys)), int(np.max(ys)) + 1
    cx = (int(np.min(xs)) + int(np.max(xs)) + 1) / 2

    def place(yaw: float, pitch: float) -> tuple[View, float]:
        rot = _rot(yaw, pitch)
        xyz = points @ rot.T
        lo, hi = xyz[:, :2].min(0), xyz[:, :2].max(0)
        # Height is the steadier anchor: thin parts (a staff, a tail) change the width more.
        s = (bottom - top) / max(hi[1] - lo[1], 1e-9)
        proj = Projection(s, cx - s * (lo[0] + hi[0]) / 2, (top + bottom) / 2 + s * (lo[1] + hi[1]) / 2)
        covered, lum, front = _splat(xyz, normals @ rot[2], luma, proj.resized(k), work)
        mask = ndimage.binary_closing(covered, iterations=2)
        lum = ndimage.gaussian_filter(_fill(lum, front), 1.0)
        both = mask & target
        score = _iou(mask, target) + 0.5 * _ncc(lum, target_luma, ndimage.binary_erosion(both, iterations=2))
        return View(yaw, pitch, proj), score

    best_view, best = place(yaw0, 0.0)
    step = (15.0, 10.0)
    center = (yaw0, 0.0)
    while step[0] >= 1.0:
        for i in (-2, -1, 0, 1, 2):
            for j in (-1, 0, 1):
                yaw = float(np.clip(center[0] + i * step[0], yaw0 - yaw_range, yaw0 + yaw_range))
                pitch = float(np.clip(center[1] + j * step[1], *pitch_range))
                view, score = place(yaw, pitch)
                if score > best + 1e-6:
                    best_view, best = view, score
        center = (best_view.yaw, best_view.pitch)
        step = (step[0] / 2, step[1] / 2)

    # Final 2D refinement on silhouette overlap at the chosen angles.
    xyz = points @ best_view.rotation().T
    base = best_view.proj
    covered, _, _ = _splat(xyz, normals @ best_view.rotation()[2], luma, base.resized(k), work)
    mask = ndimage.binary_closing(covered, iterations=2)
    img = Image.fromarray(mask)
    c = work / 2

    def warped(z: float, dx: float, dy: float) -> NDArray[np.bool_]:
        coeffs = (1 / z, 0, c - (c + dx) / z, 0, 1 / z, c - (c + dy) / z)
        return (
            np.asarray(img.transform(img.size, Image.Transform.AFFINE, coeffs, Image.Resampling.NEAREST)) > 0
        )

    cur, iou = (1.0, 0.0, 0.0), _iou(mask, target)
    steps = [0.04, work * 0.02]
    while steps[1] >= 0.25:
        improved = False
        for dim, st in ((0, steps[0]), (1, steps[1]), (2, steps[1])):
            for sign in (-1, 1):
                cand = list(cur)
                cand[dim] += sign * st
                score = _iou(warped(*cand), target)
                if score > iou + 1e-6:
                    cur, iou, improved = (cand[0], cand[1], cand[2]), score, True
        if not improved:
            steps = [steps[0] / 2, steps[1] / 2]
    z, dx, dy = cur
    # Undo the warp in full-size pixels: p' = z * (p - c) + c + d.
    cf, dxf, dyf = size / 2, dx / k, dy / k
    proj = Projection(base.scale * z, z * (base.ox - cf) + cf + dxf, z * (base.oy - cf) + cf + dyf)
    return View(best_view.yaw, best_view.pitch, proj), iou


def _fill(values: F64, mask: NDArray[np.bool_]) -> F64:
    """Fill holes in a splatted image from the nearest covered pixel."""
    if mask.all() or not mask.any():
        return values
    _, (iy, ix) = ndimage.distance_transform_edt(~mask, return_indices=True)
    return values[iy, ix]


def zbuffer(
    vertices: F64, faces: NDArray[np.int64], proj: Projection, size: int
) -> tuple[NDArray[np.int64], NDArray[np.float32], F64]:
    """Nearest triangle id (-1 where empty), its barycentrics and its depth (+Z is near) per pixel."""
    tri_id = np.full((size, size), -1, dtype=np.int64)
    bary = np.zeros((size, size, 3), dtype=np.float32)
    depth = np.full((size, size), -np.inf)
    tri = proj(vertices)[faces] - 0.5  # pixel centers
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


def _sample(img: F64, px: F64) -> F64:
    """Bilinear sample of an (h, w, c) array at pixel coordinates (x, y), centers at +0.5."""
    coords = [px[:, 1] - 0.5, px[:, 0] - 0.5]
    return np.stack(
        [
            ndimage.map_coordinates(img[..., ch], coords, order=1, mode="nearest")
            for ch in range(img.shape[-1])
        ],
        -1,
    )


def color_transfer(
    rendered: F64,
    target: F64,
    lab: F64,
    color: float = 1.0,
    lightness: float = 0.0,
    clusters: int = 8,
    seed: int = 0,
    sigma: float = 12.0,
    prior: int = 500,
) -> F64:
    """Per-color corrections learned on the visible front, applied to every texel by color.

    Painters drift per material (a robe too green and too saturated, fur about right), and the
    back drifts further than the front, so neither one global transform nor fixed offsets fit.
    The front's rendered colors are clustered; each cluster learns a hue rotation and chroma
    scale about the neutral axis (a complex least-squares fit in the a/b plane) and a median
    lightness offset. Clusters with few samples lean on the global fit. Texels blend their
    nearest clusters' corrections, so the back follows the front by color, not by position.
    """
    rng = np.random.default_rng(seed)
    if len(rendered) > 50_000:
        pick = rng.choice(len(rendered), 50_000, replace=False)
        rendered, target = rendered[pick], target[pick]
    zr = rendered[:, 1] + 1j * rendered[:, 2]
    zt = target[:, 1] + 1j * target[:, 2]

    def chroma_fit(r: NDArray[np.complex128], t: NDArray[np.complex128]) -> complex:
        power = float(np.mean(np.abs(r) ** 2))
        if power < 4.0:
            return 1 + 0j  # near-gray: a hue angle means nothing and a scale stays near zero anyway
        w = complex(np.mean(t * np.conj(r)) / power)
        k, theta = np.clip(abs(w), 0.5, 1.5), np.clip(np.angle(w), -np.pi / 6, np.pi / 6)
        return complex(k * np.exp(1j * theta))

    overall_w, overall_l = chroma_fit(zr, zt), float(np.median(target[:, 0] - rendered[:, 0]))
    distinct = len(np.unique(np.round(rendered), axis=0))
    if distinct < 2:
        centers, label = rendered[:1], np.zeros(len(rendered), dtype=np.int64)
    else:
        centers, label = kmeans2(rendered, min(clusters, distinct), minit="++", seed=rng)
    ws = np.empty(len(centers), dtype=np.complex128)
    ls = np.empty(len(centers))
    for j in range(len(centers)):
        mem = label == j
        n = int(np.count_nonzero(mem))
        wj = chroma_fit(zr[mem], zt[mem]) if n else overall_w
        lj = float(np.median(target[mem, 0] - rendered[mem, 0])) if n else overall_l
        ws[j] = (n * wj + prior * overall_w) / (n + prior)
        ls[j] = (n * lj + prior * overall_l) / (n + prior)

    out = np.empty_like(lab)
    for start in range(0, len(lab), 1 << 18):  # chunks keep the texel x cluster table small
        part = lab[start : start + (1 << 18)]
        d2 = ((part[:, None, :] - centers[None]) ** 2).sum(-1)
        p = np.exp(-(d2 - d2.min(1, keepdims=True)) / (2 * sigma**2))
        p /= p.sum(1, keepdims=True)
        w = 1 + color * (p @ ws - 1)
        z = (part[:, 1] + 1j * part[:, 2]) * w
        out[start : start + len(part)] = np.stack([part[:, 0] + lightness * (p @ ls), z.real, z.imag], -1)
    return out


def flow(
    rendered: F64,
    reference: F64,
    mask: NDArray[np.bool_],
    block: int = 48,
    stride: int = 24,
    radius: int = 16,
    min_ncc: float = 0.3,
) -> F64:
    """Per-pixel (dx, dy) such that reference(p + d) shows what rendered(p) shows.

    The generated shape is never a pixel-exact match for the image, so a strap or a hand sits
    a few pixels off and reprojection would draw it twice. Blocks of the rendered view are
    matched against the reference by normalized cross-correlation of high-passed luminance
    (shading differs between the delit texture and the lit image, edges do not). Weak or
    featureless blocks keep zero motion; the field is smoothed so it bends, never tears.
    """
    size = rendered.shape[0]

    def highpass(x: F64) -> F64:
        return x - ndimage.gaussian_filter(x, block / 6)

    r, t = highpass(rendered), highpass(reference)
    t = np.pad(t, radius, mode="edge")
    half = block // 2
    centers = np.arange(half, size - half, stride)
    grid = np.zeros((len(centers), len(centers), 2))
    weight = np.zeros((len(centers), len(centers)))
    side = 2 * radius + 1
    for gi, cy in enumerate(centers):
        for gj, cx in enumerate(centers):
            if mask[cy - half : cy + half, cx - half : cx + half].mean() < 0.6:
                continue
            p = r[cy - half : cy + half, cx - half : cx + half]
            p = p - p.mean()
            pn = float(np.sqrt(np.sum(p * p)))
            if pn < 1e-3 * block:
                continue  # featureless: nothing to align on
            w = t[cy - half : cy + half + 2 * radius, cx - half : cx + half + 2 * radius]
            num = signal.correlate(w, p, mode="valid", method="fft")
            box = np.ones((block, block))
            s1 = signal.correlate(w, box, mode="valid", method="fft")
            s2 = signal.correlate(w * w, box, mode="valid", method="fft")
            var = np.maximum(s2 - s1 * s1 / block**2, 1e-12)
            ncc = num / (pn * np.sqrt(var))
            k = int(np.argmax(ncc))
            dy, dx = divmod(k, side)
            peak = float(ncc.flat[k])
            if peak >= min_ncc and peak > float(ncc[radius, radius]) + 0.02:
                grid[gi, gj] = (dx - radius, dy - radius)
                weight[gi, gj] = peak
            elif float(ncc[radius, radius]) >= min_ncc:
                weight[gi, gj] = float(ncc[radius, radius])  # already aligned: anchor zero motion
    # A median rejects lone outliers, a weighted blur fills the gaps between confident blocks.
    for ch in range(2):
        grid[..., ch] = ndimage.median_filter(grid[..., ch], size=3) * (weight > 0)
    num = ndimage.gaussian_filter(grid * weight[..., None], (1.0, 1.0, 0))
    den = ndimage.gaussian_filter(weight, 1.0)[..., None]
    smooth = np.where(den > 1e-6, num / np.maximum(den, 1e-6), 0.0)
    # Grid cell (i, j) sits at pixel (half + j * stride, half + i * stride).
    gy, gx = np.meshgrid((np.arange(size) - half) / stride, (np.arange(size) - half) / stride, indexing="ij")
    full = np.stack(
        [ndimage.map_coordinates(smooth[..., ch], [gy, gx], order=1, mode="nearest") for ch in range(2)], -1
    )
    return full


def _smoothstep(e0: float, e1: float, x: F64) -> F64:
    t = np.clip((x - e0) / max(e1 - e0, 1e-9), 0, 1)
    return t * t * (3 - 2 * t)


def match(
    mesh: trimesh.Trimesh,
    texture: Image.Image,
    reference: Image.Image,
    color: float = 1.0,
    lightness: float = 0.0,
    reproject: float = 1.0,
    facing: tuple[float, float] = (0.35, 0.75),
    feather: float = 0.015,
    min_iou: float = 0.8,
    samples: int = 200_000,
    clusters: int = 8,
    align: bool = True,
    yaw: float = 0.0,
    seed: int = 0,
) -> MatchResult:
    """`color` and `lightness` weight the chroma and lightness parts of the color transfer;
    `reproject` weights the reference on texels that face the fitted camera, and `align` lets
    it bend a few pixels onto the surface it belongs to. `yaw` is where the camera search
    starts, for shapes generated facing away from +Z. Below `min_iou` the fit is not trusted:
    the reference would land on the wrong surface, so only the color transfer runs.

    Lightness stays off by default: the reference carries its lighting, the painted texture is
    delit, so matching L would paint the key light into the albedo everywhere.
    """
    rgba = np.asarray(reference.convert("RGBA"), dtype=np.float64) / 255
    size = rgba.shape[0]
    alpha = rgba[..., 3] > 0.5
    ref_rgb = rgba[..., :3]
    tex = np.asarray(texture.convert("RGB"), dtype=np.float64) / 255
    uv = mesh_ops.uvs_of(mesh)
    f = np.asarray(mesh.faces, dtype=np.int64)

    def tex_at(face: NDArray[np.int64], weights: F64, image: F64 = tex) -> F64:
        puv = np.einsum("ij,ijk->ik", weights, uv[f[face]])
        return _sample(image, np.stack([puv[:, 0] * image.shape[1], (1 - puv[:, 1]) * image.shape[0]], -1))

    points, face = trimesh.sample.sample_surface(mesh, samples, seed=seed)
    luma = tex_at(face, trimesh.triangles.points_to_barycentric(mesh.triangles[face], points)) @ _LUMA
    normals = mesh.face_normals[face]
    view, iou = fit(np.asarray(points, dtype=np.float64), normals, luma, alpha, ref_rgb @ _LUMA, yaw0=yaw)
    rot = view.rotation()
    v = np.asarray(mesh.vertices, dtype=np.float64) @ rot.T
    tri_id, bary, depth = zbuffer(v, f, view.proj, size)

    # Pixels where the fitted render and the reference describe the same surface, seen head-on.
    inner = ndimage.binary_erosion(alpha, iterations=max(1, int(size * 0.006)))
    hit = tri_id >= 0
    face_nz = np.zeros((size, size))
    face_nz[hit] = (mesh.face_normals @ rot.T)[tri_id[hit], 2]
    ys, xs = np.nonzero(hit & inner & (face_nz > 0.5))
    if len(ys) < 20 * clusters:
        # The mesh and the reference barely overlap: nothing trustworthy to learn from.
        return MatchResult(
            texture=texture.convert("RGB"), view=view, iou=iou, shift_lab=(0.0, 0.0, 0.0), reprojected=0.0
        )
    rendered = srgb_to_lab(tex_at(tri_id[ys, xs], bary[ys, xs].astype(np.float64)))
    target = srgb_to_lab(ref_rgb[ys, xs])

    lab = srgb_to_lab(tex).reshape(-1, 3)
    moved = color_transfer(rendered, target, lab, color, lightness, clusters, seed)
    out = lab_to_srgb(moved).reshape(tex.shape)
    dl, da, db = (float(x) for x in (moved - lab).mean(0))

    reprojected = 0.0
    if reproject > 0 and iou >= min_iou:
        hy, hx = np.nonzero(hit)
        seen = np.zeros((size, size))
        seen[hy, hx] = tex_at(tri_id[hy, hx], bary[hy, hx].astype(np.float64), out) @ _LUMA
        motion = flow(_fill(seen, hit), ref_rgb @ _LUMA, hit & alpha) if align else np.zeros((size, size, 2))
        tid, tb = bake.raster_uv(uv, f, tex.shape[0])
        ty, tx = np.nonzero(tid >= 0)
        corners = f[tid[ty, tx]]
        w = tb[ty, tx].astype(np.float64)[..., None]
        pos = np.sum(v[corners] * w, axis=1)
        nrm = np.sum((np.asarray(mesh.vertex_normals, dtype=np.float64) @ rot.T)[corners] * w, axis=1)
        nrm /= np.maximum(np.linalg.norm(nrm, axis=1, keepdims=True), 1e-12)
        px = view.proj(pos)
        ix = np.clip(px[:, 0].astype(int), 0, size - 1)
        iy = np.clip(px[:, 1].astype(int), 0, size - 1)
        # Visible when nothing in the 3x3 neighbourhood is nearer: conservative at silhouette edges.
        near = ndimage.maximum_filter(depth, size=3)
        tol = 0.005 * float(np.linalg.norm(mesh.extents))
        visible = pos[:, 2] >= near[iy, ix] - tol
        edge = ndimage.distance_transform_edt(alpha)
        amount = (
            reproject
            * _smoothstep(*facing, nrm[:, 2])
            * np.clip(edge[iy, ix] / max(feather * size, 1.0), 0, 1)
            * visible
        )
        keep = amount > 0
        blend = amount[keep, None]
        ref_at = _sample(ref_rgb, px[keep] + motion[iy[keep], ix[keep]])
        out[ty[keep], tx[keep]] = out[ty[keep], tx[keep]] * (1 - blend) + ref_at * blend
        reprojected = float(keep.mean()) if len(keep) else 0.0

    img = Image.fromarray((np.clip(out, 0, 1) * 255).round().astype(np.uint8), "RGB")
    return MatchResult(texture=img, view=view, iou=iou, shift_lab=(dl, da, db), reprojected=reprojected)
