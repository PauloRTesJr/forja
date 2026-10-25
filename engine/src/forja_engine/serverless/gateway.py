"""The engine's /v1 API in front of GPU workers that scale to zero (Modal).

The gateway runs on a small CPU container: it answers health, artifacts and run state itself,
from a volume and a key-value board the GPU workers share, and wakes a GPU worker only to run
a stage graph. A core reaches it like any remote engine, so artifacts move by hash, once
(HEAD before PUT, GET only what it lacks), and checking health costs no GPU time.

Board keys: `describe` (the last worker's /v1/info and /v1/models), `run:<id>` (a run's
state), `ev:<id>:<n>` (its events, numbered from 1), `call:<id>` (the worker call running it)
and `client:<client run id>` (idempotent retries).
"""

from __future__ import annotations

import hmac
import json
import time
import uuid
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Protocol

from fastapi import Depends, FastAPI, HTTPException, Request, Response
from fastapi.responses import FileResponse, JSONResponse, StreamingResponse

from forja_engine.api.version import API_VERSION
from forja_engine.runs import RunRequest
from forja_engine.serverless.worker import Worker
from forja_engine.store import PREFIX, ArtifactStore

_DONE = ("succeeded", "failed", "cancelled")

# The shortest token the gateway serves with: it stands between the internet and paid GPU time.
MIN_TOKEN = 16


class Board(Protocol):
    def get(self, key: str) -> Any: ...

    def put(self, key: str, value: Any) -> None: ...


@dataclass
class Backend:
    """How the gateway reaches GPU workers, and keeps its view of the shared volume fresh."""

    # Starts a job on a GPU worker; returns the call's id.
    spawn: Callable[[dict[str, Any]], str]
    # Why a call ended without finishing its run (it crashed, timed out), or None.
    failure: Callable[[str], str | None]
    cancel: Callable[[str], None]
    # Sees what other containers wrote to the volume; publishes what this one wrote.
    reload: Callable[[], None] = field(default=lambda: None)
    commit: Callable[[], None] = field(default=lambda: None)


def _problem(status: int, detail: str) -> JSONResponse:
    return JSONResponse(
        {"type": "about:blank", "title": "Request failed", "status": status, "detail": detail},
        status_code=status,
        media_type="application/problem+json",
    )


def _queued(run_id: str, req: RunRequest) -> dict[str, Any]:
    stage: dict[str, Any] = {"status": "queued", "outputs": [], "seconds": None, "error": None}
    stage |= {"step": 0, "total": 0, "peak_vram_bytes": None}
    return {
        "id": run_id,
        "client_run_id": req.client_run_id,
        "status": "queued",
        "error": None,
        "stages": {s.id: dict(stage) for s in req.stages},
    }


