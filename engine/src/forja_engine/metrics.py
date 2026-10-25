"""What a benchmark measures while a stage runs (runs that ask to `measure`, API 1.7).

Resident memory and GPU utilization have no event to wait for, so they are sampled: ten times
a second on a thread of its own, from the moment the stage starts until it returns.
"""

from __future__ import annotations

import threading
from collections.abc import Callable
from types import TracebackType

import psutil

HZ = 10.0


def rss_of(pid: int) -> Callable[[], int | None]:
    """A function returning the resident memory of process pid, or None once it is gone."""
    try:
        proc = psutil.Process(pid)
    except psutil.Error:
        return lambda: None

    def rss() -> int | None:
        try:
            return int(proc.memory_info().rss)
        except psutil.Error:
            return None

    return rss


class Sampler:
    """Samples `rss` and `util` until the block ends: the peak of one, the mean of the other."""

    def __init__(
        self,
        rss: Callable[[], int | None] | None = None,
        util: Callable[[], float | None] | None = None,
        hz: float = HZ,
    ) -> None:
        self._rss, self._util = rss, util
        self._interval = 1.0 / hz
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self.peak_ram_bytes: int | None = None
        self._util_sum, self._util_n = 0.0, 0

    @property
    def gpu_util(self) -> float | None:
        """Mean percent of time the GPU ran kernels; None when it could not be read."""
        return round(self._util_sum / self._util_n, 1) if self._util_n else None

    def _sample_rss(self) -> None:
        if self._rss and (rss := self._rss()) is not None:
            self.peak_ram_bytes = max(self.peak_ram_bytes or 0, rss)

    def _loop(self) -> None:
        while not self._stop.wait(self._interval):
            self._sample_rss()
            # NVML reports the last sample period, so only readings taken inside the stage count.
            if self._util and (util := self._util()) is not None:
                self._util_sum += util
                self._util_n += 1

    def __enter__(self) -> Sampler:
        if self._rss or self._util:
            self._sample_rss()  # a stage shorter than one interval still gets a reading
            self._thread = threading.Thread(target=self._loop, name="forja-sampler", daemon=True)
            self._thread.start()
        return self

    def __exit__(
        self, exc_type: type[BaseException] | None, exc: BaseException | None, tb: TracebackType | None
    ) -> None:
        if self._thread is None:
            return
        self._stop.set()
        self._thread.join()
        self._sample_rss()
