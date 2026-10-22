"""The engine's versioned HTTP API, shared by the local core, remote cores and serverless handlers."""

from __future__ import annotations

import asyncio
import hmac
import json
import time
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from fastapi import Depends, FastAPI, HTTPException, Request, Response
from fastapi.responses import FileResponse, JSONResponse, StreamingResponse

from forja_engine import __version__
from forja_engine.api.version import API_VERSION
from forja_engine.devices import detect
from forja_engine.registry import ManifestError, Registry
from forja_engine.runs import RunManager, RunRequest
from forja_engine.scheduler import Scheduler
from forja_engine.store import ArtifactStore
from forja_engine.vram import DoesNotFit, FitMode

GB = 1 << 30


@dataclass
class EngineConfig:
    store_root: Path
    token: str | None = None
    fit_mode: FitMode = FitMode.FIT_FIRST
    vram_reserve_bytes: int = 1 * GB
    idle_unload_seconds: float = 15 * 60
    fairness_cap: int = 8
    max_upload_bytes: int = 2 * GB
    manifest_dirs: list[Path] = field(default_factory=list)
    in_process_models: bool = False


def _problem(status: int, title: str, detail: str) -> JSONResponse:
    return JSONResponse(
        {"type": "about:blank", "title": title, "status": status, "detail": detail},
        status_code=status,
        media_type="application/problem+json",
    )


def create_app(cfg: EngineConfig) -> FastAPI:
    store = ArtifactStore(cfg.store_root)
    report = detect(cfg.store_root)
    registry = Registry.load(cfg.manifest_dirs)
    budgets = {d.id: max(0, d.vram_bytes - cfg.vram_reserve_bytes) for d in report.devices}
    scheduler = Scheduler(
        budgets, fit_mode=cfg.fit_mode, fairness_cap=cfg.fairness_cap, in_process=cfg.in_process_models
    )
    runs = RunManager(store, registry, scheduler)
    last_activity = [time.monotonic()]

    async def idle_unloader() -> None:
        # Frees VRAM for games and Unity when nothing has run for a while.
        while True:
            await asyncio.sleep(min(30.0, cfg.idle_unload_seconds))
            busy = any(not r.done for r in runs.runs.values())
            if not busy and time.monotonic() - last_activity[0] >= cfg.idle_unload_seconds:
                await scheduler.unload_all()

    @asynccontextmanager
    async def lifespan(_: FastAPI) -> AsyncIterator[None]:
        scheduler.start()
        idle = asyncio.create_task(idle_unloader())
        yield
        idle.cancel()
        await scheduler.stop()

    app = FastAPI(title="forja-engine", version=API_VERSION, lifespan=lifespan)
    app.state.runs = runs
    app.state.scheduler = scheduler

    def auth(request: Request) -> None:
        if cfg.token is None:
            return
        header = request.headers.get("authorization", "")
        if not hmac.compare_digest(header, f"Bearer {cfg.token}"):
            raise HTTPException(401, "missing or wrong bearer token")

    @app.exception_handler(HTTPException)
    async def http_problem(_: Request, exc: HTTPException) -> JSONResponse:
        return _problem(exc.status_code, "Request failed", str(exc.detail))

    @app.exception_handler(ManifestError)
    async def manifest_problem(_: Request, exc: ManifestError) -> JSONResponse:
        return _problem(422, "Unknown or invalid model", str(exc))

    @app.exception_handler(DoesNotFit)
    async def fit_problem(_: Request, exc: DoesNotFit) -> JSONResponse:
        return _problem(507, "Model does not fit", str(exc))

    @app.get("/v1/health")
    async def health() -> dict[str, str]:
        return {"status": "ok"}

    guarded = [Depends(auth)]

    @app.get("/v1/info", dependencies=guarded)
    async def info() -> dict[str, Any]:
        return {
            "version": __version__,
            "api_version": API_VERSION,
            "system": detect(cfg.store_root).to_json(),
            "fit_mode": cfg.fit_mode,
            "lanes": {
                dev: {
                    "budget_bytes": lane.vram.budget,
                    "loaded": list(lane.vram.loaded),
                    "queued": len(lane.ready),
                    "stats": lane.stats.__dict__,
                }
                for dev, lane in scheduler.lanes.items()
            },
        }

    @app.get("/v1/models", dependencies=guarded)
    async def models() -> list[dict[str, Any]]:
        loaded = {m for lane in scheduler.lanes.values() for m in lane.vram.loaded}
        out = []
        for m in registry.models.values():
            try:
                device: str | None = scheduler.place(m)
            except Exception:
                device = None
            out.append({**m.to_json(), "status": "loaded" if m.id in loaded else "ready", "device": device})
        return out

    @app.post("/v1/models/unload-all", dependencies=guarded, status_code=204)
    async def unload_all() -> None:
        await scheduler.unload_all()

    @app.head("/v1/artifacts/{ref}", dependencies=guarded)
    async def artifact_exists(ref: str) -> Response:
        return Response(status_code=200 if store.has(ref) else 404)

    @app.get("/v1/artifacts/{ref}", dependencies=guarded)
    async def artifact_get(ref: str) -> FileResponse:
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
                if size > cfg.max_upload_bytes:
                    tmp.unlink(missing_ok=True)
                    raise HTTPException(413, "upload too large")
                f.write(chunk)
        try:
            return {"hash": store.put_verified(ref, tmp)}
        except ValueError as e:
            raise HTTPException(422, str(e)) from e

    @app.post("/v1/runs", dependencies=guarded, status_code=202)
    async def run_create(req: RunRequest) -> dict[str, Any]:
        last_activity[0] = time.monotonic()
        try:
            run = runs.submit(req)
        except KeyError as e:
            raise HTTPException(409, str(e.args[0])) from e
        return run.to_json()

    @app.get("/v1/runs/{run_id}", dependencies=guarded)
    async def run_get(run_id: str) -> dict[str, Any]:
        if run_id not in runs.runs:
            raise HTTPException(404, f"no run {run_id}")
        return runs.runs[run_id].to_json()

    @app.delete("/v1/runs/{run_id}", dependencies=guarded)
    async def run_cancel(run_id: str) -> dict[str, Any]:
        if run_id not in runs.runs:
            raise HTTPException(404, f"no run {run_id}")
        return (await runs.cancel(run_id)).to_json()

    @app.get("/v1/runs/{run_id}/events", dependencies=guarded)
    async def run_events(run_id: str, request: Request) -> StreamingResponse:
        if run_id not in runs.runs:
            raise HTTPException(404, f"no run {run_id}")
        run = runs.runs[run_id]
        after = int(request.headers.get("last-event-id", request.query_params.get("after", "0")))

        async def stream() -> AsyncIterator[bytes]:
            cursor = after
            while True:
                events = await run.wait_events(cursor)
                for ev in events:
                    cursor = ev.id
                    yield f"id: {ev.id}\nevent: {ev.type}\ndata: {json.dumps(ev.data)}\n\n".encode()
                    last_activity[0] = time.monotonic()
                if run.done and cursor >= len(run.events):
                    return

        return StreamingResponse(
            stream(), media_type="text/event-stream", headers={"Cache-Control": "no-cache"}
        )

    return app
