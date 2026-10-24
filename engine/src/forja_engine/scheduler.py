"""Keeps every device busy with useful work.

- Model stages go to one serial lane per device. Among ready stages the lane prefers
  interactive work, then the model already loaded (affinity), capped by a fairness limit.
- Built-in CPU stages go to a process pool, so one job's UV unwrap overlaps the next job's
  shape stage and the GIL never stalls the API.
"""

from __future__ import annotations

import asyncio
import itertools
import logging
import os
import time
from collections.abc import Callable
from concurrent.futures import ProcessPoolExecutor
from dataclasses import dataclass, field
from typing import Any

from forja_engine import gpumem
from forja_engine.adapters.builtin import BUILTINS
from forja_engine.registry import Manifest, instantiate
from forja_engine.sdk import StageContext, StageType
from forja_engine.vram import FitMode, VramManager
from forja_engine.worker import ctx_from_json, ctx_to_json
from forja_engine.workers import StageFailed, WorkerCrashed, WorkerPool

log = logging.getLogger(__name__)

INTERACTIVE, BATCH = 0, 1
GB = 1 << 30


@dataclass
class WorkItem:
    key: str  # "<run id>/<stage id>"
    ctx: StageContext
    manifest: Manifest | None
    priority: int
    on_progress: Callable[[int, int], None]
    seq: int = 0
    future: asyncio.Future[dict[str, Any]] = field(
        default_factory=lambda: asyncio.get_running_loop().create_future()
    )

    @property
    def model(self) -> str | None:
        return self.manifest.id if self.manifest else None


@dataclass
class LaneStats:
    busy_seconds: float = 0.0
    stages: int = 0
    swaps: int = 0


def _run_builtin(task: str, ctx_json: dict[str, Any]) -> dict[str, Any]:
    ctx = ctx_from_json(ctx_json)
    started = time.perf_counter()
    outputs = BUILTINS[StageType(task)]().run(ctx)
    return {
        "type": "result",
        "seconds": time.perf_counter() - started,
        "outputs": [{"path": str(o.path), "kind": str(o.kind), "preview": o.preview} for o in outputs],
    }


def _weights(m: Manifest) -> str | None:
    return str(m.weights_dir) if m.weights_dir else None


