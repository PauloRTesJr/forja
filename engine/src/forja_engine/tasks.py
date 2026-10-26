"""Long-running engine work the app follows live: model installs, removals and fixes.

Work runs in a thread and reports to its Task. Listeners on /v1/tasks/{id}/events receive the
task's whole state each time it changes, at most a few times a second, so a client that
reconnects just gets the current state. Installs and removals run one at a time: they share
bandwidth, disk and compilers.
"""

from __future__ import annotations

import asyncio
import contextlib
import threading
import time
import uuid
from collections import deque
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any

from forja_engine.install import InstallCancelled, Reporter

_KEEP = 50
_LOG_LINES = 200
# The statuses a task ends in.
ENDED = ("succeeded", "failed", "cancelled")


@dataclass
class Step:
    id: str
    label: str
    status: str = "pending"  # pending | running | done | skipped | failed


class Task:
    def __init__(self, kind: str, target: str, steps: tuple[tuple[str, str], ...]) -> None:
        self.id = uuid.uuid4().hex[:12]
        self.kind = kind
        self.target = target
        self.status = "queued"  # queued | running | succeeded | failed | cancelled
        self.steps = [Step(i, label) for i, label in steps]
        self.progress: dict[str, Any] | None = None
        self.log: deque[str] = deque(maxlen=_LOG_LINES)
        self.error: str | None = None
        self.result: dict[str, Any] = {}
        self.created_at = time.time()
        self.finished_at: float | None = None
        self.cancel = threading.Event()
        self.version = 0
        self._lock = threading.Lock()
        self._loop: asyncio.AbstractEventLoop | None = None
        self._changed = asyncio.Event()

    @property
    def done(self) -> bool:
        return self.status in ENDED

    def bind(self, loop: asyncio.AbstractEventLoop) -> None:
        self._loop = loop

    def touch(self) -> None:
        """Mark a change; safe from any thread."""
        with self._lock:
            self.version += 1
        if self._loop is not None:
            with contextlib.suppress(RuntimeError):  # the loop closed at shutdown
                self._loop.call_soon_threadsafe(self._changed.set)

    async def wait(self, after: int) -> int:
        while self.version <= after:
            self._changed.clear()
            if self.version > after:
                break
            await self._changed.wait()
        return self.version

    def set_step(self, step: str, status: str) -> None:
        for s in self.steps:
            if s.id == step:
                s.status = status
        if status != "running" and self.progress and self.progress.get("step") == step:
            self.progress = None
        self.touch()

    def finish(self, status: str, error: str | None = None) -> None:
        self.status = status
        self.error = error
        self.finished_at = time.time()
        for s in self.steps:
            if s.status == "running":
                s.status = "failed" if status == "failed" else "pending"
        self.progress = None
        self.touch()

    def to_json(self, log_lines: int = 30) -> dict[str, Any]:
        return {
            "id": self.id,
            "kind": self.kind,
            "target": self.target,
            "status": self.status,
            "steps": [{"id": s.id, "label": s.label, "status": s.status} for s in self.steps],
            "progress": self.progress,
            "log": list(self.log)[-log_lines:],
            "error": self.error,
            "result": self.result,
            "created_at": self.created_at,
            "finished_at": self.finished_at,
            "version": self.version,
        }


class TaskReporter(Reporter):
    """Feeds an installer's log, steps and bytes into a Task."""

    def __init__(self, task: Task) -> None:
        super().__init__(log=self._log)
        self.task = task
        self.cancel = task.cancel
        self._current = ""
        self._last_bytes = 0.0

    def _log(self, line: str) -> None:
        self.task.log.append(line)
        self.task.touch()

    def step(self, step: str, status: str) -> None:
        if status == "running":
            self._current = step
        self.task.set_step(step, status)

    def bytes(self, done: int, total: int) -> None:
        self.task.progress = {"step": self._current, "done": done, "total": total}
        now = time.monotonic()
        if now - self._last_bytes >= 0.25 or (total and done >= total):
            self._last_bytes = now
            self.task.touch()


Work = Callable[[TaskReporter], dict[str, Any] | None]


class TaskManager:
    def __init__(self) -> None:
        self.tasks: dict[str, Task] = {}
        self._serial: asyncio.Lock | None = None
        # The loop holds tasks weakly; this keeps running ones alive.
        self._running: set[asyncio.Task[None]] = set()

    def start(
        self,
        kind: str,
        target: str,
        steps: tuple[tuple[str, str], ...],
        work: Work,
        *,
        serial: bool = True,
        prepare: Callable[[], Awaitable[None]] | None = None,
        done: Callable[[Task], object] | None = None,
    ) -> Task:
        """Queue work; must be called from the event loop."""
        loop = asyncio.get_running_loop()
        if self._serial is None:
            self._serial = asyncio.Lock()
        task = Task(kind, target, steps)
        task.bind(loop)
        self.tasks[task.id] = task
        self._prune()
        runner = loop.create_task(self._run(task, work, serial, prepare, done))
        self._running.add(runner)
        runner.add_done_callback(self._running.discard)
        return task

    async def _run(
        self,
        task: Task,
        work: Work,
        serial: bool,
        prepare: Callable[[], Awaitable[None]] | None,
        done: Callable[[Task], object] | None,
    ) -> None:
        assert self._serial is not None
        lock: contextlib.AbstractAsyncContextManager[Any] = (
            self._serial if serial else contextlib.nullcontext()
        )
        async with lock:
            if task.done:  # cancelled while it waited its turn
                return
            task.status = "running"
            task.touch()
            reporter = TaskReporter(task)
            try:
                if prepare is not None:
                    await prepare()
                result: dict[str, Any] | None = await asyncio.to_thread(work, reporter)
                task.result = result or {}
                task.finish("succeeded")
            except InstallCancelled:
                task.finish("cancelled")
            except Exception as e:
                task.finish("failed", str(e) or type(e).__name__)
            finally:
                if done is not None:
                    done(task)

    def active(self, kind: str, target: str) -> Task | None:
        for t in self.tasks.values():
            if t.kind == kind and t.target == target and not t.done:
                return t
        return None

    def cancel(self, task_id: str) -> Task | None:
        task = self.tasks.get(task_id)
        if task is not None and not task.done:
            task.cancel.set()
            if task.status == "queued":
                task.finish("cancelled")
            else:
                task.touch()
        return task

    def _prune(self) -> None:
        finished = sorted((t for t in self.tasks.values() if t.done), key=lambda t: t.created_at)
        for t in finished[: max(0, len(self.tasks) - _KEEP)]:
            self.tasks.pop(t.id, None)
