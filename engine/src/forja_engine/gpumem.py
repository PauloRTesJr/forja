"""Device memory in use and utilization, read from NVML through ctypes (no extra dependency).

Torch's own peak misses memory held by other runtimes (an ONNX Runtime arena keeps what its
cuDNN search grabbed) and by other applications (the desktop, a game engine sharing the GPU).
Over budget, a Windows driver does not fail: it pages to system RAM and the stage gets up to
ten times slower, so planning needs what the device really holds.
"""

from __future__ import annotations

import ctypes
import sys
from collections.abc import Callable

_lib: ctypes.CDLL | None = None
_tried = False


class _Memory(ctypes.Structure):
    _fields_ = [("total", ctypes.c_ulonglong), ("free", ctypes.c_ulonglong), ("used", ctypes.c_ulonglong)]


class _Utilization(ctypes.Structure):
    _fields_ = [("gpu", ctypes.c_uint), ("memory", ctypes.c_uint)]


def _nvml() -> ctypes.CDLL | None:
    global _lib, _tried
    if _tried:
        return _lib
    _tried = True
    name = "nvml.dll" if sys.platform == "win32" else "libnvidia-ml.so.1"
    try:
        lib = ctypes.CDLL(name)
        if lib.nvmlInit_v2() == 0:
            _lib = lib
    except (OSError, AttributeError):
        _lib = None
    return _lib


def _handle(device: str) -> tuple[ctypes.CDLL, ctypes.c_void_p] | None:
    backend, _, index = device.partition(":")
    lib = _nvml() if backend == "cuda" else None
    if lib is None:
        return None
    handle = ctypes.c_void_p()
    if lib.nvmlDeviceGetHandleByIndex_v2(ctypes.c_uint(int(index or 0)), ctypes.byref(handle)) != 0:
        return None
    return lib, handle


def reader(device: str) -> Callable[[], int] | None:
    """A function returning bytes in use on device ("cuda:0"), or None where NVML is absent."""
    found = _handle(device)
    if found is None:
        return None
    lib, handle = found

    def used() -> int:
        mem = _Memory()
        if lib.nvmlDeviceGetMemoryInfo(handle, ctypes.byref(mem)) != 0:
            return 0
        return int(mem.used)

    return used


def utilization(device: str) -> Callable[[], float | None] | None:
    """A function returning the percent of the last sample period the device ran kernels, or
    None where NVML is absent."""
    found = _handle(device)
    if found is None:
        return None
    lib, handle = found

    def busy() -> float | None:
        util = _Utilization()
        if lib.nvmlDeviceGetUtilizationRates(handle, ctypes.byref(util)) != 0:
            return None
        return float(util.gpu)

    return busy
