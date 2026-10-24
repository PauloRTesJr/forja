"""Hardware detection on an Apple Silicon Mac: faked here, real under the mps marker."""

import importlib.machinery
import platform
import sys
import types

import pytest

from forja_engine import checks, devices, families
from forja_engine.registry import Registry
from forja_engine.sdk import Device

GIB = 1 << 30
APPLE_SILICON = sys.platform == "darwin" and platform.machine() == "arm64"


def test_the_apple_gpu_budget_is_metals_working_set():
    gpu = devices.apple_gpu("Apple M2 Pro", 32 * GIB, 22_906_503_168, "15.1")
    assert (gpu.id, gpu.backend, gpu.name) == ("mps", Device.MPS, "Apple M2 Pro GPU")
    assert gpu.vram_bytes == 22_906_503_168 and gpu.driver == "macOS 15.1"


def test_without_metal_the_budget_is_two_thirds_of_ram():
    assert devices.apple_gpu(None, 24 * GIB, None, None).vram_bytes == 16 * GIB
    assert devices.apple_gpu(None, 24 * GIB, 0, None).vram_bytes == 16 * GIB
    # A working set larger than RAM is not believed.
    assert devices.apple_gpu(None, 24 * GIB, 48 * GIB, None).vram_bytes == 16 * GIB
    assert devices.apple_gpu(None, 8 * GIB, None, None).name == "Apple GPU"


def test_a_mac_reports_its_apple_gpu_then_its_cpu(fake_mac, tmp_path):
    report = devices.detect(tmp_path)
    assert (report.os, report.arch) == ("darwin", "arm64")
    assert [(d.id, d.backend) for d in report.devices] == [("mps", Device.MPS), ("cpu", Device.CPU)]
    gpu, cpu = report.devices
    assert gpu.name == "Apple M2 Pro GPU" and gpu.vram_bytes == fake_mac.working_set
    # Unified memory: the CPU sees all of it, the GPU its working set.
    assert cpu.name == "Apple M2 Pro" and cpu.vram_bytes == report.ram_total_bytes == 32 * GIB
    assert "CoreMLExecutionProvider" in report.onnx_providers


def test_an_intel_mac_has_no_apple_gpu(fake_mac, tmp_path, monkeypatch):
    for module in (devices, families):
        monkeypatch.setattr(module.platform, "machine", lambda: "x86_64")
    assert [d.id for d in devices.detect(tmp_path).devices] == ["cpu"]
    assert families.detect_lane() == "cpu"


def test_torch_finds_the_apple_gpu_when_metal_cannot_be_asked(fake_mac, tmp_path, monkeypatch):
    torch = types.ModuleType("torch")
    torch.__spec__ = importlib.machinery.ModuleSpec("torch", None)
    torch.cuda = types.SimpleNamespace(is_available=lambda: False)
    torch.backends = types.SimpleNamespace(mps=types.SimpleNamespace(is_available=lambda: True))
    torch.mps = types.SimpleNamespace(recommended_max_memory=lambda: 21 * GIB)
    monkeypatch.setitem(sys.modules, "torch", torch)
    (gpu,) = devices._torch_devices()
    assert gpu.id == "mps" and gpu.vram_bytes == 21 * GIB and gpu.name == "Apple M2 Pro GPU"


def test_apple_silicon_installs_the_mps_lane(fake_mac):
    assert families.detect_lane() == "mps"


def test_rosetta_is_named_where_the_apple_gpu_is_needed(fake_mac, tmp_path, monkeypatch):
    for module in (devices, families):
        monkeypatch.setattr(module.platform, "machine", lambda: "x86_64")
    fake_mac.translated = "1"
    assert devices.translated()
    reg = Registry.load()
    found = {c.id: c for c in checks.for_models([reg.models["hunyuan3d2-mv"]], tmp_path)}
    check = found["runs-here:hunyuan3d2-mv"]
    assert not check.ok and "Rosetta" in check.found and check.fix and "Apple Silicon" in check.fix.label
    # BiRefNet runs on the CPU, which Rosetta still has.
    assert "runs-here:birefnet" not in {c.id for c in checks.for_models([reg.models["birefnet"]], tmp_path)}


# --- on a real Apple Silicon Mac ---------------------------------------------------------

mps = pytest.mark.skipif(not APPLE_SILICON, reason="needs an Apple Silicon Mac")


@pytest.mark.mps
@mps
def test_metal_answers_with_a_working_set_inside_ram():
    import psutil

    working_set = devices._metal_working_set()
    assert working_set is not None and 0 < working_set <= psutil.virtual_memory().total


@pytest.mark.mps
@mps
def test_this_mac_reports_its_apple_gpu(tmp_path):
    report = devices.detect(tmp_path)
    gpu = report.devices[0]
    assert gpu.id == "mps" and gpu.name.startswith("Apple M") and gpu.vram_bytes <= report.ram_total_bytes
    assert families.detect_lane() == "mps" and not devices.translated()
