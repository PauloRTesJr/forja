"""What stages measure about their outputs (StageOutput.metrics) reaches the run; what built-ins say."""

import dataclasses
import json
import math
from pathlib import Path

import numpy as np
import pytest
import trimesh
from fastapi.testclient import TestClient
from PIL import Image

from forja_engine.adapters import builtin, stub
from forja_engine.adapters.builtin import Bake, Lods, TextureMatch
from forja_engine.api.app import EngineConfig, create_app
from forja_engine.ops import match as match_ops
from forja_engine.ops import mesh as m
from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageOutput, StageType
from forja_engine.store import hash_file
from forja_engine.worker import finite_metrics, outputs_to_json

FIXTURE = Path(__file__).parent / "fixtures" / "chest.png"

PIPELINE = [
    {"id": "bg", "use": "background-removal", "model": "stub-background", "inputs": []},
    {"id": "frame", "use": "framing", "inputs": ["stage:bg"]},
    {"id": "shape", "use": "shape", "model": "stub-shape", "inputs": ["stage:frame"]},
    {"id": "cleanup", "use": "mesh.cleanup", "inputs": ["stage:shape"]},
    {"id": "lowpoly", "use": "mesh.decimate", "params": {"target_tris": 3000}, "inputs": ["stage:cleanup"]},
    {"id": "uv", "use": "mesh.uv", "inputs": ["stage:lowpoly"]},
    {
        "id": "paint",
        "use": "texture",
        "model": "stub-paint",
        "params": {"resolution": 512},
        "inputs": ["stage:uv", "stage:frame"],
    },
    {"id": "match", "use": "texture.match", "inputs": ["stage:paint", "stage:frame"]},
    {"id": "bake", "use": "bake", "params": {"ao_samples": 8}, "inputs": ["stage:match", "stage:cleanup"]},
    {"id": "lods", "use": "mesh.lods", "inputs": ["stage:bake"]},
]


def run_to_the_end(client: TestClient, stages: list[dict]) -> dict:
    ref = hash_file(FIXTURE)
    assert client.put(f"/v1/artifacts/{ref}", content=FIXTURE.read_bytes()).status_code == 201
    stages = [{**stages[0], "inputs": [ref]}, *stages[1:]]
    run = client.post("/v1/runs", json={"stages": stages}).json()
    with client.stream("GET", f"/v1/runs/{run['id']}/events") as resp:
        for _ in resp.iter_lines():  # until the run ends
            pass
    done = client.get(f"/v1/runs/{run['id']}").json()
    assert done["status"] == "succeeded", done
    return done


# --- the wire ---------------------------------------------------------------------------------


def test_only_finite_numbers_are_kept():
    kept = finite_metrics({"a": 1, "b": 0.5, "nan": math.nan, "inf": math.inf, "ninf": -math.inf, "t": True})
    assert kept == {"a": 1.0, "b": 0.5}
    assert all(isinstance(v, float) for v in kept.values())


def test_outputs_carry_metrics_as_valid_json(tmp_path):
    out = StageOutput(tmp_path / "x.glb", ArtifactKind.MESH_GLB, metrics={"match_iou": 0.9, "bad": math.nan})
    [encoded] = outputs_to_json([out])
    assert encoded["metrics"] == {"match_iou": 0.9}
    json.loads(json.dumps(encoded, allow_nan=False))  # a NaN would make this line fail
    assert outputs_to_json([StageOutput(tmp_path / "y.glb", ArtifactKind.MESH_GLB)])[0]["metrics"] == {}


def test_metrics_default_to_a_dict_of_their_own_on_a_frozen_output(tmp_path):
    a, b = (
        StageOutput(tmp_path / "a", ArtifactKind.MESH_GLB),
        StageOutput(tmp_path / "b", ArtifactKind.MESH_GLB),
    )
    assert a.metrics == {} and a.metrics is not b.metrics
    with pytest.raises(dataclasses.FrozenInstanceError):
        a.metrics = {"x": 1.0}  # type: ignore[misc]


