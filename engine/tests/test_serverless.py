import base64
import json
import threading
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest
from fastapi.testclient import TestClient

from forja_engine.api.app import EngineConfig, create_app
from forja_engine.serverless import runpod
from forja_engine.serverless.gateway import Backend, MemoryBoard, create_gateway, serve
from forja_engine.serverless.worker import EngineError, Reply, Worker
from forja_engine.store import hash_file

FIXTURE = Path(__file__).parent / "fixtures" / "chest.png"

STAGES = [
    {"id": "bg", "use": "background-removal", "model": "stub-background", "inputs": []},
    {"id": "frame", "use": "framing", "params": {"size": 128}, "inputs": ["stage:bg"]},
]


class ClientEngine:
    """The worker's view of an engine, over a TestClient instead of a socket."""

    def __init__(self, client: TestClient) -> None:
        self.client = client

    def request(self, method: str, path: str, body: bytes | None = None, json_body: Any = None) -> Reply:
        r = self.client.request(method, path, content=body, json=json_body)
        return Reply(r.status_code, r.content)

    def lines(self, path: str) -> Iterator[str]:
        with self.client.stream("GET", path) as resp:
            yield from resp.iter_lines()


@pytest.fixture
def engine(tmp_path):
    app = create_app(EngineConfig(store_root=tmp_path / "store", token="secret"))
    with TestClient(app, headers={"Authorization": "Bearer secret"}) as c:
        yield c


def run_job(ref: str, **extra: Any) -> dict[str, Any]:
    stages = [dict(STAGES[0], inputs=[ref]), STAGES[1]]
    return {"op": "run", "run": {"client_run_id": "job-1", "stages": stages}, **extra}


def test_a_run_job_takes_inputs_inline_streams_events_and_returns_outputs(engine):
    worker = Worker(ClientEngine(engine))
    ref = hash_file(FIXTURE)
    job = run_job(ref, inline={ref: base64.b64encode(FIXTURE.read_bytes()).decode()}, outputs="inline")
    items = list(worker.handle(job))
    events = [i["event"]["type"] for i in items if "event" in i]
    assert events[0] == "run.started" and events[-1] == "run.finished" and "artifact.ready" in events
    result = items[-1]["result"]
    assert result["run"]["status"] == "succeeded"
    frame = result["run"]["stages"]["frame"]["outputs"][0]["hash"]
    raw = base64.b64decode(result["artifacts"][frame])
    assert raw.startswith(b"\x89PNG")
    # What the worker runs comes back with every run, so the core's description stays fresh.
    assert result["info"]["api_version"] and any(m["id"] == "stub-shape" for m in result["models"])


def test_an_input_the_worker_lacks_is_named_instead_of_run(engine):
    worker = Worker(ClientEngine(engine))
    ref = hash_file(FIXTURE)
    assert list(worker.handle(run_job(ref))) == [{"result": {"missing": [ref]}}]


def test_inline_outputs_stop_at_the_limit(engine):
    worker = Worker(ClientEngine(engine))
    ref = hash_file(FIXTURE)
    job = run_job(
        ref, inline={ref: base64.b64encode(FIXTURE.read_bytes()).decode()}, outputs="inline", limit=100
    )
    with pytest.raises(EngineError, match="network volume"):
        list(worker.handle(job))


def test_a_wrong_inline_artifact_is_refused(engine):
    worker = Worker(ClientEngine(engine))
    ref = hash_file(FIXTURE)
    with pytest.raises(EngineError, match="hash mismatch"):
        list(worker.handle(run_job(ref, inline={ref: base64.b64encode(b"not the image").decode()})))


def test_ensure_installs_only_what_is_missing(engine):
    worker = Worker(ClientEngine(engine))
    worker.ensure(["stub-shape", "stub-paint"])  # in the engine's own environment: nothing to do
    with pytest.raises(EngineError, match="no model nope"):
        worker.ensure(["nope"])


def test_the_runpod_handler_reports_errors_as_items(monkeypatch, engine):
    monkeypatch.setattr(runpod, "_worker", Worker(ClientEngine(engine)))
    items = list(runpod.handler({"input": {"op": "nothing"}}))
    assert len(items) == 1 and "error" in items[0]
    described = list(runpod.handler({"input": {"op": "describe"}}))
    assert described[0]["result"]["info"]["stages"]


def test_the_runpod_worker_keeps_its_data_on_the_network_volume(monkeypatch, tmp_path):
    # The engine image sets FORJA_DATA_DIR=/data; the volume must still win, where the core's
    # S3 transfers look for the store.
    monkeypatch.setenv("FORJA_DATA_DIR", "/data")
    monkeypatch.setattr(runpod, "VOLUME", tmp_path)
    assert runpod.data_dir() == tmp_path / "forja"
    monkeypatch.setattr(runpod, "VOLUME", tmp_path / "none")
    assert runpod.data_dir() == Path("/data")
    monkeypatch.delenv("FORJA_DATA_DIR")
    assert runpod.data_dir() == Path("/data")


