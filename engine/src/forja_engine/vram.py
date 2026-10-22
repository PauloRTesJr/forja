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
        self.loaded: OrderedDict[str, int] = OrderedDict()  # least recently used first
        self.measured: dict[str, int] = {}

    def need(self, model: str, estimate: int) -> int:
        return self.measured.get(model, estimate)

    def plan(self, model: str, estimate: int) -> LoadPlan:
        if model in self.loaded:
            return LoadPlan(evict=[], offload=self.loaded[model] > self.budget)
        need = self.need(model, estimate)
        if need > self.budget:
            if self.fit_mode is FitMode.QUALITY_FIRST:
                raise DoesNotFit(model, need, self.budget)
            return LoadPlan(evict=list(self.loaded), offload=True)
        evict: list[str] = []
        free = self.budget - sum(self.loaded.values())
        for victim, size in self.loaded.items():
            if free >= need:
                break
            evict.append(victim)
            free += size
        return LoadPlan(evict=evict, offload=False)

    def did_load(self, model: str, estimate: int) -> None:
        self.loaded[model] = self.need(model, estimate)
        self.loaded.move_to_end(model)

    def did_unload(self, model: str) -> None:
        self.loaded.pop(model, None)

    def touch(self, model: str) -> None:
        if model in self.loaded:
            self.loaded.move_to_end(model)

    def record_peak(self, model: str, peak: int) -> None:
        self.measured[model] = peak
        if model in self.loaded:
            self.loaded[model] = peak