def test_metrics_reach_the_run_and_non_finite_ones_do_not(tmp_path, monkeypatch):
    shape = stub.StubShape.run

    def measured(self, ctx):
        metrics = {"made_up_iou": 0.75, "broken": math.nan, "unbounded": math.inf}
        return [dataclasses.replace(o, metrics=metrics) for o in shape(self, ctx)]

    monkeypatch.setattr(stub.StubShape, "run", measured)
    app = create_app(EngineConfig(store_root=tmp_path, token="secret", in_process_models=True))
    with TestClient(app, headers={"Authorization": "Bearer secret"}) as c:
        run = run_to_the_end(c, PIPELINE[:3])
        events = c.get(f"/v1/runs/{run['id']}/events").text
    assert run["stages"]["shape"]["outputs"][0]["metrics"] == {"made_up_iou": 0.75}
    assert run["stages"]["bg"]["outputs"][0]["metrics"] == {}
    assert "NaN" not in json.dumps(run, allow_nan=False) and "NaN" not in events


def test_the_built_in_stages_report_what_they_measured_through_the_api(tmp_path):
    app = create_app(EngineConfig(store_root=tmp_path, token="secret"))
    with TestClient(app, headers={"Authorization": "Bearer secret"}) as c:
        run = run_to_the_end(c, PIPELINE)
    metrics = {sid: st["outputs"][0]["metrics"] for sid, st in run["stages"].items()}
    assert {"match_iou", "match_reprojected"} <= set(metrics["match"])
    assert {"bake_hit_ratio", "bake_cage"} <= set(metrics["bake"])
    assert {"lod_open_edges", "uv_coverage"} <= set(metrics["lods"])
    assert all(metrics[sid] == {} for sid in ("bg", "frame", "cleanup", "lowpoly", "uv"))


# --- open_edges -------------------------------------------------------------------------------


def test_open_edges_counts_cracks_and_not_uv_seams():
    ball = trimesh.creation.icosphere(subdivisions=3)
    assert m.open_edges(ball) == 0
    # Unwrapping splits vertices along the UV seams; the surface is still closed.
    seamed = m.unwrap(ball)
    assert len(seamed.vertices) > len(ball.vertices)
    assert m.open_edges(seamed) == 0
    cut = trimesh.Trimesh(ball.vertices, ball.faces[3:], process=False)  # three triangles missing
    assert m.open_edges(cut) >= 3
    # A crack: the two halves keep their own vertices, and one moved a hair away from the other.
    south = ball.triangles_center[:, 2] < 0
    north, bottom = ball.submesh([~south])[0], ball.submesh([south])[0]
    assert m.open_edges(trimesh.util.concatenate([north, bottom])) == 0  # touching halves weld shut
    bottom.apply_translation([0.0, 0.0, -0.01])
    assert m.open_edges(trimesh.util.concatenate([north, bottom])) > 0


# --- what each built-in says -------------------------------------------------------------------


def plane(n: int = 8) -> trimesh.Trimesh:
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
    u, v = np.meshgrid(np.linspace(0, 1, size), np.linspace(0, 1, size))
    rgb = np.stack([0.3 + 0.4 * u, 0.5 - 0.2 * v, 0.4 + 0.2 * u * v], -1)
    return Image.fromarray((rgb * 255).astype(np.uint8), "RGB")


def mesh_ctx(task, paths, tmp_path, **params):
    out = tmp_path / "out"
    out.mkdir(exist_ok=True)
    inputs = [StageInput(p, ArtifactKind.MESH_GLB, f"h{i}") for i, p in enumerate(paths)]
    return StageContext("s", task, inputs, params, 0, out)


