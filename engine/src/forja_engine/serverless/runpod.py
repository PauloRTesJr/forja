"""RunPod serverless handler: `python -m forja_engine.serverless.runpod`, the RunPod image's command.

Each job's events stream back as they happen (a generator handler); the last item is the
result. Artifacts move one of two ways, chosen by the core per job:

- With the S3 key of the endpoint's network volume, the core writes inputs straight into the
  engine's store on the volume and reads outputs from it, each only when the other side lacks
  that hash. Nothing large rides in the job.
- Without it, inputs ride in the job as base64 and outputs come back in the result, within
  RunPod's payload limits.
"""

from __future__ import annotations

import os
from collections.abc import Iterator
from pathlib import Path
from typing import Any

from forja_engine.serverless.worker import Worker, models_from_env, start_engine

# RunPod mounts an endpoint's network volume here on every worker.
VOLUME = Path("/runpod-volume")

_worker: Worker | None = None


def data_dir() -> Path:
    """Envs, weights and the store: on the network volume when there is one, so they outlive workers.

    The volume wins over FORJA_DATA_DIR, which the engine image always sets to /data: the core
    moves artifacts through the volume's S3 API at forja/store/objects/ and must find them there.
    """
    if VOLUME.is_dir():
        return VOLUME / "forja"
    return Path(os.environ.get("FORJA_DATA_DIR") or "/data")


def worker() -> Worker:
    global _worker
    if _worker is None:
        _, engine = start_engine(data_dir())
        w = Worker(engine)
        w.ensure(models_from_env())
        _worker = w
    return _worker


def handler(job: dict[str, Any]) -> Iterator[dict[str, Any]]:
    try:
        yield from worker().handle(job.get("input") or {})
    except Exception as e:
        yield {"error": f"{type(e).__name__}: {e}"}


def main() -> None:
    import runpod

    worker()  # the cold start: the engine runs and its models are installed before any job
    runpod.serverless.start({"handler": handler, "return_aggregate_stream": True})


if __name__ == "__main__":
    main()
