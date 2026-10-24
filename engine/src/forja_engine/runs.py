"""Runs: a stage graph handed over by the core, executed once, then forgotten.

The engine does not memoize. The core sends only stages it has no cached output for and
passes earlier outputs in as `blake3:` refs.
"""

from __future__ import annotations

import asyncio
import contextlib
import shutil
import time
import uuid
from collections import OrderedDict
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel, Field, model_validator

from forja_engine.registry import Registry
from forja_engine.scheduler import BATCH, INTERACTIVE, Scheduler
from forja_engine.sdk import VIEWS, ArtifactKind, StageContext, StageInput, StageType
from forja_engine.store import PREFIX, ArtifactStore
from forja_engine.workers import StageFailed

STAGE_REF = "stage:"
_KEEP_FINISHED = 200


class StageSpec(BaseModel):
    id: str = Field(pattern=r"^[a-z0-9][a-z0-9_-]*$")
    use: StageType
    model: str | None = None
    params: dict[str, Any] = Field(default_factory=dict)
    # Each input is "stage:<id>" (that stage's primary output) or "blake3:<hex>".
    inputs: list[str] = Field(default_factory=list)
    # The view each input shows ("" for none), when the stage reads several views (API 1.6).
    views: list[str] | None = None

    @model_validator(mode="after")
    def _check_views(self) -> StageSpec:
        if self.views is None:
            return self
        if len(self.views) != len(self.inputs):
            raise ValueError(f"stage {self.id}: views must label each input")
        for v in self.views:
            if v and v not in VIEWS:
                raise ValueError(f"stage {self.id}: unknown view {v!r}")
        return self


class RunRequest(BaseModel):
    client_run_id: str | None = None
    priority: Literal["interactive", "batch"] = "batch"
    seed: int = 42
    stages: list[StageSpec] = Field(min_length=1)

    @model_validator(mode="after")
    def _check_graph(self) -> RunRequest:
        ids = [s.id for s in self.stages]
        if len(set(ids)) != len(ids):
            raise ValueError("stage ids must be unique")
        seen: set[str] = set()
        for s in self.stages:
            for ref in s.inputs:
                if ref.startswith(STAGE_REF) and ref[len(STAGE_REF) :] not in seen:
                    raise ValueError(f"stage {s.id} reads {ref}, which is not an earlier stage")
                if not ref.startswith((STAGE_REF, PREFIX)):
                    raise ValueError(f"stage {s.id}: bad input ref {ref!r}")
            seen.add(s.id)
        return self


class Status(StrEnum):
    QUEUED = "queued"
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    CANCELLED = "cancelled"


@dataclass
class StageState:
    status: Status = Status.QUEUED
    outputs: list[dict[str, Any]] = field(default_factory=list)
    seconds: float | None = None
    error: str | None = None
    step: int = 0
    total: int = 0
    peak_vram_bytes: int | None = None


@dataclass
class Event:
    id: int
    type: str
    data: dict[str, Any]


class Run:
    def __init__(self, run_id: str, req: RunRequest) -> None:
        self.id = run_id
        self.req = req
        self.status = Status.QUEUED
        self.stages = {s.id: StageState() for s in req.stages}
        self.events: list[Event] = []
        self.created = time.time()
        self.error: str | None = None
        self._changed = asyncio.Condition()
        self._outputs: dict[str, asyncio.Future[str]] = {}
        self.task: asyncio.Task[None] | None = None
        self.pending: set[asyncio.Task[None]] = set()

    async def emit(self, type_: str, **data: Any) -> None:
        async with self._changed:
            self.events.append(Event(len(self.events) + 1, type_, data))
            self._changed.notify_all()

    async def wait_events(self, after: int) -> list[Event]:
        async with self._changed:
            await self._changed.wait_for(lambda: len(self.events) > after or self.done)
            return self.events[after:]

    @property
    def done(self) -> bool:
        return self.status in (Status.SUCCEEDED, Status.FAILED, Status.CANCELLED)

    def to_json(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "client_run_id": self.req.client_run_id,
            "status": self.status,
            "error": self.error,
            "stages": {
                sid: {
                    "status": st.status,
                    "outputs": st.outputs,
                    "seconds": st.seconds,
                    "error": st.error,
                    "step": st.step,
                    "total": st.total,
                    "peak_vram_bytes": st.peak_vram_bytes,
                }
                for sid, st in self.stages.items()
            },
        }


