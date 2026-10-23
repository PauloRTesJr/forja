"""Supervisor side of model workers: start, talk to, and restart worker processes."""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
from collections import deque
from collections.abc import Callable
from typing import Any

from forja_engine.sdk import StageContext
from forja_engine.worker import ctx_to_json

log = logging.getLogger(__name__)
_STDERR_TAIL = 60


class WorkerCrashed(RuntimeError):
    def __init__(self, family: str, tail: list[str]) -> None:
        super().__init__(f"worker for {family} exited unexpectedly")
        self.tail = tail


class StageFailed(RuntimeError):
    def __init__(self, message: str, cancelled: bool) -> None:
        super().__init__(message)
        self.cancelled = cancelled


class Worker:
    def __init__(self, family: str, interpreter: str) -> None:
        self.family = family
        self.interpreter = interpreter
        self.adapter: str | None = None
        self.proc: asyncio.subprocess.Process | None = None
        self.stderr_tail: deque[str] = deque(maxlen=_STDERR_TAIL)
        self._control: asyncio.Queue[dict[str, Any]] = asyncio.Queue()
        self._pending: dict[str, asyncio.Future[dict[str, Any]]] = {}
        self._progress: dict[str, Callable[[int, int], None]] = {}
        self._tasks: list[asyncio.Task[None]] = []
        self._lock = asyncio.Lock()

    @property
    def alive(self) -> bool:
        return self.proc is not None and self.proc.returncode is None

    async def start(self) -> None:
        self.proc = await asyncio.create_subprocess_exec(
            self.interpreter,
            "-m",
            "forja_engine.worker",
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            limit=1 << 20,
        )
        self._tasks = [asyncio.create_task(self._read_stdout()), asyncio.create_task(self._read_stderr())]
        msg = await self._control_reply()
        if msg.get("type") != "ready":
            raise WorkerCrashed(self.family, list(self.stderr_tail))

    async def load(
        self,
        adapter: str,
        device: str,
        weights_dir: str | None = None,
        dtype: str = "fp16",
        offload: bool = False,
    ) -> None:
        async with self._lock:
            await self._send(
                {
                    "op": "load",
                    "adapter": adapter,
                    "device": device,
                    "dtype": dtype,
                    "weights_dir": weights_dir,
                    "offload": offload,
                }
            )
            msg = await self._control_reply()
            if msg.get("type") != "loaded":
                raise StageFailed(msg.get("error", "load failed"), cancelled=False)
            self.adapter = adapter

    async def unload(self) -> None:
        if not self.alive or self.adapter is None:
            return
        async with self._lock:
            await self._send({"op": "unload"})
            await self._control_reply()
            self.adapter = None

    async def run(
        self, run_id: str, ctx: StageContext, on_progress: Callable[[int, int], None]
    ) -> dict[str, Any]:
        fut: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
        self._pending[run_id] = fut
        self._progress[run_id] = on_progress
        try:
            await self._send({"op": "run", "id": run_id, "ctx": ctx_to_json(ctx)})
            msg = await fut
        finally:
            self._pending.pop(run_id, None)
            self._progress.pop(run_id, None)
        if msg["type"] == "error":
            raise StageFailed(msg["error"], cancelled=bool(msg.get("cancelled")))
        return msg

    async def cancel(self, run_id: str) -> None:
        if self.alive and run_id in self._pending:
            await self._send({"op": "cancel", "id": run_id})

    async def stop(self) -> None:
        if self.alive:
            with contextlib.suppress(Exception):
                await self._send({"op": "exit"})
            try:
                await asyncio.wait_for(self.proc.wait(), timeout=5)  # type: ignore[union-attr]
            except TimeoutError:
                self.proc.kill()  # type: ignore[union-attr]
        for t in self._tasks:
            t.cancel()

    async def _send(self, msg: dict[str, Any]) -> None:
        if not self.alive:
            raise WorkerCrashed(self.family, list(self.stderr_tail))
        assert self.proc and self.proc.stdin
        self.proc.stdin.write((json.dumps(msg) + "\n").encode())
        await self.proc.stdin.drain()

    async def _control_reply(self) -> dict[str, Any]:
        return await self._control.get()

    async def _read_stdout(self) -> None:
        assert self.proc and self.proc.stdout
        async for raw in self.proc.stdout:
            msg = json.loads(raw)
            kind = msg.get("type")
            rid = msg.get("id")
            if kind == "progress" and rid in self._progress:
                self._progress[rid](msg["step"], msg["total"])
            elif kind in ("result", "error") and rid in self._pending:
                self._pending[rid].set_result(msg)
            else:
                await self._control.put(msg)
        await self.proc.wait()
        crash = WorkerCrashed(self.family, list(self.stderr_tail))
        for fut in self._pending.values():
            if not fut.done():
                fut.set_exception(crash)
        await self._control.put({"type": "exited"})

    async def _read_stderr(self) -> None:
        assert self.proc and self.proc.stderr
        async for raw in self.proc.stderr:
            line = raw.decode(errors="replace").rstrip()
            self.stderr_tail.append(line)
            log.debug("[%s] %s", self.family, line)


class WorkerPool:
    def __init__(self) -> None:
        self._workers: dict[str, Worker] = {}

    async def get(self, family: str, interpreter: str) -> Worker:
        worker = self._workers.get(family)
        if worker is None or not worker.alive:
            worker = Worker(family, interpreter)
            await worker.start()
            self._workers[family] = worker
        return worker

    def find(self, family: str) -> Worker | None:
        return self._workers.get(family)

    async def stop(self, family: str) -> None:
        worker = self._workers.pop(family, None)
        if worker is not None:
            await worker.stop()

    async def stop_all(self) -> None:
        await asyncio.gather(*(w.stop() for w in self._workers.values()), return_exceptions=True)
        self._workers.clear()
