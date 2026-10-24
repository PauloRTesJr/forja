"""The engine's versioned HTTP API, shared by the local core, remote cores and serverless handlers."""

from __future__ import annotations

import asyncio
import hmac
import json
import platform
import time
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from fastapi import Depends, FastAPI, HTTPException, Request, Response
from fastapi.responses import FileResponse, JSONResponse, StreamingResponse
from pydantic import BaseModel

from forja_engine import __version__, checks, families
from forja_engine.adapters.builtin import BUILTINS
from forja_engine.api.version import API_VERSION
from forja_engine.devices import detect
from forja_engine.install import STEPS, Installer, InstallError, Reporter, _run
from forja_engine.registry import Manifest, ManifestError, Registry
from forja_engine.runs import RunManager, RunRequest
from forja_engine.scheduler import Scheduler
from forja_engine.store import ArtifactStore
from forja_engine.tasks import TaskManager, TaskReporter
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
    # Family environments and model weights; default to siblings of the store.
    envs_dir: Path | None = None
    models_dir: Path | None = None
    # Callers allowed to run fixes, which install software on this machine.
    local_hosts: tuple[str, ...] = ("127.0.0.1", "::1", "localhost")

    def envs(self) -> Path:
        return self.envs_dir or self.store_root.parent / "envs"

    def models(self) -> Path:
        return self.models_dir or self.store_root.parent / "models"


# Request bodies live at module level: FastAPI resolves annotations by name, and a class
# defined inside create_app would be read as a query parameter.
class InstallRequest(BaseModel):
    lane: str | None = None
    weights: bool = True


class PairingRequest(BaseModel):
    code: str
    expires_in: float = 600


class PairRequest(BaseModel):
    code: str


def _problem(status: int, title: str, detail: str) -> JSONResponse:
    return JSONResponse(
        {"type": "about:blank", "title": title, "status": status, "detail": detail},
        status_code=status,
        media_type="application/problem+json",
    )


