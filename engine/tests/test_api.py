import json
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from forja_engine.api.app import EngineConfig, create_app
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
    {"id": "lods", "use": "mesh.lods", "inputs": ["stage:paint"]},
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
