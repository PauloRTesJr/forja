"""Per-device VRAM budget. Pure bookkeeping: it decides, the scheduler acts."""

from __future__ import annotations

from collections import OrderedDict
from dataclasses import dataclass
from enum import StrEnum


class FitMode(StrEnum):
    FIT_FIRST = "fit-first"  # run anyway with the adapter's fallbacks (offload, tiling)
    QUALITY_FIRST = "quality-first"  # refuse and suggest another provider


class DoesNotFit(Exception):
    def __init__(self, model: str, need: int, budget: int) -> None:
        gb = need / (1 << 30)
        super().__init__(
            f"{model} needs about {gb:.1f}GB but this device has {budget / (1 << 30):.1f}GB for models; "
            "route this stage to a remote provider or switch fit_mode to fit-first"
        )
        self.model, self.need, self.budget = model, need, budget


@dataclass(frozen=True)
class LoadPlan:
    evict: list[str]
    offload: bool  # True when the model only runs with the adapter's low-memory fallbacks


class VramManager:
    def __init__(self, budget_bytes: int, fit_mode: FitMode = FitMode.FIT_FIRST) -> None:
        self.budget = budget_bytes
        self.fit_mode = fit_mode
        # Bytes each loaded model holds on the device, least recently used first.
        self.loaded: OrderedDict[str, int] = OrderedDict()
        # What a model needs to run without offload; offloaded runs never lower it, or the
        # next load would believe the model fits and overflow again.
        self.measured: dict[str, int] = {}
        self.offloaded: set[str] = set()
        self.device_measured: set[str] = set()

    def need(self, model: str, estimate: int) -> int:
        return self.measured.get(model, estimate)

    def available(self, external: int = 0) -> int:
        """Budget left for models once other applications' current use is taken out."""
        return max(0, self.budget - external)

    def plan(self, model: str, estimate: int, external: int = 0) -> LoadPlan:
        """external: device bytes in use by other processes right now (desktop, games)."""
        budget = self.available(external)
        if model in self.loaded:
            return LoadPlan(evict=[], offload=model in self.offloaded)
        need = self.need(model, estimate)
        if need > budget:
            if self.fit_mode is FitMode.QUALITY_FIRST:
                raise DoesNotFit(model, need, budget)
            return LoadPlan(evict=list(self.loaded), offload=True)
        evict: list[str] = []
        free = budget - sum(self.loaded.values())
        for victim, size in self.loaded.items():
            if free >= need:
                break
            evict.append(victim)
            free += size
        return LoadPlan(evict=evict, offload=False)

    def did_load(self, model: str, estimate: int, offloaded: bool = False) -> None:
        self.loaded[model] = estimate if offloaded else self.need(model, estimate)
        self.loaded.move_to_end(model)
        if offloaded:
            self.offloaded.add(model)
        else:
            self.offloaded.discard(model)

    def did_unload(self, model: str) -> None:
        self.loaded.pop(model, None)
        self.offloaded.discard(model)

    def touch(self, model: str) -> None:
        if model in self.loaded:
            self.loaded.move_to_end(model)

    def record_resident(self, model: str, resident: int) -> None:
        """Bytes the model holds on the device between runs, as measured on the device."""
        self.device_measured.add(model)
        if model in self.loaded:
            self.loaded[model] = resident
        if model not in self.offloaded:
            self.measured[model] = max(self.measured.get(model, 0), resident)

    def record_peak(self, model: str, peak: int) -> None:
        """The runtime's own peak (torch); device measurements, when present, already cover it."""
        if model in self.offloaded:
            return
        self.measured[model] = peak
        if model in self.loaded and model not in self.device_measured:
            self.loaded[model] = max(self.loaded[model], peak)
