"""Forja's engine on Modal: a CPU gateway that serves /v1, and a GPU worker that wakes to run.

The gateway answers health checks and moves artifacts by hash on its own, so a core reaches
it like any remote engine and pays for GPU seconds only while stages run. Deploy it from the
repository root with your own Modal account:

    modal secret create forja-engine FORJA_ENGINE_TOKEN=<a long random token, 16 characters or more>
    modal deploy docker/modal/app.py

Then add it in Forja (Settings, or `forja providers add modal <gateway url>`) with that
token. The image builds on Modal from docker/engine/Dockerfile; nothing is pushed anywhere.

Deploy-time settings, as environment variables:

    FORJA_MODAL_GPU   the GPU type (default L40S, 48 GB; A10G or L4 for 24 GB models)
    FORJA_CUDA        the engine image's CUDA line (default cu124)
    FORJA_VERSION     the version the image is labelled with

and in the forja-engine secret, besides the token:

    FORJA_MODELS       models the worker installs on the volume before its first run
    FORJA_ENGINE_ARGS  more engine flags, such as --fit-mode quality-first
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Any

import modal

ROOT = Path(__file__).resolve().parents[2]
GPU = os.environ.get("FORJA_MODAL_GPU", "L40S")

app = modal.App("forja-engine")
image = modal.Image.from_dockerfile(
    str(ROOT / "docker" / "engine" / "Dockerfile"),
    context_dir=str(ROOT),
    build_args={
        "VERSION": os.environ.get("FORJA_VERSION", "0.1.0"),
        "CUDA": os.environ.get("FORJA_CUDA", "cu124"),
    },
).entrypoint([])

# Weights and family environments: written only by the GPU worker (one container at a time).
data = modal.Volume.from_name("forja-data", create_if_missing=True)
# The artifact store the gateway and the worker share. Apart from the weights, because a
# volume reloads only while none of its files are open, and a loaded model keeps them open.
artifacts = modal.Volume.from_name("forja-artifacts", create_if_missing=True)
# Run state and events, from the worker to the gateway (see forja_engine.serverless.gateway).
board = modal.Dict.from_name("forja-board", create_if_missing=True)
secret = modal.Secret.from_name("forja-engine")


def _quietly(fn: Any) -> Any:
    # A reload fails while a request still streams a file; a stale view only means a 404, and
    # the core then uploads again.
    def call() -> None:
        try:
            fn()
        except Exception as e:
            print(f"volume: {e}")

    return call


@app.cls(
    image=image,
    gpu=GPU,
    volumes={"/data": data, "/artifacts": artifacts},
    secrets=[secret],
    timeout=60 * 60,
    scaledown_window=120,
    max_containers=1,
)
class Engine:
    @modal.enter()
    def start(self) -> None:
        from forja_engine.serverless import Worker, models_from_env, start_engine

        _, client = start_engine(Path("/artifacts"), ["--envs", "/data/envs", "--models", "/data/models"])
        self.worker = Worker(client)
        self.worker.ensure(models_from_env())
        data.commit()
        board.put("describe", self.worker.describe())

    @modal.method()
    def run(self, job: dict[str, Any]) -> None:
        from forja_engine.serverless.gateway import Backend, serve

        def unused(*_: Any) -> Any:
            raise RuntimeError("workers do not spawn")

        backend = Backend(
            spawn=unused, failure=unused, cancel=unused, reload=artifacts.reload, commit=artifacts.commit
        )
        serve(self.worker, job, board, backend)


def _failure(call_id: str) -> str | None:
    try:
        modal.FunctionCall.from_id(call_id).get(timeout=0)
    except (TimeoutError, modal.exception.TimeoutError):
        return None  # still running
    except Exception as e:
        return str(e) or type(e).__name__
    return None


@app.function(image=image, volumes={"/artifacts": artifacts}, secrets=[secret], cpu=1.0, memory=1024)
@modal.concurrent(max_inputs=32)
@modal.asgi_app()
def gateway() -> Any:
    from forja_engine.serverless.gateway import Backend, create_gateway

    engine = Engine()
    backend = Backend(
        spawn=lambda job: engine.run.spawn(job).object_id,
        failure=_failure,
        cancel=lambda call_id: modal.FunctionCall.from_id(call_id).cancel(),
        reload=_quietly(artifacts.reload),
        commit=_quietly(artifacts.commit),
    )
    # Never None here: that serves without a token. create_gateway refuses an empty or short one.
    return create_gateway(Path("/artifacts/store"), os.environ.get("FORJA_ENGINE_TOKEN", ""), board, backend)
