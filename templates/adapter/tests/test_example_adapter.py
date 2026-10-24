"""Tests for the example adapter, in the shape a real family's tests take.

A real model needs a GPU and gigabytes of weights, so its tests swap the upstream pipeline
for a fake and pin down the glue the adapter owns: inputs, params, progress, cancellation
and the output file. The conformance suite then checks the adapter against the SDK.
"""

from pathlib import Path

import numpy as np
import pytest
from forja_sdk import ArtifactKind, Cancelled, Device, StageContext, StageInput, StageType
from forja_sdk.artifacts import read_glb_info
from forja_sdk.conformance import assert_conforms, load_manifest
from forja_sdk.samples import subject_png

from forja_example import ExampleExtrude

PACKAGE = Path(__file__).parents[1]
MANIFEST = PACKAGE / "manifests" / "example-extrude.yaml"


def context(tmp_path: Path, cancel_at: int | None = None, **params: object) -> tuple[StageContext, list]:
    image = subject_png(tmp_path / "cutout.png", alpha=True)
    steps: list[tuple[int, int]] = []

    def progress(step: int, total: int) -> None:
        steps.append((step, total))

    ctx = StageContext(
        "shape",
        StageType.SHAPE,
        [StageInput(image, ArtifactKind.IMAGE_PNG, "")],
        dict(params),
        7,
        tmp_path,
        progress=progress,
        cancelled=lambda: cancel_at is not None and len(steps) >= cancel_at,
    )
    return ctx, steps


class FakePipeline:
    """Stands in for the upstream model: records what the adapter hands it."""

    def __init__(self) -> None:
        self.calls: list[tuple[np.ndarray, float]] = []

    def __call__(self, mask, depth, step):
        self.calls.append((mask, depth))
        step()
        verts = np.array([[0, 0, 0], [1, 0, 0], [0, 1, 0], [0, 0, 1]], dtype=np.float64)
        return verts, np.array([[0, 2, 1], [0, 1, 3], [0, 3, 2], [1, 2, 3]])


def test_adapter_hands_the_pipeline_the_mask_and_params(tmp_path):
    adapter = ExampleExtrude()
    adapter.load(Device.CPU)
    adapter.pipeline = fake = FakePipeline()
    ctx, steps = context(tmp_path, resolution=32, depth=0.5)
    (out,) = adapter.run(ctx)
    mask, depth = fake.calls[0]
    assert mask.shape == (32, 32) and mask.any() and not mask.all()  # the subject, not the backdrop
    assert depth == 0.5
    assert steps == [(1, 3), (2, 3), (3, 3)]
    assert out.kind == ArtifactKind.MESH_GLB and out.preview
    assert read_glb_info(out.path.read_bytes()).triangles == 4


def test_cancel_stops_between_steps(tmp_path):
    adapter = ExampleExtrude()
    adapter.load(Device.CPU)
    adapter.pipeline = fake = FakePipeline()
    ctx, _ = context(tmp_path, cancel_at=1)
    with pytest.raises(Cancelled):
        adapter.run(ctx)
    assert fake.calls == []  # cancelled before the pipeline ran


def test_real_run_makes_a_closed_slab(tmp_path):
    adapter = ExampleExtrude()
    adapter.load(Device.CPU)
    ctx, _ = context(tmp_path, resolution=48, depth=0.2)
    (out,) = adapter.run(ctx)
    info = read_glb_info(out.path.read_bytes())
    assert info.triangles > 1000 and not info.has_uv


def test_conforms_to_the_sdk(tmp_path):
    report = assert_conforms(ExampleExtrude, manifest=load_manifest(MANIFEST), work_dir=tmp_path)
    assert report.result("cancellation[shape]").status == "pass"
