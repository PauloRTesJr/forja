"""Runtime hardware detection. Never assume a backend: probe it."""

from __future__ import annotations

import importlib.util
import platform
import shutil
import subprocess
import sys
from dataclasses import asdict, dataclass, field
from functools import cache
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
    # The supervisor's environment has no torch (torch lives in each family's environment),
    # so GPUs are found through vendor tools first and torch only as a fallback.
    devices = _nvidia_devices() or _apple_devices() or _torch_devices()
    mem = psutil.virtual_memory()
    chip = _sysctl("machdep.cpu.brand_string") if sys.platform == "darwin" else None
    devices.append(
        DeviceInfo("cpu", Device.CPU, chip or platform.processor() or platform.machine(), mem.total)
    )
    return SystemReport(
        os=platform.system().lower(),
        arch=platform.machine().lower(),
        devices=devices,
        ram_total_bytes=mem.total,
        ram_free_bytes=mem.available,
        disk_free_bytes=_disk_free(store_root),
        onnx_providers=_onnx_providers(),
    )


def _disk_free(root: Path) -> int:
    return shutil.disk_usage(root).free


_NVIDIA_QUERY = "index,name,memory.total,driver_version,compute_cap"


def parse_nvidia_smi(output: str) -> list[DeviceInfo]:
    found = []
    for line in output.strip().splitlines():
        parts = [p.strip() for p in line.split(",")]
        if len(parts) < 5:
            continue
        index, name, mem_mib, driver, cap = parts[:5]
        found.append(
            DeviceInfo(
                f"cuda:{index}",
                Device.CUDA,
                name,
                int(float(mem_mib)) << 20,
                compute_capability=cap,
                driver=driver,
            )
        )
    return found


def _nvidia_devices() -> list[DeviceInfo]:
    smi = shutil.which("nvidia-smi")
    if not smi:
        return []
    try:
        out = subprocess.run(
            [smi, f"--query-gpu={_NVIDIA_QUERY}", "--format=csv,noheader,nounits"],
            capture_output=True,
            text=True,
            timeout=10,
            check=True,
        ).stdout
    except (OSError, subprocess.SubprocessError):
        return []
    return parse_nvidia_smi(out)


def apple_gpu(chip: str | None, ram_total: int, working_set: int | None, macos: str | None) -> DeviceInfo:
    """The Apple GPU of a Mac with `ram_total` bytes of unified memory.

    The GPU has no memory of its own, so its budget is Metal's recommended working set: what
    Metal lets the GPU keep resident while leaving the rest of RAM to macOS and applications.
    Without Metal's answer, the budget is two thirds of RAM: what Metal recommends on Macs up
    to 32 GB, and less than it recommends on larger ones.
    """
    budget = working_set if working_set and 0 < working_set <= ram_total else ram_total * 2 // 3
    name = f"{chip} GPU" if chip else "Apple GPU"
    # Metal ships with macOS, and MPS coverage (bf16 from macOS 14) follows its version.
    return DeviceInfo("mps", Device.MPS, name, budget, driver=f"macOS {macos}" if macos else None)


def translated() -> bool:
    """True when this process is an Intel build running under Rosetta on Apple Silicon."""
    return sys.platform == "darwin" and _sysctl("sysctl.proc_translated") == "1"


@cache
def _sysctl(name: str) -> str | None:
    try:
        out = subprocess.run(["sysctl", "-n", name], capture_output=True, text=True, timeout=5, check=True)
    except (OSError, subprocess.SubprocessError):
        return None
    return out.stdout.strip() or None


# MTLDevice.recommendedMaxWorkingSetSize through the Objective-C runtime, with no dependency.
_METAL_PROBE = """
import ctypes
# A process gets a default Metal device only once Core Graphics is loaded.
ctypes.CDLL("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
metal = ctypes.CDLL("/System/Library/Frameworks/Metal.framework/Metal")
objc = ctypes.CDLL("/usr/lib/libobjc.A.dylib")
metal.MTLCreateSystemDefaultDevice.restype = ctypes.c_void_p
objc.sel_registerName.restype = ctypes.c_void_p
objc.sel_registerName.argtypes = [ctypes.c_char_p]
device = metal.MTLCreateSystemDefaultDevice()
# objc_msgSend must be called through a prototype of the method's exact signature.
send = ctypes.CFUNCTYPE(ctypes.c_uint64, ctypes.c_void_p, ctypes.c_void_p)(("objc_msgSend", objc))
print(send(device, objc.sel_registerName(b"recommendedMaxWorkingSetSize")) if device else 0)
"""


@cache
def _metal_working_set() -> int | None:
    """Metal's recommended working set for the GPU, or None where Metal cannot answer.

    A native call that faults cannot be caught, so the probe runs in a child process.
    """
    try:
        out = subprocess.run(
            [sys.executable, "-I", "-c", _METAL_PROBE], capture_output=True, text=True, timeout=20, check=True
        ).stdout
        return int(out.strip()) or None
    except (OSError, subprocess.SubprocessError, ValueError):
        return None


def _apple_devices() -> list[DeviceInfo]:
    if sys.platform != "darwin" or platform.machine() != "arm64":
        return []
    return [
        apple_gpu(
            _sysctl("machdep.cpu.brand_string"),
            psutil.virtual_memory().total,
            _metal_working_set(),
            platform.mac_ver()[0] or None,
        )
    ]


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
        recommended = getattr(getattr(torch, "mps", None), "recommended_max_memory", None)
        found.append(
            apple_gpu(
                _sysctl("machdep.cpu.brand_string"),
                psutil.virtual_memory().total,
                int(recommended()) if recommended else None,
                platform.mac_ver()[0] or None,
            )
        )
    return found


def _onnx_providers() -> list[str]:
    if importlib.util.find_spec("onnxruntime") is None:
        return []
    import onnxruntime

    return list(onnxruntime.get_available_providers())
