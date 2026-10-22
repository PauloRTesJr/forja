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


def test_other_applications_shrink_the_budget():
    vm = VramManager(11 * GB)
    assert not vm.plan("shape", 9 * GB).offload
    assert vm.plan("shape", 9 * GB, external=int(2.5 * GB)).offload  # desktop and a game hold 2.5GB


def test_measured_footprint_evicts_a_model_that_declared_less():
    vm = VramManager(10 * GB)
    vm.did_load("bg", int(1.5 * GB))
    vm.record_resident("bg", 7 * GB)  # an ONNX Runtime arena kept what its first run grabbed
    assert vm.plan("shape", 8 * GB).evict == ["bg"]
    assert vm.need("bg", int(1.5 * GB)) == 7 * GB


def test_offloaded_runs_do_not_lower_the_need():
    vm = VramManager(10 * GB)
    vm.record_peak("paint", 12 * GB)
    plan = vm.plan("paint", 10 * GB)
    assert plan.offload
    vm.did_load("paint", 10 * GB, offloaded=True)
    vm.record_peak("paint", 3 * GB)
    vm.record_resident("paint", 2 * GB)
    vm.did_unload("paint")
    assert vm.plan("paint", 10 * GB).offload


def test_device_measurement_is_not_added_to_the_runtime_peak():
    vm = VramManager(11 * GB)
    vm.did_load("shape", 8 * GB)
    vm.record_resident("shape", 3 * GB)  # weights and context after load
    vm.record_peak("shape", int(7.5 * GB))  # torch's peak during the run
    vm.record_resident("shape", int(8.5 * GB))  # what the device holds after the run
    assert vm.loaded["shape"] == int(8.5 * GB) and vm.need("shape", 8 * GB) == int(8.5 * GB)