def test_texture_match_reports_the_fit_and_the_reprojected_share(tmp_path):
    glb = m.save(m.textured(plane(), gradient(128), roughness=0.5, metallic=0.1), tmp_path / "painted.glb")
    ref = tmp_path / "ref.png"
    subject = np.zeros((256, 256, 4), dtype=np.uint8)
    subject[28:228, 28:228] = (230, 40, 40, 255)
    Image.fromarray(subject, "RGBA").save(ref)
    ctx = mesh_ctx(StageType.TEXTURE_MATCH, [glb], tmp_path)
    ctx = dataclasses.replace(ctx, inputs=[*ctx.inputs, StageInput(ref, ArtifactKind.IMAGE_PNG, "r")])
    metrics = TextureMatch().run(ctx)[0].metrics
    assert set(metrics) == {"match_iou", "match_reprojected"}
    assert 0.0 <= metrics["match_iou"] <= 1.0 and 0.0 <= metrics["match_reprojected"] <= 1.0


def test_texture_match_reports_each_side_view_it_was_given(tmp_path, monkeypatch):
    glb = m.save(m.textured(plane(), gradient(128), roughness=0.5, metallic=0.1), tmp_path / "painted.glb")
    ref = tmp_path / "ref.png"
    Image.new("RGBA", (64, 64), (200, 50, 50, 255)).save(ref)
    left = tmp_path / "left.png"
    Image.new("RGBA", (64, 64), (50, 50, 200, 255)).save(left)

    def fake(mesh, base, reference, **kw):
        assert set(kw["sides"]) == {"left"}
        return match_ops.MatchResult(
            base, None, 0.93, (0, 0, 0), 0.41, {"left": (0.62, 0.2, True), "back": (0.5, 0.0, False)}
        )

    monkeypatch.setattr(builtin.match_ops, "match", fake)
    ctx = mesh_ctx(StageType.TEXTURE_MATCH, [glb], tmp_path)
    inputs = [
        *ctx.inputs,
        StageInput(ref, ArtifactKind.IMAGE_PNG, "r"),
        StageInput(left, ArtifactKind.IMAGE_PNG, "l", "left"),
    ]
    metrics = TextureMatch().run(dataclasses.replace(ctx, inputs=inputs))[0].metrics
    assert metrics == {
        "match_iou": 0.93,
        "match_reprojected": 0.41,
        "match_left_iou": 0.62,
        "match_back_iou": 0.5,
    }


def test_bake_reports_how_much_it_reached_and_the_cage_it_used(tmp_path):
    high = trimesh.creation.icosphere(subdivisions=4)
    low = m.textured(m.unwrap(m.decimate(high, 300)), Image.new("RGB", (64, 64), (180, 120, 60)), 0.6, 0.1)
    paths = [m.save(low, tmp_path / "low.glb"), m.save(high, tmp_path / "high.glb")]
    metrics = Bake().run(mesh_ctx(StageType.BAKE, paths, tmp_path, ao_samples=4))[0].metrics
    assert set(metrics) == {"bake_hit_ratio", "bake_cage"}
    assert 0.9 <= metrics["bake_hit_ratio"] <= 1.0 and metrics["bake_cage"] > 0


def test_lods_report_the_worst_level_s_open_edges_and_the_uv_space_lod0_uses(tmp_path):
    ball = m.unwrap(trimesh.creation.icosphere(subdivisions=3))
    src = m.save(ball, tmp_path / "ball.glb")
    out = Lods().run(mesh_ctx(StageType.MESH_LODS, [src], tmp_path))[0]
    levels = trimesh.load(out.path, file_type="glb", process=False).geometry
    assert len(levels) == 3
    assert out.metrics["lod_open_edges"] == max(m.open_edges(level) for level in levels.values())
    assert 0.05 < out.metrics["uv_coverage"] < 1.0


def test_lods_of_an_open_surface_count_its_open_edges(tmp_path):
    src = m.save(plane(), tmp_path / "plane.glb")
    metrics = Lods().run(mesh_ctx(StageType.MESH_LODS, [src], tmp_path, ratios=[0.5]))[0].metrics
    assert metrics["lod_open_edges"] >= 4  # the plane's rim, at the very least
