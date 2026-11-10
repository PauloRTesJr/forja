"""Adapters that go wrong the way real models do, for tests of what the engine does about it.

Workers import them by name, so tests put this folder on PYTHONPATH (the `misbehaving_models` fixture).
"""

import os
import sys
import time
from pathlib import Path

from forja_engine.sdk import (
    Adapter,
    ArtifactKind,
    Cancelled,
    Capabilities,
    Device,
    StageContext,
    StageOutput,
    StageType,
)

MANIFEST = """
id: {id}
family: misbehaving
adapter: misbehaving:{adapter}
tasks: [shape]
license: {{ spdx: MIT, commercial: true }}
requirements:
  cpu: {{ status: supported }}
python: '{python}'
"""


def write_manifests(folder: Path) -> Path:
    """One model per adapter here, named test-<adapter>, run by this interpreter."""
    folder.mkdir(parents=True, exist_ok=True)
    for adapter in ("DiesOnLoad", "FailsToLoad", "SlowToLoad", "Slow"):
        doc = MANIFEST.format(id=f"test-{adapter.lower()}", adapter=adapter, python=sys.executable)
        (folder / f"{adapter.lower()}.yaml").write_text(doc, encoding="utf-8")
    return folder


def die(*_: object) -> None:
    """Stands in for a built-in stage whose native code takes its process down."""
    os._exit(3)


def _done(ctx: StageContext) -> list[StageOutput]:
    out = ctx.out_dir / "out.json"
    out.write_text("{}", encoding="utf-8")
    return [StageOutput(out, ArtifactKind.JSON)]


class _Model(Adapter):
    def capabilities(self) -> Capabilities:
        return Capabilities(tasks=(StageType.SHAPE,), devices=(Device.CPU,))

    def run(self, ctx: StageContext) -> list[StageOutput]:
        return _done(ctx)


class DiesOnLoad(_Model):
    def load(self, device: Device, dtype: str = "fp16") -> None:
        print("CUDA error: the launch ran out of device memory", file=sys.stderr, flush=True)
        os._exit(3)


class FailsToLoad(_Model):
    def load(self, device: Device, dtype: str = "fp16") -> None:
        raise KeyError("unet")


class SlowToLoad(_Model):
    """Takes a while to load; leaves `marker` behind when it runs."""

    def load(self, device: Device, dtype: str = "fp16") -> None:
        time.sleep(1.5)

    def run(self, ctx: StageContext) -> list[StageOutput]:
        Path(ctx.params["marker"]).write_text("ran", encoding="utf-8")
        return _done(ctx)


class Slow(_Model):
    """Works for `seconds`, looking for a cancel ten times a second; `marker` says how it ended."""

    def run(self, ctx: StageContext) -> list[StageOutput]:
        marker = Path(ctx.params["marker"])
        steps = int(float(ctx.params.get("seconds", 8)) * 10)
        try:
            for step in range(steps):
                ctx.progress(step, steps)
                ctx.check_cancel()
                time.sleep(0.1)
        except Cancelled:
            marker.write_text("cancelled", encoding="utf-8")
            raise
        marker.write_text("finished", encoding="utf-8")
        return _done(ctx)
