import sys
import types
from pathlib import Path

import pytest

# Family adapter packages live outside the engine package; make them importable in tests.
for src in (Path(__file__).parents[1] / "families").glob("*/src"):
    sys.path.insert(0, str(src))

GIB = 1 << 30


@pytest.fixture
def fake_mac(monkeypatch):
    """This process as an Apple Silicon Mac (M2 Pro, 32 GB) to the engine's hardware probes.

    Only the probes' own module references change (devices, families), so the rest of the
    process keeps running as what it is. The values are what such a Mac reports: Metal's
    recommended working set is two thirds of its RAM.
    """
    from forja_engine import devices, families

    mac = types.SimpleNamespace(
        chip="Apple M2 Pro",
        ram=32 * GIB,
        working_set=22_906_503_168,
        macos="15.1",
        translated="0",
    )
    sysctl = {"machdep.cpu.brand_string": lambda: mac.chip, "sysctl.proc_translated": lambda: mac.translated}
    host = types.SimpleNamespace(
        system=lambda: "Darwin",
        machine=lambda: "arm64",
        processor=lambda: "arm",
        mac_ver=lambda: (mac.macos, ("", "", ""), "arm64"),
    )
    memory = types.SimpleNamespace(
        virtual_memory=lambda: types.SimpleNamespace(total=mac.ram, available=20 * GIB)
    )
    for module in (devices, families):
        monkeypatch.setattr(module, "sys", types.SimpleNamespace(platform="darwin"))
        monkeypatch.setattr(module, "platform", host)
    monkeypatch.setattr(devices, "psutil", memory)
    monkeypatch.setattr(devices, "_sysctl", lambda name: sysctl[name]() if name in sysctl else None)
    monkeypatch.setattr(devices, "_metal_working_set", lambda: mac.working_set)
    monkeypatch.setattr(devices, "_nvidia_devices", list)
    monkeypatch.setattr(devices, "_disk_free", lambda root: 500 * GIB)
    # What PyPI's onnxruntime reports on macOS.
    monkeypatch.setattr(
        devices,
        "_onnx_providers",
        lambda: ["CoreMLExecutionProvider", "AzureExecutionProvider", "CPUExecutionProvider"],
    )
    return mac
