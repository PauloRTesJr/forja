"""One engine on loopback, driven through /v1 for jobs that arrive from a vendor's queue."""

from __future__ import annotations

import base64
import json
import os
import secrets
import shlex
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from collections.abc import Callable, Iterator
from dataclasses import dataclass
from pathlib import Path
from typing import IO, Any, Literal, Protocol

from pydantic import BaseModel, Field

from forja_engine.store import PREFIX

_DONE = ("succeeded", "failed", "cancelled")
MB = 1 << 20


class EngineError(RuntimeError):
    pass


@dataclass
class Reply:
    status: int
    body: bytes

    def json(self) -> Any:
        return json.loads(self.body)


class EngineHTTP(Protocol):
    """The engine's /v1 API as the worker reaches it: plain requests and an event stream."""

    def request(self, method: str, path: str, body: bytes | None = None, json_body: Any = None) -> Reply: ...

    def lines(self, path: str) -> Iterator[str]: ...


class UrllibEngine:
    def __init__(self, base: str, token: str | None) -> None:
        self.base = base.rstrip("/")
        self.token = token

    def _req(self, method: str, path: str, data: bytes | None, kind: str | None) -> urllib.request.Request:
        req = urllib.request.Request(self.base + path, data=data, method=method)
        if self.token:
            req.add_header("Authorization", f"Bearer {self.token}")
        if kind:
            req.add_header("Content-Type", kind)
        return req

    def request(self, method: str, path: str, body: bytes | None = None, json_body: Any = None) -> Reply:
        kind = None
        if json_body is not None:
            body, kind = json.dumps(json_body).encode(), "application/json"
        try:
            with urllib.request.urlopen(self._req(method, path, body, kind), timeout=600) as resp:
                return Reply(resp.status, resp.read())
        except urllib.error.HTTPError as e:
            return Reply(e.code, e.read())

    def lines(self, path: str) -> Iterator[str]:
        with urllib.request.urlopen(self._req("GET", path, None, None), timeout=None) as resp:
            for raw in resp:
                yield raw.decode("utf-8").rstrip("\r\n")


class Job(BaseModel):
    """What a vendor's queue hands the worker."""

    op: Literal["describe", "run"]
    # An engine run request, with inputs as "stage:<id>" or "blake3:<hex>" refs.
    run: dict[str, Any] | None = None
    # The id the caller knows this run by (a gateway's own); the engine's otherwise.
    id: str | None = None
    # Artifacts that travel with the job, as base64, for workers the core cannot upload to.
    inline: dict[str, str] = Field(default_factory=dict)
    # "inline" returns every stage output in the result; "store" leaves them in the worker's
    # store, where the core fetches them by hash.
    outputs: Literal["inline", "store"] = "store"
    # The most bytes of outputs that may come back inline; 0 means no limit.
    limit: int = 0


def _check(reply: Reply, what: str, *ok: int) -> Reply:
    if reply.status in (ok or (200,)):
        return reply
    try:
        detail = reply.json().get("detail", "")
    except (ValueError, AttributeError):
        detail = reply.body.decode("utf-8", "replace")
    raise EngineError(f"{what}: engine answered {reply.status}: {detail}")


def sse(lines: Iterator[str]) -> Iterator[dict[str, Any]]:
    """Parses a Server-Sent Events stream into {id, type, data} events."""
    ev: dict[str, Any] = {}
    for line in lines:
        if line.startswith("id: "):
            ev["id"] = int(line[4:])
        elif line.startswith("event: "):
            ev["type"] = line[7:]
        elif line.startswith("data: "):
            ev["data"] = json.loads(line[6:])
        elif line == "" and "type" in ev:
            yield {"id": ev.get("id", 0), "type": ev["type"], "data": ev.get("data", {})}
            ev = {}


