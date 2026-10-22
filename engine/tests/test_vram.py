import pytest

from forja_engine.vram import DoesNotFit, FitMode, VramManager

GB = 1 << 30


def test_loads_without_eviction_when_it_fits():
    vm = VramManager(11 * GB)
    vm.did_load("bg", 1 * GB)
    assert vm.plan("shape", 8 * GB).evict == []


def test_evicts_least_recently_used_first():
    vm = VramManager(11 * GB)
    vm.did_load("shape", 8 * GB)
    vm.did_load("bg", 1 * GB)
    vm.touch("shape")
    plan = vm.plan("paint", 6 * GB)
    assert plan.evict == ["bg", "shape"]
    vm2 = VramManager(11 * GB)
    vm2.did_load("bg", 1 * GB)
    vm2.did_load("shape", 8 * GB)
    assert vm2.plan("paint", 3 * GB).evict == ["bg"]


def test_quality_first_refuses_with_an_actionable_message():
    vm = VramManager(11 * GB, FitMode.QUALITY_FIRST)
    with pytest.raises(DoesNotFit, match=r"21\.0GB"):
        vm.plan("hunyuan-2.1-paint", 21 * GB)


def test_fit_first_offloads_and_clears_the_device():
    vm = VramManager(11 * GB, FitMode.FIT_FIRST)
    vm.did_load("bg", 1 * GB)
    plan = vm.plan("hunyuan-2.1-paint", 21 * GB)
    assert plan.offload and plan.evict == ["bg"]


def test_measured_peak_replaces_estimate():
    vm = VramManager(11 * GB)
    vm.record_peak("shape", 10 * GB)
    vm.did_load("bg", 2 * GB)
    assert vm.plan("shape", 4 * GB).evict == ["bg"]
