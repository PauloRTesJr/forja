import asyncio
import sys
import time
from concurrent.futures.process import BrokenProcessPool

import misbehaving
import pytest
from PIL import Image

from forja_engine import scheduler
from forja_engine.registry import License, Manifest
from forja_engine.scheduler import BATCH, INTERACTIVE, DeviceLane, WorkItem
from forja_engine.sdk import ArtifactKind, StageContext, StageInput, StageType
from forja_engine.vram import VramManager
from forja_engine.workers import StageFailed, WorkerPool


def _manifest(model: str) -> Manifest:
    return Manifest(model, model, "f", "x:Y", (StageType.SHAPE,), License("MIT", True))


def _item(model: str, seq: int, priority: int = BATCH) -> WorkItem:
    ctx = StageContext("s", StageType.SHAPE, [], {}, 0, None)  # type: ignore[arg-type]
    return WorkItem(f"r/{seq}", ctx, _manifest(model), priority, lambda a, b: None, seq=seq)


async def test_prefers_loaded_model_then_fifo():
    lane = DeviceLane("cuda:0", VramManager(1 << 34), WorkerPool(), fairness_cap=8, in_process=True)
    lane.current_model = "paint"
    for i, model in enumerate(["shape", "paint", "shape", "paint"]):
        lane.ready.append(_item(model, i))
    assert [lane.pick().seq for _ in range(4)][:2] == [1, 3]


async def test_fairness_cap_lets_other_models_run():
    lane = DeviceLane("cuda:0", VramManager(1 << 34), WorkerPool(), fairness_cap=2, in_process=True)
    lane.current_model, lane.streak = "paint", 2
    lane.ready += [_item("shape", 0), _item("paint", 1)]
    assert lane.pick().model == "shape"


async def test_interactive_jumps_the_queue():
    lane = DeviceLane("cuda:0", VramManager(1 << 34), WorkerPool(), fairness_cap=8, in_process=True)
    lane.current_model = "paint"
    lane.ready += [_item("paint", 0), _item("shape", 1, INTERACTIVE)]
    assert lane.pick().seq == 1
    await asyncio.sleep(0)


def test_nvidia_smi_parsing():
    from forja_engine.devices import parse_nvidia_smi

    devs = parse_nvidia_smi("0, NVIDIA GeForce RTX 4070 Ti, 12282, 560.35.03, 8.9\n")
    assert (
        devs[0].id == "cuda:0" and devs[0].vram_bytes == 12282 << 20 and devs[0].compute_capability == "8.9"
    )


def test_cuda_models_are_placed_on_the_gpu_lane():
    from forja_engine.registry import Registry
    from forja_engine.scheduler import Scheduler

    reg = Registry.load()
    s = Scheduler({"cuda:0": 11 << 30, "cpu": 8 << 30})
    assert s.place(reg.models["triposg"]) == "cuda:0"
    assert s.place(reg.models["stub-shape"]) == "cpu"


async def _until(condition, seconds: float = 30.0) -> None:
    deadline = time.monotonic() + seconds
    while not condition():
        assert time.monotonic() < deadline, "timed out"
        await asyncio.sleep(0.02)


def _framing(tmp_path, name: str) -> StageContext:
    src = tmp_path / f"{name}.png"
    img = Image.new("RGBA", (32, 32), (0, 0, 0, 0))
    img.paste((200, 40, 40, 255), (8, 8, 24, 24))
    img.save(src)
    out = tmp_path / name
    out.mkdir()
    return StageContext(name, StageType.FRAMING, [StageInput(src, ArtifactKind.IMAGE_PNG, "h")], {}, 0, out)


async def test_a_process_pool_broken_by_one_stage_still_runs_the_next(tmp_path):
    """A pool process that dies used to fail every later built-in stage until the engine restarted."""
    s = scheduler.Scheduler({"cpu": 1 << 30}, cpu_workers=1)
    try:
        with pytest.raises(BrokenProcessPool):
            await asyncio.get_running_loop().run_in_executor(s.pool, misbehaving.die)
        result = await s.submit("r/next", _framing(tmp_path, "next"), None, BATCH, lambda a, b: None)
        assert result["outputs"][0]["kind"] == "image/png"
    finally:
        await s.stop()


async def test_a_builtin_stage_whose_process_dies_fails_with_words_and_frees_the_pool(tmp_path, monkeypatch):
    s = scheduler.Scheduler({"cpu": 1 << 30}, cpu_workers=1)
    try:
        with monkeypatch.context() as patched:
            patched.setattr(scheduler, "_run_builtin", misbehaving.die)
            with pytest.raises(StageFailed, match="stopped unexpectedly"):
                await s.submit("r/dies", _framing(tmp_path, "dies"), None, BATCH, lambda a, b: None)
        result = await s.submit("r/next", _framing(tmp_path, "next"), None, BATCH, lambda a, b: None)
        assert result["outputs"]
    finally:
        await s.stop()


def _model(adapter: str) -> Manifest:
    return Manifest(
        f"test-{adapter.lower()}",
        adapter,
        "misbehaving",
        f"misbehaving:{adapter}",
        (StageType.SHAPE,),
        License("MIT", True),
        requirements={"cpu": {"status": "supported"}},
        python=sys.executable,
    )


async def test_cancelling_a_stage_whose_model_is_still_loading_never_runs_it(tmp_path, misbehaving_models):
    """The worker learns of a stage only once its model is loaded, so a cancel sent before that
    reached nobody: the stage ran in full, holding the device, for a run that was gone."""
    s = scheduler.Scheduler({"cpu": 8 << 30}, cpu_workers=1)
    s.start()
    marker = tmp_path / "ran"
    out = tmp_path / "out"
    out.mkdir()
    ctx = StageContext("s", StageType.SHAPE, [], {"marker": str(marker)}, 0, out)
    try:
        stage = asyncio.create_task(s.submit("r/s", ctx, _model("SlowToLoad"), BATCH, lambda a, b: None))
        await _until(lambda: s.lanes["cpu"].running is not None)
        await s.cancel("r/s")
        with pytest.raises(StageFailed) as failed:
            await asyncio.wait_for(stage, 60)
        assert failed.value.cancelled
        assert not marker.exists()
    finally:
        await s.stop()


async def test_freeing_memory_leaves_the_model_a_running_stage_holds(tmp_path, misbehaving_models):
    """ "Free GPU memory now" during a job waited for the running stage's worker to answer, so the
    request hung until the stage ended, and then unloaded the model the job's next stage needs."""
    s = scheduler.Scheduler({"cpu": 8 << 30}, cpu_workers=1)
    s.start()
    lane = s.lanes["cpu"]
    marker = tmp_path / "how-it-ended"
    out = tmp_path / "out"
    out.mkdir()
    ctx = StageContext("s", StageType.SHAPE, [], {"marker": str(marker), "seconds": 4}, 0, out)
    steps = []
    try:
        stage = asyncio.create_task(
            s.submit("r/s", ctx, _model("Slow"), BATCH, lambda step, total: steps.append(step))
        )
        await _until(lambda: bool(steps))
        await asyncio.wait_for(s.unload_all(), 2)
        assert "test-slow" in lane.vram.loaded
        await asyncio.wait_for(stage, 60)
        assert marker.read_text(encoding="utf-8") == "finished"
        # With nothing running, it frees everything.
        await s.unload_all()
        assert not lane.vram.loaded
    finally:
        await s.stop()