def create_app(cfg: EngineConfig) -> FastAPI:
    store = ArtifactStore(cfg.store_root)
    report = detect(cfg.store_root)

    def load_registry() -> Registry:
        return Registry.load(cfg.manifest_dirs).bind(cfg.envs(), cfg.models())

    registry = load_registry()
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
            # Built-in stage versions; the core puts them in the cache key (model versions are
            # in /v1/models).
            "stages": {str(task): cls.version for task, cls in BUILTINS.items()},
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

    def refresh() -> Registry:
        # Picks up models installed since startup (a few small files), no restart needed.
        nonlocal registry
        registry = runs.registry = load_registry()
        return registry

    tasks = TaskManager()
    app.state.tasks = tasks

    @app.get("/v1/models", dependencies=guarded)
    async def models() -> list[dict[str, Any]]:
        refresh()
        loaded = {m for lane in scheduler.lanes.values() for m in lane.vram.loaded}
        out = []
        for m in registry.models.values():
            try:
                device: str | None = scheduler.place(m)
            except Exception:
                device = None
            status = "loaded" if m.id in loaded else "ready" if m.installed else "not-installed"
            runs_on, fit = scheduler.fit(m)
            busy = tasks.active("install", m.id) or tasks.active("remove", m.id)
            out.append(
                {
                    **m.to_json(),
                    "status": status,
                    "device": device,
                    "runs_on": runs_on,
                    "fit": fit,
                    "task": busy.id if busy else None,
                }
            )
        return out

    def installer(reporter: Reporter) -> Installer:
        return Installer(load_registry(), cfg.envs(), cfg.models(), reporter=reporter)

    def manifests(ids: list[str]) -> list[Manifest]:
        reg = refresh()
        unknown = [i for i in ids if i not in reg.models]
        if unknown:
            raise HTTPException(404, f"unknown model {', '.join(unknown)}")
        return [reg.models[i] for i in ids]

    def runs_active() -> bool:
        return any(not r.done for r in runs.runs.values())

    def local_caller(request: Request) -> None:
        if (request.client.host if request.client else "") not in cfg.local_hosts:
            raise HTTPException(403, "fixes install software, so they run only for callers on this computer")

    def checks_for(ms: list[Manifest]) -> list[checks.Check]:
        return checks.for_models(ms, cfg.models(), families.detect_lane())

    @app.get("/v1/checks", dependencies=guarded)
    async def checks_get(models: str = "") -> dict[str, Any]:
        ids = [m for m in models.split(",") if m] or [m.id for m in refresh().models.values() if not m.stub]
        found = await asyncio.to_thread(checks_for, manifests(ids))
        return {
            "lane": families.detect_lane(),
            "ok": all(c.ok for c in found),
            "checks": [c.to_json() for c in found],
        }

    @app.post("/v1/checks/{check_id}/fix", dependencies=[*guarded, Depends(local_caller)], status_code=202)
    async def check_fix(check_id: str) -> dict[str, Any]:
        known = [m for m in refresh().models.values() if not m.stub]
        check = next((c for c in await asyncio.to_thread(checks_for, known) if c.id == check_id), None)
        if check is None:
            raise HTTPException(404, f"no check {check_id}")
        if check.ok:
            raise HTTPException(409, f"{check.label} already passes")
        if check.fix is None or not check.fix.command:
            raise HTTPException(409, f"{check.label} cannot be fixed from here; follow the steps")
        if (running := tasks.active("fix", check_id)) is not None:
            return running.to_json()
        command: list[str | Path] = list(check.fix.command)

        def work(r: TaskReporter) -> dict[str, Any]:
            r.step("run", "running")
            try:
                _run(command, r.log, cancel=r.cancel)
            except BaseException:
                r.step("run", "failed")
                raise
            r.step("run", "done")
            r.step("verify", "running")
            again = next((c for c in checks_for(known) if c.id == check_id), None)
            if again is not None and not again.ok:
                r.step("verify", "failed")
                raise InstallError(
                    f"{again.label} still does not pass: found {again.found}, need {again.need}"
                )
            r.step("verify", "done")
            return {"check": again.to_json() if again else None}

        steps = (("run", check.fix.label), ("verify", "Check again"))
        return tasks.start("fix", check_id, steps, work, serial=False).to_json()

    @app.post("/v1/models/{model_id}/install", dependencies=guarded, status_code=202)
    async def model_install(model_id: str, req: InstallRequest | None = None) -> dict[str, Any]:
        m = manifests([model_id])[0]
        if (running := tasks.active("install", model_id)) is not None:
            return running.to_json()
        if m.installed and runs_active():
            raise HTTPException(409, f"{m.name} is in use; repair it when no job is running")
        opts = req or InstallRequest()

        def work(r: TaskReporter) -> None:
            installer(r).install(model_id, lane=opts.lane, weights=opts.weights)

        task = tasks.start(
            "install",
            model_id,
            STEPS,
            work,
            prepare=lambda: scheduler.release(m.family),
            done=lambda _: refresh(),
        )
        return task.to_json()

    @app.delete("/v1/models/{model_id}", dependencies=guarded, status_code=202)
    async def model_remove(model_id: str) -> dict[str, Any]:
        m = manifests([model_id])[0]
        if m.in_engine_env or m.python is not None:
            raise HTTPException(409, f"{m.name} is part of the engine; there is nothing to remove")
        if tasks.active("install", model_id) is not None:
            raise HTTPException(409, f"{m.name} is being installed; cancel that first")
        if runs_active():
            raise HTTPException(409, f"{m.name} may be in use; remove it when no job is running")

        def work(r: TaskReporter) -> None:
            r.step("remove", "running")
            installer(r).remove(model_id)
            r.step("remove", "done")

        task = tasks.start(
            "remove",
            model_id,
            (("remove", "Remove files"),),
            work,
            prepare=lambda: scheduler.release(m.family),
            done=lambda _: refresh(),
        )
        return task.to_json()

    # Pairing: the core on this machine sets a short code; another computer trades it, once,
    # for the token. Few attempts per code, so guessing six digits does not pay.
    pairing: dict[str, Any] = {"code": None, "expires": 0.0, "attempts": 0}

    @app.post("/v1/pairing", dependencies=[*guarded, Depends(local_caller)], status_code=204)
    async def pairing_set(req: PairingRequest) -> None:
        if not (req.code.isdigit() and len(req.code) >= 6):
            raise HTTPException(422, "a pairing code is at least six digits")
        pairing.update(code=req.code, expires=time.monotonic() + req.expires_in, attempts=0)

    @app.post("/v1/pair")
    async def pair(req: PairRequest) -> dict[str, Any]:
        if cfg.token is None or pairing["code"] is None or time.monotonic() > pairing["expires"]:
            raise HTTPException(410, "no pairing code is active: show one on the other computer")
        pairing["attempts"] += 1
        if pairing["attempts"] > 5:
            pairing["code"] = None
            raise HTTPException(429, "too many wrong codes: show a new one on the other computer")
        if not hmac.compare_digest(req.code.encode(), str(pairing["code"]).encode()):
            raise HTTPException(403, "wrong pairing code")
        pairing["code"] = None
        return {"token": cfg.token, "name": platform.node(), "version": __version__}

    @app.get("/v1/tasks", dependencies=guarded)
    async def tasks_list() -> list[dict[str, Any]]:
        return [t.to_json() for t in sorted(tasks.tasks.values(), key=lambda t: -t.created_at)]

    def task_or_404(task_id: str) -> Any:
        if task_id not in tasks.tasks:
            raise HTTPException(404, f"no task {task_id}")
        return tasks.tasks[task_id]

    @app.get("/v1/tasks/{task_id}", dependencies=guarded)
    async def task_get(task_id: str) -> dict[str, Any]:
        return task_or_404(task_id).to_json(log_lines=200)  # type: ignore[no-any-return]

    @app.delete("/v1/tasks/{task_id}", dependencies=guarded)
    async def task_cancel(task_id: str) -> dict[str, Any]:
        task_or_404(task_id)
        return tasks.cancel(task_id).to_json()  # type: ignore[union-attr]

    @app.get("/v1/tasks/{task_id}/events", dependencies=guarded)
    async def task_events(task_id: str, request: Request) -> StreamingResponse:
        task = task_or_404(task_id)
        after = int(request.headers.get("last-event-id", request.query_params.get("after", "0")))

        async def stream() -> AsyncIterator[bytes]:
            seen = after
            while True:
                seen = await task.wait(seen)
                yield f"id: {seen}\nevent: task\ndata: {json.dumps(task.to_json())}\n\n".encode()
                if task.done:
                    return
                await asyncio.sleep(0.25)  # at most four updates a second

        return StreamingResponse(
            stream(), media_type="text/event-stream", headers={"Cache-Control": "no-cache"}
        )

    @app.post("/v1/models/unload-all", dependencies=guarded, status_code=204)
    async def unload_all() -> None:
        await scheduler.unload_all()

    # A core that finds an artifact here skips uploading it and soon runs on it: the asking
    # marks it used, so eviction on this machine leaves it alone meanwhile.
    @app.head("/v1/artifacts/{ref}", dependencies=guarded)
    async def artifact_exists(ref: str) -> Response:
        if not store.has(ref):
            return Response(status_code=404)
        store.touch(ref)
        return Response(status_code=200)

    @app.get("/v1/artifacts/{ref}", dependencies=guarded)
    async def artifact_get(ref: str) -> FileResponse:
        if not store.has(ref):
            raise HTTPException(404, f"no artifact {ref}")
        store.touch(ref)
        return FileResponse(store.path(ref), media_type="application/octet-stream")

    @app.put("/v1/artifacts/{ref}", dependencies=guarded, status_code=201)
    async def artifact_put(ref: str, request: Request) -> dict[str, str]:
        if store.has(ref):
            store.touch(ref)
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
        refresh()
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
