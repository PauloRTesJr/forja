"""Runtime hardware detection. Never assume a backend: probe it."""

from __future__ import annotations

import importlib.util
import platform
import shutil
from dataclasses import asdict, dataclass, field
from pathlib import Path

import psutil

from forja_engine.sdk import Device


@dataclass(frozen=True)
class DeviceInfo:
    id: str  # "cuda:0", "mps", "cpu"
    backend: Device
    name: str
    vram_bytes: int
    compute_capability: str | None = None
    driver: str | None = None


@dataclass(frozen=True)
class SystemReport:
    os: str
    arch: str
    devices: list[DeviceInfo] = field(default_factory=list)
    ram_total_bytes: int = 0
    ram_free_bytes: int = 0
    disk_free_bytes: int = 0
    onnx_providers: list[str] = field(default_factory=list)

    def to_json(self) -> dict[str, object]:
        return asdict(self)


def detect(store_root: Path) -> SystemReport:
    devices = _torch_devices()
    mem = psutil.virtual_memory()
    devices.append(DeviceInfo("cpu", Device.CPU, platform.processor() or platform.machine(), mem.total))
    return SystemReport(
        os=platform.system().lower(),
        arch=platform.machine().lower(),
        devices=devices,
        ram_total_bytes=mem.total,
        ram_free_bytes=mem.available,
        disk_free_bytes=shutil.disk_usage(store_root).free,
        onnx_providers=_onnx_providers(),
    )


def _torch_devices() -> list[DeviceInfo]:
    # torch lives in model-family environments, not necessarily in the supervisor's.
    if importlib.util.find_spec("torch") is None:
        return []
    import torch

    found: list[DeviceInfo] = []
    if torch.cuda.is_available():
        backend = Device.ROCM if getattr(torch.version, "hip", None) else Device.CUDA
        for i in range(torch.cuda.device_count()):
            props = torch.cuda.get_device_properties(i)
            found.append(
                DeviceInfo(
                    f"cuda:{i}",
                    backend,
                    props.name,
                    props.total_memory,
                    compute_capability=f"{props.major}.{props.minor}",
                    driver=torch.version.cuda or getattr(torch.version, "hip", None),
                )
            )
    elif getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
        # Apple Silicon shares memory; report the recommended working set as the budget.
        found.append(DeviceInfo("mps", Device.MPS, "Apple GPU", psutil.virtual_memory().total * 2 // 3))
    return found


def _onnx_providers() -> list[str]:
    if importlib.util.find_spec("onnxruntime") is None:
        return []
    import onnxruntime

    return list(onnxruntime.get_available_providers())