class Calls:
    """GPU calls run in threads, as spawned functions run in their own containers."""

    def __init__(self, worker: Worker | None, board: MemoryBoard) -> None:
        self.worker, self.board = worker, board
        self.failed: dict[str, str] = {}
        self.backend = Backend(spawn=self.spawn, failure=lambda c: self.failed.get(c), cancel=self.cancel)
        self.threads: list[None] = []

    def spawn(self, job: dict[str, Any]) -> str:
        call = f"call-{len(self.threads)}"
        self.threads.append(None)
        if self.worker is None:
            return call
        t = threading.Thread(target=serve, args=(self.worker, job, self.board, self.backend))
        t.start()
        return call

    def cancel(self, call: str) -> None:
        self.failed[call] = "cancelled"


def sse_events(client: TestClient, path: str) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    with client.stream("GET", path) as resp:
        ev: dict[str, Any] = {}
        for line in resp.iter_lines():
            if line.startswith("event: "):
                ev["type"] = line[7:]
            elif line.startswith("data: "):
                ev["data"] = json.loads(line[6:])
            elif line == "" and ev:
                out.append(ev)
                ev = {}
    return out


GW_TOKEN = "gw-fake-token-0123"


def test_a_token_with_a_stray_byte_order_mark_is_refused_not_a_crash(tmp_path, engine):
    gw = create_gateway(tmp_path / "store", GW_TOKEN, MemoryBoard(), Calls(None, MemoryBoard()).backend)
    bom = {"Authorization": f"Bearer \ufeff{GW_TOKEN}".encode()}
    assert TestClient(gw).get("/v1/info", headers=bom).status_code == 401
    assert engine.get("/v1/info", headers=bom).status_code == 401


@pytest.mark.parametrize("token", ["", "   ", "short-token"])
def test_the_gateway_refuses_to_start_with_an_empty_or_short_token(tmp_path, token):
    # An empty token would let "Bearer " alone through to paid GPU time.
    with pytest.raises(ValueError, match="at least 16 characters"):
        create_gateway(tmp_path / "store", token, MemoryBoard(), Calls(None, MemoryBoard()).backend)


def test_the_gateway_serves_v1_from_the_volume_and_wakes_a_worker_only_to_run(tmp_path, engine):
    board = MemoryBoard()
    calls = Calls(Worker(ClientEngine(engine)), board)
    gw = create_gateway(tmp_path / "store", GW_TOKEN, board, calls.backend, poll=0.02)
    with TestClient(gw, headers={"Authorization": f"Bearer {GW_TOKEN}"}) as c:
        assert TestClient(gw).get("/v1/info").status_code == 401
        assert c.get("/v1/health").status_code == 200
        assert calls.threads == []  # health and artifacts never wake a GPU worker
        ref = hash_file(FIXTURE)
        assert c.head(f"/v1/artifacts/{ref}").status_code == 404
        assert c.put(f"/v1/artifacts/{ref}", content=b"wrong").status_code == 422
        assert c.put(f"/v1/artifacts/{ref}", content=FIXTURE.read_bytes()).status_code == 201
        assert c.head(f"/v1/artifacts/{ref}").status_code == 200

        info = c.get("/v1/info").json()  # the first description wakes one
        assert info["stages"] and len(calls.threads) == 1

        missing = {"stages": [dict(STAGES[0], inputs=["blake3:" + "0" * 64])]}
        assert c.post("/v1/runs", json=missing).status_code == 409
        req = {"client_run_id": "job-1", "stages": [dict(STAGES[0], inputs=[ref]), STAGES[1]]}
        run = c.post("/v1/runs", json=req).json()
        assert run["status"] == "queued"
        assert c.post("/v1/runs", json=req).json()["id"] == run["id"]  # a retry is the same run
        events = sse_events(c, f"/v1/runs/{run['id']}/events")
        assert events[-1]["type"] == "run.finished" and events[-1]["data"]["status"] == "succeeded"
        final = c.get(f"/v1/runs/{run['id']}").json()
        assert final["id"] == run["id"] and final["status"] == "succeeded"
        frame = final["stages"]["frame"]["outputs"][0]["hash"]
        assert c.get(f"/v1/artifacts/{frame}").content.startswith(b"\x89PNG")
        # A reconnecting reader resumes after the events it has.
        tail = sse_events(c, f"/v1/runs/{run['id']}/events?after={len(events) - 1}")
        assert [e["type"] for e in tail] == ["run.finished"]


def test_the_gateway_ends_the_stream_of_a_run_whose_worker_is_lost(tmp_path):
    board = MemoryBoard()
    calls = Calls(None, board)
    gw = create_gateway(tmp_path / "store", None, board, calls.backend, poll=0.02)
    with TestClient(gw) as c:
        run = c.post("/v1/runs", json={"stages": STAGES[:1]}).json()
        board.put(f"call:{run['id']}", "gone")
        calls.failed["gone"] = "out of memory"
        events = sse_events(c, f"/v1/runs/{run['id']}/events")
        assert events == [
            {
                "type": "run.finished",
                "data": {"status": "failed", "error": "the GPU worker stopped: out of memory"},
            }
        ]

        run = c.post("/v1/runs", json={"stages": STAGES[:1]}).json()
        assert c.delete(f"/v1/runs/{run['id']}").json()["status"] == "cancelled"
        assert sse_events(c, f"/v1/runs/{run['id']}/events")[-1]["data"]["status"] == "cancelled"