def create_gateway(
    store_root: Path,
    token: str | None,
    board: Board,
    backend: Backend,
    *,
    window: float = 100.0,
    poll: float = 0.25,
    describe_wait: float = 140.0,
    max_upload: int = 2 << 30,
) -> FastAPI:
    """window: how long one event stream stays open (vendors cut long requests; cores resume).

    token None serves without one, for tests and trying the gateway on this computer; an empty
    or short one is refused, since `Bearer ` alone would otherwise pass."""
    if token is not None and len(token.strip()) < MIN_TOKEN:
        raise ValueError(
            f"FORJA_ENGINE_TOKEN must be at least {MIN_TOKEN} characters; "
            "set a long random one in the forja-engine Modal secret"
        )
    store = ArtifactStore(store_root)
    app = FastAPI(title="forja-engine gateway", version=API_VERSION)

    def auth(request: Request) -> None:
        if token is None:
            return
        given = request.headers.get("authorization", "").encode()
        if not hmac.compare_digest(given, f"Bearer {token}".encode()):
            raise HTTPException(401, "missing or wrong bearer token")

    @app.exception_handler(HTTPException)
    async def problem(_: Request, exc: HTTPException) -> JSONResponse:
        return _problem(exc.status_code, str(exc.detail))

    guarded = [Depends(auth)]

    @app.get("/v1/health")
    def health() -> dict[str, str]:
        return {"status": "ok"}

    def describe() -> dict[str, Any]:
        found = board.get("describe")
        if found is None:
            backend.spawn({"op": "describe"})
            deadline = time.monotonic() + describe_wait
            while found is None and time.monotonic() < deadline:
                time.sleep(poll)
                found = board.get("describe")
        if found is None:
            raise HTTPException(503, "the GPU worker is still starting; try again in a minute")
        return found  # type: ignore[no-any-return]

    @app.get("/v1/info", dependencies=guarded)
    def info() -> dict[str, Any]:
        return describe()["info"]  # type: ignore[no-any-return]

    @app.get("/v1/models", dependencies=guarded)
    def models() -> list[dict[str, Any]]:
        return describe()["models"]  # type: ignore[no-any-return]

    @app.head("/v1/artifacts/{ref}", dependencies=guarded)
    def artifact_exists(ref: str) -> Response:
        backend.reload()
        return Response(status_code=200 if store.has(ref) else 404)

    @app.get("/v1/artifacts/{ref}", dependencies=guarded)
    def artifact_get(ref: str) -> FileResponse:
        backend.reload()
        if not store.has(ref):
            raise HTTPException(404, f"no artifact {ref}")
        return FileResponse(store.path(ref), media_type="application/octet-stream")

    @app.put("/v1/artifacts/{ref}", dependencies=guarded, status_code=201)
    async def artifact_put(ref: str, request: Request) -> dict[str, str]:
        if store.has(ref):
            return {"hash": ref}
        tmp = store.new_upload()
        size = 0
        with tmp.open("wb") as f:
            async for chunk in request.stream():
                size += len(chunk)
                if size > max_upload:
                    break
                f.write(chunk)
        if size > max_upload:
            tmp.unlink(missing_ok=True)
            raise HTTPException(413, "upload too large")
        try:
            stored = store.put_verified(ref, tmp)
        except ValueError as e:
            raise HTTPException(422, str(e)) from e
        backend.commit()
        return {"hash": stored}

    def run_or_404(run_id: str) -> dict[str, Any]:
        run = board.get(f"run:{run_id}")
        if run is None:
            raise HTTPException(404, f"no run {run_id}")
        return run  # type: ignore[no-any-return]

    @app.post("/v1/runs", dependencies=guarded, status_code=202)
    def run_create(req: RunRequest) -> dict[str, Any]:
        if req.client_run_id and (known := board.get(f"client:{req.client_run_id}")):
            return run_or_404(known)  # a retried submit
        backend.reload()
        for s in req.stages:
            for ref in s.inputs:
                if ref.startswith(PREFIX) and not store.has(ref):
                    raise HTTPException(409, f"artifact {ref} is not on this engine; upload it first")
        run_id = uuid.uuid4().hex
        run = _queued(run_id, req)
        board.put(f"run:{run_id}", run)
        if req.client_run_id:
            board.put(f"client:{req.client_run_id}", run_id)
        job = {"op": "run", "id": run_id, "run": req.model_dump(mode="json", exclude_none=True)}
        board.put(f"call:{run_id}", backend.spawn(job))
        return run

    @app.get("/v1/runs/{run_id}", dependencies=guarded)
    def run_get(run_id: str) -> dict[str, Any]:
        return run_or_404(run_id)

    @app.delete("/v1/runs/{run_id}", dependencies=guarded)
    def run_cancel(run_id: str) -> dict[str, Any]:
        run = run_or_404(run_id)
        if run["status"] in _DONE:
            return run
        if call := board.get(f"call:{run_id}"):
            backend.cancel(call)
        run = {**run, "status": "cancelled", "error": "cancelled"}
        board.put(f"run:{run_id}", run)
        return run

    def ended(run_id: str) -> dict[str, Any] | None:
        """The run's state once it can no longer send events: finished, cancelled, or lost."""
        run = run_or_404(run_id)
        if run["status"] in _DONE:
            return run
        call = board.get(f"call:{run_id}")
        why = backend.failure(call) if call else None
        if why is None:
            return None
        run = {**run, "status": "failed", "error": f"the GPU worker stopped: {why}"}
        board.put(f"run:{run_id}", run)
        return run

    @app.get("/v1/runs/{run_id}/events", dependencies=guarded)
    def run_events(run_id: str, request: Request) -> StreamingResponse:
        run_or_404(run_id)
        after = int(request.headers.get("last-event-id", request.query_params.get("after", "0")))

        def stream() -> Iterator[bytes]:
            cursor, close_at, quiet_end = after, time.monotonic() + window, False
            while time.monotonic() < close_at:
                ev = board.get(f"ev:{run_id}:{cursor + 1}")
                if ev is not None:
                    cursor = ev["id"]
                    yield _sse(ev)
                    if ev["type"] == "run.finished":
                        return
                    continue
                run = ended(run_id)
                if run is not None:
                    # A worker writes the state before the last event; wait one beat for it,
                    # then end the stream for a worker that never will.
                    if quiet_end:
                        ev = {"id": cursor + 1, "type": "run.finished", "data": {"status": run["status"]}}
                        ev["data"]["error"] = run["error"]
                        yield _sse(ev)
                        return
                    quiet_end = True
                time.sleep(poll)

        return StreamingResponse(
            stream(), media_type="text/event-stream", headers={"Cache-Control": "no-cache"}
        )

    return app


def _sse(ev: dict[str, Any]) -> bytes:
    return f"id: {ev['id']}\nevent: {ev['type']}\ndata: {json.dumps(ev['data'])}\n\n".encode()


def serve(worker: Worker, job: dict[str, Any], board: Board, backend: Backend) -> None:
    """Runs one gateway job on a GPU worker, publishing its events and state on the board."""
    if job["op"] == "describe":
        board.put("describe", worker.describe())
        return
    run_id = job["id"]
    backend.reload()  # inputs the gateway stored a moment ago
    last: dict[str, Any] | None = None
    for item in worker.handle(job):
        if "event" in item:
            ev = item["event"]
            if ev["type"] == "run.finished":
                last = ev  # after the state, so a reader that sees it can read the outputs
                continue
            board.put(f"ev:{run_id}:{ev['id']}", ev)
            continue
        result = item["result"]
        backend.commit()
        if "missing" in result:
            state = {**board.get(f"run:{run_id}"), "status": "failed"}
            state["error"] = "artifacts missing on the volume: " + ", ".join(result["missing"])
            board.put(f"run:{run_id}", state)
            continue
        board.put(f"run:{run_id}", result["run"])
        board.put("describe", {"info": result["info"], "models": result["models"]})
    if last is not None:
        board.put(f"ev:{run_id}:{last['id']}", last)


class MemoryBoard:
    """A board in one process: for tests and for trying the gateway locally."""

    def __init__(self) -> None:
        self.items: dict[str, Any] = {}

    def get(self, key: str) -> Any:
        return self.items.get(key)

    def put(self, key: str, value: Any) -> None:
        self.items[key] = value
