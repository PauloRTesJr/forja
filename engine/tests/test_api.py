import json
import os
import struct
import time
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from PIL import Image

from forja_engine.adapters.builtin import BUILTINS
from forja_engine.api.app import EngineConfig, create_app
from forja_engine.sdk import StageType
from forja_engine.store import hash_file

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
    {"id": "export", "use": "export", "inputs": ["stage:lods"]},
]


@pytest.fixture
def client(tmp_path):
    app = create_app(EngineConfig(store_root=tmp_path, token="secret"))
    with TestClient(app, headers={"Authorization": "Bearer secret"}) as c:
        yield c


def upload(client) -> str:
    ref = hash_file(FIXTURE)
    assert client.head(f"/v1/artifacts/{ref}").status_code == 404
    assert client.put(f"/v1/artifacts/{ref}", content=FIXTURE.read_bytes()).status_code == 201
    assert client.head(f"/v1/artifacts/{ref}").status_code == 200
    return ref


def events(client, run_id):
    out = []
    with client.stream("GET", f"/v1/runs/{run_id}/events") as resp:
        for line in resp.iter_lines():
            if line.startswith("event: "):
                out.append([line[7:], None])
            elif line.startswith("data: "):
                out[-1][1] = json.loads(line[6:])
    return out


def test_health_needs_no_token(tmp_path):
    with TestClient(create_app(EngineConfig(store_root=tmp_path, token="t"))) as c:
        assert c.get("/v1/health").status_code == 200
        assert c.get("/v1/info").status_code == 401


def test_info_and_models_say_what_each_stage_runs(client):
    """The core keys its cache by these: a new adapter or new weights must not reuse old outputs."""
    info = client.get("/v1/info").json()
    assert info["stages"]["bake"] == BUILTINS[StageType.BAKE].version
    assert set(info["stages"]) == {str(t) for t in BUILTINS}
    models = {m["id"]: m for m in client.get("/v1/models").json()}
    assert models["stub-shape"]["adapter_version"] == "1" and models["stub-shape"]["upstream"] == ""
    assert models["triposg"]["weights"]["revision"] and len(models["triposg"]["upstream"]) == 64


def test_upload_rejects_wrong_hash(client):
    ref = "blake3:" + "0" * 64
    r = client.put(f"/v1/artifacts/{ref}", content=b"not matching")
    assert r.status_code == 422


def test_full_pipeline_with_stub_models_in_workers(client):
    image = upload(client)
    stages = [dict(s) for s in PIPELINE]
    stages[0] = {**stages[0], "inputs": [image]}
    run = client.post("/v1/runs", json={"priority": "interactive", "stages": stages}).json()
    evs = events(client, run["id"])
    kinds = [e[0] for e in evs]
    assert kinds[-1] == "run.finished" and evs[-1][1]["status"] == "succeeded", evs[-1]
    assert "stage.progress" in kinds
    previews = [e[1] for e in evs if e[0] == "artifact.ready" and e[1]["preview"]]
    assert previews and previews[0]["stage"] == "shape"
    final = client.get(f"/v1/runs/{run['id']}").json()
    glb = final["stages"]["export"]["outputs"][0]["hash"]
    body = client.get(f"/v1/artifacts/{glb}").content
    assert body[:4] == b"glTF"
    n = struct.unpack_from("<I", body, 12)[0]
    json.loads(body[20 : 20 + n], parse_constant=lambda c: pytest.fail(f"{c} in the GLB JSON"))


def test_same_inputs_give_identical_outputs(client):
    image = upload(client)
    stages = [{**PIPELINE[0], "inputs": [image]}, *PIPELINE[1:3]]
    hashes = []
    for _ in range(2):
        run = client.post("/v1/runs", json={"stages": stages}).json()
        events(client, run["id"])
        hashes.append(client.get(f"/v1/runs/{run['id']}").json()["stages"]["shape"]["outputs"][0]["hash"])
    assert hashes[0] == hashes[1]


def test_idempotent_submission(client):
    image = upload(client)
    body = {"client_run_id": "job-1", "stages": [{**PIPELINE[0], "inputs": [image]}]}
    a = client.post("/v1/runs", json=body).json()
    b = client.post("/v1/runs", json=body).json()
    assert a["id"] == b["id"]


def test_unknown_model_is_a_problem_document(client):
    image = upload(client)
    r = client.post(
        "/v1/runs", json={"stages": [{"id": "s", "use": "shape", "model": "nope", "inputs": [image]}]}
    )
    assert r.status_code == 422
    assert r.headers["content-type"].startswith("application/problem+json")


def test_missing_artifact_is_refused(client):
    r = client.post("/v1/runs", json={"stages": [{**PIPELINE[0], "inputs": ["blake3:" + "a" * 64]}]})
    assert r.status_code == 409


def upload_image(client, img: Image.Image, path: Path) -> str:
    img.save(path)
    ref = hash_file(path)
    assert client.put(f"/v1/artifacts/{ref}", content=path.read_bytes()).status_code == 201
    return ref


def finished(client, body) -> dict:
    run = client.post("/v1/runs", json=body).json()
    evs = events(client, run["id"])
    assert evs[-1][1]["status"] == "succeeded", evs[-1]
    return client.get(f"/v1/runs/{run['id']}").json()


def test_side_views_reach_the_stage_that_reads_them(client, tmp_path):
    front = upload(client)
    side = Image.new("RGBA", (200, 400), (0, 0, 0, 0))
    side.paste((200, 50, 50, 255), (60, 20, 140, 380))
    left = upload_image(client, side, tmp_path / "left.png")

    def prep(suffix, ref):
        return [
            {"id": f"bg{suffix}", "use": "background-removal", "model": "stub-background", "inputs": [ref]},
            {"id": f"frame{suffix}", "use": "framing", "inputs": [f"stage:bg{suffix}"]},
        ]

    shape = {"id": "shape", "use": "shape", "model": "stub-shape", "inputs": ["stage:frame"]}
    alone = finished(client, {"stages": [*prep("", front), shape]})
    with_side = {**shape, "inputs": ["stage:frame", "stage:frame-left"], "views": ["front", "left"]}
    both = finished(client, {"stages": [*prep("", front), *prep("-left", left), with_side]})
    hashes = [r["stages"]["shape"]["outputs"][0]["hash"] for r in (alone, both)]
    assert hashes[0] != hashes[1]
    bad = {**with_side, "views": ["front"]}
    r = client.post("/v1/runs", json={"stages": [*prep("", front), *prep("-left", left), bad]})
    assert r.status_code == 422


def test_sheet_detection_runs_as_a_builtin_stage(client):
    image = upload(client)
    stages = [
        {**PIPELINE[0], "inputs": [image]},
        {"id": "sheet", "use": "views.detect", "inputs": ["stage:bg"]},
    ]
    out = finished(client, {"stages": stages})["stages"]["sheet"]["outputs"][0]
    assert out["kind"] == "application/json"
    found = json.loads(client.get(f"/v1/artifacts/{out['hash']}").content)
    assert found["sheet"] is False and len(found["parts"]) >= 1


def test_asking_for_an_artifact_marks_it_used(client, tmp_path):
    ref = upload(client)
    path = tmp_path / "objects" / ref[7:9] / ref[9:]
    old = time.time() - 48 * 3600
    for ask in (client.head, client.get, lambda url: client.put(url, content=FIXTURE.read_bytes())):
        os.utime(path, (old, old))
        assert ask(f"/v1/artifacts/{ref}").status_code in (200, 201)
        assert time.time() - path.stat().st_mtime < 60
