from __future__ import annotations

import abc
from collections.abc import Callable
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from typing import Any


class StageType(StrEnum):
    BACKGROUND_REMOVAL = "background-removal"
    FRAMING = "framing"
    SHAPE = "shape"
    MESH_CLEANUP = "mesh.cleanup"
    MESH_DECIMATE = "mesh.decimate"
    MESH_REMESH = "mesh.remesh"
    MESH_UV = "mesh.uv"
    MESH_UV_SMART = "mesh.uv.smart"
    TEXTURE = "texture"
    TEXTURE_MATCH = "texture.match"
    BAKE = "bake"
    MESH_LODS = "mesh.lods"
    EXPORT = "export"
    VIEWS_DETECT = "views.detect"
    VIEWS_SPLIT = "views.split"
    # The baked low-poly in; a skinned GLB and its skeleton profile (JSON) out (API 1.7).
    RIG = "rig"


#: The views a reference image can show. "left" shows the subject's left side, so it faces the
#: image's left edge (as in Hunyuan3D-2mv's examples); "right" is the mirror of that.
VIEWS = ("front", "left", "back", "right")


class ArtifactKind(StrEnum):
    IMAGE_PNG = "image/png"
    MESH_GLB = "mesh/glb"
    JSON = "application/json"


class Device(StrEnum):
    CUDA = "cuda"
    MPS = "mps"
    ROCM = "rocm"
    CPU = "cpu"


class Cancelled(Exception):
    """Raised by `StageContext.check_cancel` when the stage was cancelled."""


@dataclass(frozen=True)
class StageInput:
    path: Path
    kind: ArtifactKind
    hash: str
    # Which view of the subject an image shows (one of VIEWS), when the job has several.
    view: str | None = None


@dataclass(frozen=True)
class StageOutput:
    path: Path
    kind: ArtifactKind
    # True for intermediate results worth showing before the job ends (the untextured mesh).
    preview: bool = False


@dataclass(frozen=True)
class Capabilities:
    tasks: tuple[StageType, ...]
    devices: tuple[Device, ...]
    # Peak VRAM in bytes at default params; the VRAM manager replaces it with measured peaks.
    vram_bytes: int = 0


@dataclass
class StageContext:
    stage_id: str
    task: StageType
    inputs: list[StageInput]
    params: dict[str, Any]
    seed: int
    out_dir: Path
    progress: Callable[[int, int], None] = field(default=lambda step, total: None)
    cancelled: Callable[[], bool] = field(default=lambda: False)

    def check_cancel(self) -> None:
        if self.cancelled():
            raise Cancelled(self.stage_id)

    def input(self, kind: ArtifactKind | None = None, index: int = 0) -> StageInput:
        matches = [i for i in self.inputs if kind is None or i.kind == kind]
        if len(matches) <= index:
            raise ValueError(f"stage {self.stage_id} needs input #{index} of kind {kind}")
        return matches[index]

    def views(self) -> dict[str, StageInput]:
        """Reference images by view. An untagged image is the front; the first one wins."""
        out: dict[str, StageInput] = {}
        for i in self.inputs:
            if i.kind == ArtifactKind.IMAGE_PNG:
                out.setdefault(i.view or "front", i)
        return out


class Adapter(abc.ABC):
    """One model family. Lives in its own worker process and virtual environment."""

    #: Bumped whenever the adapter's output changes for the same inputs; part of the cache key.
    #: A model's manifest mirrors it as `adapter_version`, which is what the core reads.
    version: str = "1"
    #: Where install put this model's pinned weights. Set by the worker before `load`.
    weights_dir: Path | None = None
    #: Set by the worker before `load` when the model does not fit the device in fit-first
    #: mode. Adapters with a low-memory path (CPU offload, tiling) take it in `load`, before
    #: any weights reach the device; others may ignore it and run as usual.
    offload: bool = False

    @abc.abstractmethod
    def capabilities(self) -> Capabilities: ...

    def load(self, device: Device, dtype: str = "fp16") -> None:  # noqa: B027 - optional hook
        """Load weights onto the device. Called once; the worker keeps the model warm."""

    def unload(self) -> None:  # noqa: B027 - optional hook
        """Free device memory."""

    def estimate_vram(self, params: dict[str, Any]) -> int:
        return self.capabilities().vram_bytes

    @abc.abstractmethod
    def run(self, ctx: StageContext) -> list[StageOutput]: ...