class Worker:
    def __init__(self, engine: EngineHTTP) -> None:
        self.engine = engine
        self._last_run: str | None = None

    def describe(self) -> dict[str, Any]:
        """What this worker runs, as /v1/info and /v1/models say; the core keys its cache by it."""
        info = _check(self.engine.request("GET", "/v1/info"), "describing the engine").json()
        models = _check(self.engine.request("GET", "/v1/models"), "listing models").json()
        return {"info": info, "models": models}

    def ensure(self, models: list[str], log: Callable[[str], None] = print) -> None:
        """Installs models the worker must run, onto its persistent volume, once."""
        if not models:
            return
        listing = {
            m["id"]: m for m in _check(self.engine.request("GET", "/v1/models"), "listing models").json()
        }
        for model in models:
            m = listing.get(model)
            if m is None:
                raise EngineError(f"this engine has no model {model}")
            if m["status"] in ("ready", "loaded"):
                continue
            log(f"installing {model}")
            path = f"/v1/models/{model}/install"
            task = _check(self.engine.request("POST", path, json_body={}), f"installing {model}", 202).json()
            state = self._follow(task, log)
            if state["status"] != "succeeded":
                raise EngineError(f"installing {model} {state['status']}: {state.get('error') or ''}")

    def _follow(self, task: dict[str, Any], log: Callable[[str], None]) -> dict[str, Any]:
        seen = 0
        state = task
        for ev in sse(self.engine.lines(f"/v1/tasks/{task['id']}/events")):
            state = ev["data"]
            for line in state.get("log", [])[seen:]:
                log(line)
            seen = len(state.get("log", []))
            if state["status"] in _DONE:
                break
        return state

    def handle(self, raw: dict[str, Any]) -> Iterator[dict[str, Any]]:
        """Runs one job: yields {"event": ...} for each engine event, then {"result": ...}."""
        job = Job.model_validate(raw)
        if job.op == "describe":
            yield {"result": self.describe()}
            return
        if job.run is None:
            raise EngineError("a run job needs its run request")
        yield from self._run(job, job.run)

    def _run(self, job: Job, req: dict[str, Any]) -> Iterator[dict[str, Any]]:
        for ref, data in job.inline.items():
            reply = self.engine.request("PUT", f"/v1/artifacts/{ref}", body=base64.b64decode(data))
            _check(reply, f"storing {ref}", 200, 201)
        refs = sorted({r for s in req.get("stages", []) for r in s.get("inputs", []) if r.startswith(PREFIX)})
        missing = [r for r in refs if self.engine.request("HEAD", f"/v1/artifacts/{r}").status == 404]
        if missing:
            yield {"result": {"missing": missing}}
            return
        self._cancel_stale()
        run = _check(self.engine.request("POST", "/v1/runs", json_body=req), "starting the run", 202).json()
        self._last_run = run["id"]
        for ev in self._events(run["id"]):
            yield {"event": ev}
        final = _check(self.engine.request("GET", f"/v1/runs/{run['id']}"), "reading the run").json()
        if job.id:
            final["id"] = job.id
        result: dict[str, Any] = {"run": final, **self.describe()}
        if job.outputs == "inline":
            result["artifacts"] = self._inline(final, job.limit)
        yield {"result": result}

    def _events(self, run_id: str) -> Iterator[dict[str, Any]]:
        cursor, failures = 0, 0
        while True:
            try:
                for ev in sse(self.engine.lines(f"/v1/runs/{run_id}/events?after={cursor}")):
                    cursor = ev["id"]
                    yield ev
                    if ev["type"] == "run.finished":
                        return
            except OSError:
                failures += 1
                if failures > 5:
                    raise
            time.sleep(0.2)

    def _cancel_stale(self) -> None:
        # A job the vendor cancelled leaves its run going in the engine; the GPU belongs to
        # the next job now.
        if self._last_run is None:
            return
        reply = self.engine.request("GET", f"/v1/runs/{self._last_run}")
        if reply.status == 200 and reply.json()["status"] not in _DONE:
            self.engine.request("DELETE", f"/v1/runs/{self._last_run}")

    def _inline(self, run: dict[str, Any], limit: int) -> dict[str, str]:
        out: dict[str, str] = {}
        total = 0
        for state in run["stages"].values():
            for o in state["outputs"]:
                ref = o["hash"]
                if ref in out:
                    continue
                data = _check(self.engine.request("GET", f"/v1/artifacts/{ref}"), f"reading {ref}").body
                total += len(data)
                if limit and total > limit:
                    raise EngineError(
                        f"the outputs pass {limit / MB:.0f} MB, more than can come back with the job; "
                        "give Forja the S3 key of the endpoint's network volume"
                    )
                out[ref] = base64.b64encode(data).decode()
        return out


def models_from_env() -> list[str]:
    """FORJA_MODELS lists the models a worker installs before taking jobs, comma separated."""
    return [m.strip() for m in os.environ.get("FORJA_MODELS", "").split(",") if m.strip()]


def start_engine(data_dir: Path, args: list[str] | None = None) -> tuple[subprocess.Popen[str], UrllibEngine]:
    """Starts forja-engine on loopback with its store, envs and weights under data_dir.

    args and FORJA_ENGINE_ARGS add serve flags, such as `--fit-mode quality-first` on a large GPU.
    """
    token = secrets.token_urlsafe(24)
    store = str(data_dir / "store")
    cmd = [sys.executable, "-m", "forja_engine.cli", "serve", "--port", "0", "--store", store, *(args or [])]
    cmd += shlex.split(os.environ.get("FORJA_ENGINE_ARGS", ""))
    env = {**os.environ, "FORJA_ENGINE_TOKEN": token}
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, env=env)
    assert proc.stdout is not None
    port = None
    for line in proc.stdout:
        print(line, end="", flush=True)
        if line.startswith("FORJA_ENGINE_LISTENING "):
            port = int(line.split()[1])
            break
    if port is None:
        raise EngineError(f"the engine exited ({proc.wait()}) before it listened")
    threading.Thread(target=_drain, args=(proc.stdout,), daemon=True).start()
    return proc, UrllibEngine(f"http://127.0.0.1:{port}", token)


def _drain(out: IO[str]) -> None:
    for line in out:
        print(line, end="", flush=True)
