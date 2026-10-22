import asyncio

from forja_engine.registry import License, Manifest
from forja_engine.scheduler import BATCH, INTERACTIVE, DeviceLane, WorkItem
from forja_engine.sdk import StageContext, StageType
from forja_engine.vram import VramManager
from forja_engine.workers import WorkerPool


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
