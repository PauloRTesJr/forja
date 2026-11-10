"""What the supervisor says when a model worker goes wrong."""

import sys

import pytest

from forja_engine.workers import StageFailed, Worker, WorkerCrashed


async def test_a_worker_that_dies_while_loading_says_why(misbehaving_models):
    worker = Worker("misbehaving", sys.executable)
    await worker.start()
    try:
        with pytest.raises(WorkerCrashed) as crash:
            await worker.load("misbehaving:DiesOnLoad", "cpu")
    finally:
        await worker.stop()
    # Not a bare "load failed": that it died, how, and its last words.
    assert "exit code 3" in str(crash.value)
    assert "ran out of device memory" in "\n".join(crash.value.tail)


async def test_a_load_that_fails_names_the_kind_of_error(misbehaving_models):
    worker = Worker("misbehaving", sys.executable)
    await worker.start()
    try:
        with pytest.raises(StageFailed) as failed:
            await worker.load("misbehaving:FailsToLoad", "cpu")
        assert "KeyError" in str(failed.value) and "unet" in str(failed.value)
        # The traceback is in the engine's log, where a load failure used to leave nothing.
        assert any("Traceback" in line for line in worker.stderr_tail)
    finally:
        await worker.stop()