class RunManager:
    def __init__(self, store: ArtifactStore, registry: Registry, scheduler: Scheduler) -> None:
        self.store = store
        self.registry = registry
        self.scheduler = scheduler
        self.runs: OrderedDict[str, Run] = OrderedDict()
        self._by_client_id: dict[str, str] = {}

    def submit(self, req: RunRequest) -> Run:
        if req.client_run_id and req.client_run_id in self._by_client_id:
            return self.runs[self._by_client_id[req.client_run_id]]  # idempotent retry
        for spec in req.stages:
            self.registry.resolve(spec.use, spec.model)  # fail fast on unknown models
            for ref in spec.inputs:
                if ref.startswith(PREFIX) and not self.store.has(ref):
                    raise KeyError(f"artifact {ref} is not on this engine; upload it first")
        run = Run(uuid.uuid4().hex, req)
        self.runs[run.id] = run
        if req.client_run_id:
            self._by_client_id[req.client_run_id] = run.id
        run.task = asyncio.create_task(self._execute(run))
        self._trim()
        return run

    async def cancel(self, run_id: str) -> Run:
        run = self.runs[run_id]
        if run.done:
            return run
        run.status = Status.CANCELLED
        for spec in run.req.stages:
            await self.scheduler.cancel(f"{run.id}/{spec.id}")
        if run.task:
            run.task.cancel()
        return run

    def _trim(self) -> None:
        finished = [r for r in self.runs.values() if r.done]
        for r in finished[: max(0, len(finished) - _KEEP_FINISHED)]:
            self.runs.pop(r.id, None)
            if r.req.client_run_id:
                self._by_client_id.pop(r.req.client_run_id, None)

    async def _execute(self, run: Run) -> None:
        loop = asyncio.get_running_loop()
        run._outputs = {s.id: loop.create_future() for s in run.req.stages}
        run.status = Status.RUNNING
        await run.emit("run.started", run=run.id)
        tasks = [asyncio.create_task(self._stage(run, s)) for s in run.req.stages]
        try:
            await asyncio.gather(*tasks)
            run.status = Status.SUCCEEDED
        except asyncio.CancelledError:
            run.status = Status.CANCELLED
        except Exception as e:
            run.status = Status.CANCELLED if isinstance(e, StageFailed) and e.cancelled else Status.FAILED
            run.error = str(e)
        finally:
            for t in tasks:
                t.cancel()
            for fut in run._outputs.values():
                if not fut.done():
                    fut.cancel()
            with contextlib.suppress(Exception):
                await run.emit("run.finished", status=run.status, error=run.error)

    async def _stage(self, run: Run, spec: StageSpec) -> None:
        state = run.stages[spec.id]
        refs = [
            await run._outputs[r[len(STAGE_REF) :]] if r.startswith(STAGE_REF) else r for r in spec.inputs
        ]
        manifest = self.registry.resolve(spec.use, spec.model)
        params = {**_defaults(manifest.params if manifest else {}), **spec.params}
        out_dir = self.store.scratch_dir()
        views = spec.views or [""] * len(refs)
        ctx = StageContext(
            stage_id=spec.id,
            task=spec.use,
            inputs=[
                StageInput(self.store.path(r), _kind_of(self.store.path(r)), r, v or None)
                for r, v in zip(refs, views, strict=True)
            ],
            params=params,
            seed=int(params.get("seed", run.req.seed)),
            out_dir=out_dir,
        )
        loop = asyncio.get_running_loop()

        def on_progress(step: int, total: int) -> None:
            state.step, state.total = step, total

            # May be called from an adapter thread; hop onto the loop and keep a strong ref.
            def spawn() -> None:
                task = loop.create_task(run.emit("stage.progress", stage=spec.id, step=step, total=total))
                run.pending.add(task)
                task.add_done_callback(run.pending.discard)

            loop.call_soon_threadsafe(spawn)

        state.status = Status.RUNNING
        device = self.scheduler.place(manifest) if manifest else "cpu-pool"
        await run.emit("stage.started", stage=spec.id, model=spec.model, device=device)
        priority = INTERACTIVE if run.req.priority == "interactive" else BATCH
        try:
            result = await self.scheduler.submit(f"{run.id}/{spec.id}", ctx, manifest, priority, on_progress)
            for out in result["outputs"]:
                ref = self.store.put_file(Path(out["path"]))
                entry = {"hash": ref, "kind": out["kind"], "preview": out["preview"]}
                state.outputs.append(entry)
                await run.emit("artifact.ready", stage=spec.id, **entry)
            state.seconds = result["seconds"]
            state.peak_vram_bytes = result.get("peak_vram_bytes")
            state.status = Status.SUCCEEDED
            await run.emit(
                "stage.finished",
                stage=spec.id,
                seconds=round(state.seconds, 3),
                peak_vram_bytes=state.peak_vram_bytes,
            )
            run._outputs[spec.id].set_result(state.outputs[0]["hash"])
        except BaseException as e:
            cancelled = isinstance(e, asyncio.CancelledError) or (isinstance(e, StageFailed) and e.cancelled)
            state.status = Status.CANCELLED if cancelled else Status.FAILED
            tail: list[str] = getattr(e, "tail", [])  # a crashed worker's last stderr lines
            state.error = str(e) + ("\n" + "\n".join(tail[-20:]) if tail else "")
            with contextlib.suppress(Exception):
                await run.emit("stage.failed", stage=spec.id, error=state.error, cancelled=cancelled)
            raise
        finally:
            shutil.rmtree(out_dir, ignore_errors=True)


def _defaults(schema: dict[str, Any]) -> dict[str, Any]:
    return {k: v["default"] for k, v in schema.items() if isinstance(v, dict) and "default" in v}


def _kind_of(path: Path) -> ArtifactKind:
    with path.open("rb") as f:
        head = f.read(8)
    if head.startswith(b"\x89PNG"):
        return ArtifactKind.IMAGE_PNG
    if head.startswith(b"glTF"):
        return ArtifactKind.MESH_GLB
    if head[:1] in (b"{", b"["):
        return ArtifactKind.JSON
    # JPEG, WebP and other raw uploads are images; framing normalizes them to PNG.
    return ArtifactKind.IMAGE_PNG
