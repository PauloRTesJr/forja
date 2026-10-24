"""Model worker process: one per model family, running in that family's virtual environment.

Protocol: one JSON object per line. Requests on stdin, replies on stdout. Meshes and images
never cross the pipe; only paths do. The adapter's own prints go to stderr, so they can never
corrupt the protocol stream.

    -> {"op": "load", "adapter": "pkg.mod:Class", "device": "cuda:0", "dtype": "fp16", "offload": false}
    -> {"op": "run", "id": "...", "ctx": {...}}
    -> {"op": "cancel", "id": "..."}
    -> {"op": "unload"} / {"op": "exit"}
    <- {"type": "ready"} / {"type": "loaded", "adapter": ...}
    <- {"type": "progress", "id": ..., "step": n, "total": m}
    <- {"type": "result", "id": ..., "outputs": [{"path", "kind", "preview"}], "seconds": s}
    <- {"type": "error", "id": ..., "error": "...", "cancelled": bool}
"""

from __future__ import annotations

import json
import os
import queue
import sys
import threading
import time
import traceback
from pathlib import Path
from typing import Any, TextIO

from forja_engine.sdk import Adapter, ArtifactKind, Cancelled, Device, StageContext, StageInput, StageType


def ctx_to_json(ctx: StageContext) -> dict[str, Any]:
    return {
        "stage_id": ctx.stage_id,
        "task": str(ctx.task),
        "inputs": [
            {"path": str(i.path), "kind": str(i.kind), "hash": i.hash, "view": i.view} for i in ctx.inputs
        ],
        "params": ctx.params,
        "seed": ctx.seed,
        "out_dir": str(ctx.out_dir),
    }


def ctx_from_json(d: dict[str, Any]) -> StageContext:
    return StageContext(
        stage_id=d["stage_id"],
        task=StageType(d["task"]),
        inputs=[
            StageInput(Path(i["path"]), ArtifactKind(i["kind"]), i["hash"], i.get("view"))
            for i in d["inputs"]
        ],
        params=d["params"],
        seed=d["seed"],
        out_dir=Path(d["out_dir"]),
    )


class _Out:
    def __init__(self, stream: TextIO) -> None:
        self._stream = stream
        self._lock = threading.Lock()

    def send(self, msg: dict[str, Any]) -> None:
        line = json.dumps(msg, separators=(",", ":"))
        with self._lock:
            self._stream.write(line + "\n")
            self._stream.flush()


def _cuda() -> Any:
    """torch.cuda when the adapter imported torch and a GPU is present; never imports torch itself."""
    torch = sys.modules.get("torch")
    if torch is None or not torch.cuda.is_available():
        return None
    return torch.cuda


def _reset_peak_vram() -> None:
    if cuda := _cuda():
        cuda.reset_peak_memory_stats()


def _peak_vram() -> int | None:
    """Peak bytes held by PyTorch's allocator during the stage; None when not measurable here."""
    cuda = _cuda()
    return int(cuda.max_memory_reserved()) if cuda else None


def _detach_stdio() -> tuple[TextIO, TextIO]:
    """Move the protocol pipes off fds 0 and 1 and return them.

    fd 0 becomes NUL: on Windows, a DLL loaded while the reader thread blocks on the stdin
    pipe queries the standard input handle during CRT init and deadlocks behind that read.
    fd 1 becomes stderr, so native code that prints cannot corrupt the protocol stream.
    """
    proto_in = os.fdopen(os.dup(0), "r", encoding="utf-8")
    proto_out = os.fdopen(os.dup(1), "w", encoding="utf-8", buffering=1)
    null = os.open(os.devnull, os.O_RDONLY)
    os.dup2(null, 0)
    os.close(null)
    os.dup2(2, 1)
    sys.stdin = open(os.devnull, encoding="utf-8")  # noqa: SIM115
    sys.stdout = sys.stderr
    return proto_in, proto_out


def main() -> None:
    proto_in, proto = _detach_stdio()
    out = _Out(proto)

    requests: queue.Queue[dict[str, Any]] = queue.Queue()
    cancelled: set[str] = set()

    def reader() -> None:
        for line in proto_in:
            msg = json.loads(line)
            if msg.get("op") == "cancel":
                cancelled.add(msg["id"])
            else:
                requests.put(msg)
        requests.put({"op": "exit"})

    threading.Thread(target=reader, daemon=True).start()
    adapter: Adapter | None = None
    out.send({"type": "ready", "pid": os.getpid()})

    while True:
        msg = requests.get()
        op = msg.get("op")
        if op == "exit":
            if adapter:
                adapter.unload()
            return
        if op == "load":
            from forja_engine.registry import instantiate

            try:
                adapter = instantiate(msg["adapter"])
                if msg.get("weights_dir"):
                    adapter.weights_dir = Path(msg["weights_dir"])
                adapter.offload = bool(msg.get("offload"))
                adapter.load(Device(msg["device"].split(":")[0]), msg.get("dtype", "fp16"))
                out.send({"type": "loaded", "adapter": msg["adapter"]})
            except Exception as e:
                out.send({"type": "error", "id": None, "error": f"load failed: {e}", "cancelled": False})
        elif op == "unload":
            if adapter:
                adapter.unload()
            out.send({"type": "unloaded"})
        elif op == "run":
            run_id = msg["id"]
            if adapter is None:
                out.send({"type": "error", "id": run_id, "error": "no adapter loaded", "cancelled": False})
                continue
            ctx = ctx_from_json(msg["ctx"])

            def progress(step: int, total: int, rid: str = run_id) -> None:
                out.send({"type": "progress", "id": rid, "step": step, "total": total})

            def is_cancelled(rid: str = run_id) -> bool:
                return rid in cancelled

            ctx.progress, ctx.cancelled = progress, is_cancelled
            _reset_peak_vram()
            started = time.perf_counter()
            try:
                outputs = adapter.run(ctx)
                out.send(
                    {
                        "type": "result",
                        "id": run_id,
                        "seconds": time.perf_counter() - started,
                        "peak_vram_bytes": _peak_vram(),
                        "outputs": [
                            {"path": str(o.path), "kind": str(o.kind), "preview": o.preview} for o in outputs
                        ],
                    }
                )
            except Cancelled:
                out.send({"type": "error", "id": run_id, "error": "cancelled", "cancelled": True})
            except Exception as e:
                traceback.print_exc()
                out.send(
                    {"type": "error", "id": run_id, "error": f"{type(e).__name__}: {e}", "cancelled": False}
                )
            finally:
                cancelled.discard(run_id)


if __name__ == "__main__":
    main()