class DeviceLane:
    def __init__(
        self,
        device: str,
        vram: VramManager,
        workers: WorkerPool,
        fairness_cap: int,
        in_process: bool,
        used: Callable[[], int] | None = None,
    ) -> None:
        """`used` reads the device's bytes in use; None where it cannot be measured."""
        self.device = device
        self.used = used
        self.vram = vram
        self.workers = workers
        self.fairness_cap = fairness_cap
        self.in_process = in_process
        self.ready: list[WorkItem] = []
        self.current_model: str | None = None
        self.streak = 0
        self.running: WorkItem | None = None
        self.stats = LaneStats()
        self._wake = asyncio.Event()
        self._task: asyncio.Task[None] | None = None
        self._in_process_adapters: dict[str, Any] = {}
        self._loaded: dict[str, Manifest] = {}
        self._offload: dict[str, bool] = {}

    def start(self) -> None:
        self._task = asyncio.create_task(self._loop(), name=f"lane:{self.device}")

    async def stop(self) -> None:
        if self._task:
            self._task.cancel()

    def submit(self, item: WorkItem) -> None:
        self.ready.append(item)
        self._wake.set()

    def pick(self) -> WorkItem:
        """Choose the next stage: priority, then affinity (under the fairness cap), then FIFO."""
        top = min(i.priority for i in self.ready)
        candidates = [i for i in self.ready if i.priority == top]
        same = [i for i in candidates if i.model == self.current_model]
        others = [i for i in candidates if i.model != self.current_model]
        if same and (self.streak < self.fairness_cap or not others):
            choice = min(same, key=lambda i: i.seq)
        else:
            choice = min(candidates, key=lambda i: i.seq)
        self.ready.remove(choice)
        return choice

    async def cancel(self, key: str) -> bool:
        for item in self.ready:
            if item.key == key:
                self.ready.remove(item)
                item.future.set_exception(StageFailed("cancelled", cancelled=True))
                return True
        if self.running and self.running.key == key and self.running.manifest:
            worker = self.workers.find(self.running.manifest.family)
            if worker:
                await worker.cancel(key)
            return True
        return False

    async def unload_all(self) -> None:
        for model in list(self.vram.loaded):
            await self._unload(model)
        self.current_model = None

    async def _loop(self) -> None:
        while True:
            if not self.ready:
                self._wake.clear()
                await self._wake.wait()
                continue
            item = self.pick()
            if item.future.done():
                continue
            self.running = item
            started = time.perf_counter()
            try:
                result = await self._execute(item)
                item.future.set_result(result)
            except Exception as e:
                if not item.future.done():
                    item.future.set_exception(e)
            finally:
                self.running = None
                self.stats.busy_seconds += time.perf_counter() - started
                self.stats.stages += 1

    async def _execute(self, item: WorkItem) -> dict[str, Any]:
        assert item.manifest is not None
        m = item.manifest
        estimate = m.vram_bytes
        if m.id != self.current_model:
            self.streak = 0
        self.streak += 1
        if m.id not in self.vram.loaded:
            external = self._external()
            plan = self.vram.plan(m.id, estimate, external)
            for victim in plan.evict:
                await self._unload(victim)
            if plan.offload:
                log.warning(
                    "%s needs %.1fGB but %s has %.1fGB for models (other applications use %.1fGB); "
                    "loading it with offload",
                    m.id,
                    self.vram.need(m.id, estimate) / GB,
                    self.device,
                    self.vram.available(external) / GB,
                    external / GB,
                )
            self._offload[m.id] = plan.offload
            before = self._read()
            await self._load(m)
            self.vram.did_load(m.id, estimate, offloaded=plan.offload)
            if before is not None:
                self.vram.record_resident(m.id, max(0, (self._read() or 0) - before))
            self.stats.swaps += 1
        if self._offload.get(m.id):
            item.ctx.params.setdefault("offload", True)
        self.vram.touch(m.id)
        self.current_model = m.id
        if self.in_process:
            adapter = self._in_process_adapters[m.id]
            item.ctx.progress = item.on_progress
            started = time.perf_counter()
            outputs = await asyncio.to_thread(adapter.run, item.ctx)
            return {
                "seconds": time.perf_counter() - started,
                "outputs": [
                    {"path": str(o.path), "kind": str(o.kind), "preview": o.preview} for o in outputs
                ],
            }
        for attempt in (1, 2):
            worker = await self.workers.get(m.family, m.interpreter)
            if worker.adapter != m.adapter:
                await worker.load(m.adapter, self.device, _weights(m), offload=self._offload.get(m.id, False))
            try:
                before = self._read()
                result = await worker.run(item.key, item.ctx, item.on_progress)
                if result.get("peak_vram_bytes"):
                    self.vram.record_peak(m.id, int(result["peak_vram_bytes"]))
                if before is not None and m.id in self.vram.loaded:
                    # Runtimes keep what a run grabbed (allocator caches, ONNX Runtime arenas).
                    grown = (self._read() or 0) - before
                    self.vram.record_resident(m.id, max(0, self.vram.loaded[m.id] + grown))
                return result
            except WorkerCrashed:
                self.vram.did_unload(m.id)
                if attempt == 2:
                    raise
                log.warning("worker %s crashed; restarting and retrying %s once", m.family, item.key)
                self.vram.did_load(m.id, estimate, offloaded=self._offload.get(m.id, False))
        raise AssertionError("unreachable")

    def _read(self) -> int | None:
        return self.used() if self.used else None

    def _external(self) -> int:
        """Device bytes held by other processes: what is in use minus what our models hold."""
        used = self._read()
        return max(0, used - sum(self.vram.loaded.values())) if used is not None else 0

    async def _load(self, m: Manifest) -> None:
        self._loaded[m.id] = m
        if self.in_process:
            adapter = instantiate(m.adapter)
            adapter.weights_dir = m.weights_dir
            adapter.offload = self._offload.get(m.id, False)
            adapter.load(adapter.capabilities().devices[0])
            self._in_process_adapters[m.id] = adapter
            return
        worker = await self.workers.get(m.family, m.interpreter)
        # One worker per family holds one model: loading this one drops its sibling.
        for other in [o for o in self._loaded.values() if o.family == m.family and o.id != m.id]:
            if worker.adapter == other.adapter:
                self.vram.did_unload(other.id)
                self._loaded.pop(other.id, None)
        await worker.load(m.adapter, self.device, _weights(m), offload=self._offload.get(m.id, False))

    async def unload_family(self, family: str) -> None:
        for model, m in list(self._loaded.items()):
            if m.family == family:
                await self._unload(model)

    async def _unload(self, model: str) -> None:
        self.vram.did_unload(model)
        m = self._loaded.pop(model, None)
        adapter = self._in_process_adapters.pop(model, None)
        if adapter:
            adapter.unload()
            return
        worker = self.workers.find(m.family) if m else None
        if worker and m and worker.adapter == m.adapter:
            await worker.unload()


class Scheduler:
    def __init__(
        self,
        devices: dict[str, int],
        fit_mode: FitMode = FitMode.FIT_FIRST,
        fairness_cap: int = 8,
        cpu_workers: int | None = None,
        in_process: bool = False,
    ) -> None:
        """`devices` maps device id ("cuda:0", "mps", "cpu") to its model budget in bytes.

        `in_process` runs model adapters inside the supervisor; only for tests and debugging.
        """
        self.workers = WorkerPool()
        self.lanes = {
            dev: DeviceLane(
                dev,
                VramManager(budget, fit_mode),
                self.workers,
                fairness_cap,
                in_process,
                used=None if in_process else gpumem.reader(dev),
            )
            for dev, budget in devices.items()
        }
        self.pool = ProcessPoolExecutor(max_workers=cpu_workers or max(1, (os.cpu_count() or 2) - 1))
        self._seq = itertools.count()
        self._cpu_running: dict[str, asyncio.Future[Any]] = {}

    def start(self) -> None:
        for lane in self.lanes.values():
            lane.start()

    async def stop(self) -> None:
        for lane in self.lanes.values():
            await lane.stop()
        await self.workers.stop_all()
        self.pool.shutdown(wait=False, cancel_futures=True)

    async def release(self, family: str) -> None:
        """Unloads a family's models and stops its worker, so its environment can change on disk
        (Windows keeps a running interpreter's files locked)."""
        for lane in self.lanes.values():
            await lane.unload_family(family)
        await self.workers.stop(family)

    def fit(self, manifest: Manifest) -> tuple[str | None, str]:
        """Where a model would run here, installed or not, and how well.

        "fits" in the device's budget, "offload" runs with part of it in RAM (slower), "too-big"
        is below the model's minimum, "unsupported" has no device the model can use.
        """
        for dev, lane in self.lanes.items():
            backend = dev.split(":")[0]
            if not manifest.supports(backend):
                continue
            spec = manifest.requirements.get(backend) or {}
            if lane.vram.budget < float(spec.get("min_vram_gb", 0)) * GB:
                return dev, "too-big"
            if manifest.vram_bytes > lane.vram.budget:
                return dev, "offload"
            return dev, "fits"
        return None, "unsupported"

    def place(self, manifest: Manifest) -> str:
        """The device a model runs on here, by the order of preference in its requirements."""
        if not manifest.installed:
            raise StageFailed(
                f"model {manifest.id} is not installed here; run: forja models install {manifest.id}", False
            )
        for dev in self.lanes:
            if manifest.supports(dev.split(":")[0]):
                return dev
        raise StageFailed(f"{manifest.id} cannot run on this machine's devices: {list(self.lanes)}", False)

    async def submit(
        self,
        key: str,
        ctx: StageContext,
        manifest: Manifest | None,
        priority: int,
        on_progress: Callable[[int, int], None],
    ) -> dict[str, Any]:
        if manifest is None:
            loop = asyncio.get_running_loop()
            fut = loop.run_in_executor(self.pool, _run_builtin, str(ctx.task), ctx_to_json(ctx))
            self._cpu_running[key] = fut
            try:
                return await fut
            finally:
                self._cpu_running.pop(key, None)
        item = WorkItem(key, ctx, manifest, priority, on_progress, seq=next(self._seq))
        self.lanes[self.place(manifest)].submit(item)
        return await item.future

    async def cancel(self, key: str) -> None:
        for lane in self.lanes.values():
            if await lane.cancel(key):
                return
        fut = self._cpu_running.get(key)
        if fut:
            fut.cancel()

    async def unload_all(self) -> None:
        for lane in self.lanes.values():
            await lane.unload_all()
