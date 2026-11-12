"""Conformance suite: does an adapter keep the SDK's contract?

Run it from a shell (`forja-sdk test my_pkg:MyAdapter`, or a manifest path) or from pytest
(`assert_conforms(MyAdapter)`). It checks, in this order:

- capabilities: tasks and devices are declared, `version` is set.
- manifest (when given): tasks, adapter_version, devices, license metadata and parameter
  defaults agree with the adapter.
- load: `load(device, dtype)` succeeds.
- run[task]: outputs exist, sit in `out_dir`, are valid files of their kind, and the primary
  output is what the next stage needs (a cut-out with alpha, a mesh with triangles, a
  textured mesh with UVs); inputs are left unchanged.
- progress[task]: reported steps stay within 0..total and never go back.
- determinism[task]: the same inputs, params and seed give byte-identical outputs.
- cancellation[task]: when cancel is requested after a step, `run` raises `Cancelled`.
- vram: `estimate_vram` is a byte count, and covers the peak PyTorch measures on a GPU.
- reload: after `unload()` and `load()` the adapter runs again with the same outputs.
- unload: `unload()` succeeds.
"""

from __future__ import annotations

import hashlib
import importlib
import importlib.util
import json
import math
import re
import shutil
import tempfile
import time
from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Literal

from forja_sdk import artifacts, samples
from forja_sdk.types import (
    Adapter,
    ArtifactKind,
    Cancelled,
    Capabilities,
    Device,
    StageContext,
    StageInput,
    StageOutput,
    StageType,
)

Status = Literal["pass", "fail", "skip"]
AdapterSource = str | type[Adapter] | Callable[[], Adapter]
GB = 1 << 30

#: What the next stage reads from a stage's primary (first) output.
_EXPECT: dict[StageType, tuple[ArtifactKind, str]] = {
    StageType.BACKGROUND_REMOVAL: (ArtifactKind.IMAGE_PNG, "alpha"),
    StageType.FRAMING: (ArtifactKind.IMAGE_PNG, "alpha"),
    StageType.SHAPE: (ArtifactKind.MESH_GLB, "mesh"),
    StageType.MESH_CLEANUP: (ArtifactKind.MESH_GLB, "mesh"),
    StageType.MESH_DECIMATE: (ArtifactKind.MESH_GLB, "mesh"),
    StageType.MESH_UV: (ArtifactKind.MESH_GLB, "uv"),
    StageType.TEXTURE: (ArtifactKind.MESH_GLB, "textured"),
    StageType.TEXTURE_MATCH: (ArtifactKind.MESH_GLB, "textured"),
    StageType.BAKE: (ArtifactKind.MESH_GLB, "textured"),
    StageType.MESH_LODS: (ArtifactKind.MESH_GLB, "mesh"),
    StageType.EXPORT: (ArtifactKind.MESH_GLB, "mesh"),
    StageType.VIEWS_DETECT: (ArtifactKind.JSON, ""),
    StageType.VIEWS_SPLIT: (ArtifactKind.IMAGE_PNG, ""),
    # The skinned mesh first, then the skeleton profile; the rig keeps the baked textures.
    StageType.RIG: (ArtifactKind.MESH_GLB, "textured"),
    StageType.MESH_REMESH: (ArtifactKind.MESH_GLB, "mesh"),
    StageType.MESH_UV_SMART: (ArtifactKind.MESH_GLB, "uv"),
}
_PARAM_TYPES: dict[str, tuple[type, ...]] = {
    "integer": (int,),
    "number": (int, float),
    "boolean": (bool,),
    "string": (str,),
}


@dataclass(frozen=True)
class Result:
    name: str
    status: Status
    detail: str = ""


@dataclass
class Report:
    adapter: str
    device: str = ""
    results: list[Result] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return not self.failures

    @property
    def failures(self) -> list[Result]:
        return [r for r in self.results if r.status == "fail"]

    def result(self, name: str) -> Result | None:
        return next((r for r in self.results if r.name == name), None)

    def format(self) -> str:
        width = max([len(r.name) for r in self.results] + [4])
        lines = [f"{self.adapter} on {self.device or '?'}"]
        lines += [f"  {r.status.upper():4}  {r.name:<{width}}  {r.detail}".rstrip() for r in self.results]
        passed, failed = sum(r.status == "pass" for r in self.results), len(self.failures)
        lines.append(f"{passed} passed, {failed} failed, {len(self.results) - passed - failed} skipped")
        return "\n".join(lines)

    def to_json(self) -> dict[str, Any]:
        return {
            "adapter": self.adapter,
            "device": self.device,
            "ok": self.ok,
            "results": [{"name": r.name, "status": r.status, "detail": r.detail} for r in self.results],
        }


def load_manifest(path: Path) -> dict[str, Any]:
    try:
        import yaml
    except ImportError as e:  # the SDK itself needs nothing beyond the standard library
        raise RuntimeError("reading a manifest needs PyYAML: pip install pyyaml") from e
    doc = yaml.safe_load(Path(path).read_text(encoding="utf-8"))
    if not isinstance(doc, dict):
        raise ValueError(f"{path}: not a manifest")
    return doc


def resolve(source: AdapterSource) -> tuple[str, Callable[[], Adapter]]:
    """A name for the report and a factory, from "module:Class", a class or a callable."""
    if isinstance(source, str):
        module, _, attr = source.partition(":")
        if not module or not attr:
            raise ValueError(f"{source!r}: want module:Class")
        return source, getattr(importlib.import_module(module), attr)
    name = getattr(source, "__qualname__", repr(source))
    return f"{getattr(source, '__module__', '?')}:{name}", source


def assert_conforms(source: AdapterSource, **kwargs: Any) -> Report:
    """Run the suite and fail the calling test with the full report when any check fails."""
    report = run_suite(source, **kwargs)
    if not report.ok:
        raise AssertionError("adapter does not conform\n" + report.format())
    return report


def run_suite(
    source: AdapterSource,
    *,
    manifest: Mapping[str, Any] | Path | None = None,
    task: StageType | str | None = None,
    device: Device | str | None = None,
    inputs: Sequence[StageInput] | None = None,
    params: Mapping[str, Any] | None = None,
    seed: int = 42,
    dtype: str = "fp16",
    weights_dir: Path | None = None,
    offload: bool = False,
    work_dir: Path | None = None,
    skip: Iterable[str] = (),
    vram_tolerance: float = 0.25,
) -> Report:
    """Exercise one adapter; never raises for the adapter's mistakes, it reports them."""
    report = Report(adapter=str(source))
    owned = work_dir is None
    work = Path(tempfile.mkdtemp(prefix="forja-sdk-")) if work_dir is None else Path(work_dir)
    try:
        _Suite(report, work, set(skip), vram_tolerance).run(
            source, manifest, task, device, inputs, params, seed, dtype, weights_dir, offload
        )
    finally:
        if owned:
            shutil.rmtree(work, ignore_errors=True)
    return report


def _digest(path: Path) -> str:
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def _error(e: BaseException) -> str:
    return f"{type(e).__name__}: {e}"


_METRIC_NAME = re.compile(r"[a-z][a-z0-9]*(_[a-z0-9]+)*")


def _bad_metrics(metrics: Any) -> str:
    """What is wrong with a StageOutput's metrics, or "" when they are snake_case names of finite numbers."""
    if not isinstance(metrics, dict):
        return "must be a dict of snake_case names to finite numbers"
    for key, value in metrics.items():
        if not isinstance(key, str) or not _METRIC_NAME.fullmatch(key):
            return f"{key!r} is not a snake_case name"
        if isinstance(value, bool) or not isinstance(value, int | float) or not math.isfinite(value):
            return f"{key} is {value!r}, not a finite number"
    return ""


class _Peak:
    """Peak device memory through PyTorch's allocator, where there is one to ask."""

    def __init__(self, device: Device) -> None:
        self.cuda: Any = None
        if device in (Device.CUDA, Device.ROCM) and importlib.util.find_spec("torch"):
            import torch

            if torch.cuda.is_available():
                self.cuda = torch.cuda

    def reset(self) -> None:
        if self.cuda is not None:
            self.cuda.reset_peak_memory_stats()

    def read(self) -> int | None:
        return int(self.cuda.max_memory_reserved()) if self.cuda is not None else None


@dataclass
class _Run:
    outputs: list[StageOutput] | None
    error: BaseException | None
    progress: list[tuple[int, int]]
    seconds: float
    cancel_at: tuple[int, int] | None = None


class _Suite:
    def __init__(self, report: Report, work: Path, skip: set[str], tolerance: float) -> None:
        self.report = report
        self.work = work
        self.skip = skip
        self.tolerance = tolerance

    def record(self, name: str, status: Status, detail: str = "") -> None:
        self.report.results.append(Result(name, status, detail))

    def wanted(self, name: str) -> bool:
        """False (and recorded as skipped) for checks the caller asked to skip, by full or base name."""
        if name in self.skip or name.partition("[")[0] in self.skip:
            self.record(name, "skip", "skipped on request")
            return False
        return True

    def run(
        self,
        source: AdapterSource,
        manifest: Mapping[str, Any] | Path | None,
        task: StageType | str | None,
        device: Device | str | None,
        inputs: Sequence[StageInput] | None,
        params: Mapping[str, Any] | None,
        seed: int,
        dtype: str,
        weights_dir: Path | None,
        offload: bool,
    ) -> None:
        try:
            name, factory = resolve(source)
            self.report.adapter = name
            adapter = factory()
        except Exception as e:
            self.record("import", "fail", _error(e))
            return
        if not isinstance(adapter, Adapter):
            self.record("import", "fail", f"{type(adapter).__name__} does not subclass forja_sdk.Adapter")
            return
        caps = self.capabilities(adapter)
        if caps is None:
            return
        doc: Mapping[str, Any] | None = load_manifest(manifest) if isinstance(manifest, Path) else manifest
        if doc is not None:
            self.manifest(adapter, caps, doc)
        defaults = {k: v["default"] for k, v in ((doc or {}).get("params") or {}).items() if "default" in v}
        stage_params = {**defaults, **(params or {})}

        dev = Device(device) if device else (Device.CPU if Device.CPU in caps.devices else caps.devices[0])
        self.report.device = str(dev)
        if dev not in caps.devices:
            self.record("load", "fail", f"the adapter lists {', '.join(caps.devices)}, not {dev}")
            return
        tasks = [StageType(task)] if task else list(caps.tasks)
        if task and tasks[0] not in caps.tasks:
            self.record("run", "fail", f"the adapter does not serve {task}")
            return
        peak = _Peak(dev)
        peak.reset()
        if not self.load(adapter, dev, dtype, weights_dir, offload):
            return

        first: dict[StageType, list[str]] = {}
        planned: dict[StageType, list[StageInput]] = {}
        for t in tasks:
            stage_inputs = (
                list(inputs) if inputs is not None else samples.inputs_for(t, self.work / "inputs" / t)
            )
            if stage_inputs is None:
                self.record(f"run[{t}]", "skip", f"no sample inputs for {t}; pass your own (--input)")
                continue
            planned[t] = stage_inputs
            before = {str(i.path): _digest(i.path) for i in stage_inputs}
            run1 = self.execute(adapter, t, stage_inputs, stage_params, seed, "run1")
            digests = self.check_run(t, run1)
            self.check_progress(t, run1)
            changed = [p for p, d in before.items() if _digest(Path(p)) != d]
            if changed:
                self.record(f"inputs[{t}]", "fail", f"the adapter changed its inputs: {', '.join(changed)}")
            if digests is None:
                continue
            first[t] = digests
            if t == tasks[0] and self.wanted("vram"):
                self.vram(adapter, stage_params, doc, peak.read(), dev)
            if self.wanted(f"determinism[{t}]"):
                run2 = self.execute(adapter, t, stage_inputs, stage_params, seed, "run2")
                self.compare(f"determinism[{t}]", digests, run2, "a second run")
            if self.wanted(f"cancellation[{t}]"):
                self.cancellation(adapter, t, stage_inputs, stage_params, seed, run1)

        if first and self.wanted("reload"):
            t = next(iter(first))
            try:
                adapter.unload()
                adapter.load(dev, dtype)
            except Exception as e:
                self.record("reload", "fail", f"unload then load: {_error(e)}")
                return
            again = self.execute(adapter, t, planned[t], stage_params, seed, "reload")
            self.compare("reload", first[t], again, "a run after unload() and load()")
        try:
            adapter.unload()
            self.record("unload", "pass")
        except Exception as e:
            self.record("unload", "fail", _error(e))

    def capabilities(self, adapter: Adapter) -> Capabilities | None:
        try:
            caps = adapter.capabilities()
        except Exception as e:
            self.record("capabilities", "fail", _error(e))
            return None
        problems = []
        if not isinstance(caps, Capabilities):
            self.record("capabilities", "fail", f"capabilities() returned {type(caps).__name__}")
            return None
        if not caps.tasks or not all(isinstance(t, StageType) for t in caps.tasks):
            problems.append("tasks must be one or more StageType values")
        if not caps.devices or not all(isinstance(d, Device) for d in caps.devices):
            problems.append("devices must be one or more Device values")
        if not isinstance(caps.vram_bytes, int) or caps.vram_bytes < 0:
            problems.append("vram_bytes must be a byte count")
        if not isinstance(adapter.version, str) or not adapter.version:
            problems.append("version must be a non-empty string")
        if problems:
            self.record("capabilities", "fail", "; ".join(problems))
            return None
        self.record(
            "capabilities",
            "pass",
            f"tasks {', '.join(caps.tasks)}; devices {', '.join(caps.devices)}; version {adapter.version}",
        )
        return caps

    def manifest(self, adapter: Adapter, caps: Capabilities, doc: Mapping[str, Any]) -> None:
        problems = []
        try:
            tasks = {StageType(t) for t in doc.get("tasks") or []}
        except ValueError as e:
            tasks = set()
            problems.append(f"tasks: {e}")
        if tasks != set(caps.tasks):
            problems.append(
                f"tasks are [{', '.join(sorted(tasks))}] in the manifest, "
                f"[{', '.join(sorted(caps.tasks))}] in the adapter"
            )
        version = str(doc.get("adapter_version", "1"))
        if version != adapter.version:
            problems.append(
                f"adapter_version is {version!r}, the adapter's version is {adapter.version!r}; "
                "bump both together"
            )
        lic = doc.get("license")
        spdx = lic.get("spdx") if isinstance(lic, Mapping) else None
        if not isinstance(lic, Mapping) or not isinstance(spdx, str) or not spdx:
            problems.append("license.spdx is missing")
        elif not isinstance(lic.get("commercial"), bool):
            problems.append("license.commercial must be true or false")
        elif not doc.get("stub") and not lic.get("url"):
            problems.append("license.url is missing: the Models screen links the terms")
        for backend, spec in (doc.get("requirements") or {}).items():
            status = (spec or {}).get("status", "supported")
            try:
                dev = Device(backend)
            except ValueError:
                problems.append(f"requirements: unknown backend {backend!r}")
                continue
            if status in ("supported", "experimental") and dev not in caps.devices:
                problems.append(
                    f"the manifest says {backend} is {status}; the adapter lists {', '.join(caps.devices)}"
                )
        for name, spec in (doc.get("params") or {}).items():
            problems += [f"params.{name}: {p}" for p in _param_problems(spec)]
        if problems:
            self.record("manifest", "fail", "; ".join(problems))
        else:
            self.record("manifest", "pass", f"{doc.get('id', '?')}: {spdx}")

    def load(
        self, adapter: Adapter, dev: Device, dtype: str, weights_dir: Path | None, offload: bool
    ) -> bool:
        adapter.weights_dir = weights_dir
        adapter.offload = offload
        started = time.perf_counter()
        try:
            adapter.load(dev, dtype)
        except Exception as e:
            self.record("load", "fail", _error(e))
            return False
        self.record("load", "pass", f"{time.perf_counter() - started:.2f}s")
        return True

    def execute(
        self,
        adapter: Adapter,
        task: StageType,
        inputs: list[StageInput],
        params: Mapping[str, Any],
        seed: int,
        label: str,
        cancel_at: tuple[int, int] | None = None,
    ) -> _Run:
        out_dir = self.work / str(task).replace(".", "-") / label
        shutil.rmtree(out_dir, ignore_errors=True)
        out_dir.mkdir(parents=True)
        seen: list[tuple[int, int]] = []
        cancelled = [False]

        def progress(step: int, total: int) -> None:
            seen.append((step, total))
            if cancel_at is not None and (step, total) == cancel_at:
                cancelled[0] = True

        ctx = StageContext(
            stage_id=f"conformance-{label}",
            task=task,
            inputs=list(inputs),
            # A copy per run: an adapter that edits its params must not change the next run.
            params=json.loads(json.dumps(dict(params))),
            seed=seed,
            out_dir=out_dir,
            progress=progress,
            cancelled=lambda: cancelled[0],
        )
        started = time.perf_counter()
        try:
            outputs = adapter.run(ctx)
            return _Run(outputs, None, seen, time.perf_counter() - started, cancel_at)
        except BaseException as e:
            if isinstance(e, KeyboardInterrupt):
                raise
            return _Run(None, e, seen, time.perf_counter() - started, cancel_at)

    def check_run(self, task: StageType, run: _Run) -> list[str] | None:
        name = f"run[{task}]"
        if run.error is not None:
            self.record(name, "fail", _error(run.error))
            return None
        outputs = run.outputs
        if not isinstance(outputs, list) or not outputs:
            self.record(name, "fail", "run() must return a non-empty list of StageOutput")
            return None
        out_dir = (self.work / str(task).replace(".", "-") / "run1").resolve()
        infos: list[Any] = []
        for i, o in enumerate(outputs):
            if not isinstance(o, StageOutput) or not isinstance(o.kind, ArtifactKind):
                self.record(name, "fail", f"output {i} is not a StageOutput with an ArtifactKind")
                return None
            bad = _bad_metrics(o.metrics)
            if bad:
                self.record(name, "fail", f"output {i} metrics {bad}")
                return None
            path = Path(o.path).resolve()
            if not path.is_file():
                self.record(name, "fail", f"output {i} ({o.path}) does not exist")
                return None
            if not path.is_relative_to(out_dir):
                self.record(name, "fail", f"output {i} ({o.path}) is outside ctx.out_dir")
                return None
            try:
                infos.append(artifacts.validate(path, o.kind))
            except artifacts.InvalidArtifact as e:
                self.record(name, "fail", f"output {i} ({path.name}) is not a valid {o.kind}: {e}")
                return None
        problem = _expectation(task, outputs[0], infos[0])
        if problem:
            self.record(name, "fail", problem)
            return None
        self.record(
            name,
            "pass",
            f"{run.seconds:.2f}s; " + "; ".join(_describe(o, i) for o, i in zip(outputs, infos, strict=True)),
        )
        return [_digest(o.path) for o in outputs]

    def check_progress(self, task: StageType, run: _Run) -> None:
        name = f"progress[{task}]"
        if not run.progress:
            self.record(name, "skip", "no progress reported (fine for a single-step stage)")
            return
        last = -1
        for step, total in run.progress:
            if (
                not isinstance(step, int)
                or not isinstance(total, int)
                or total <= 0
                or not 0 <= step <= total
            ):
                self.record(name, "fail", f"progress({step}, {total}): want 0 <= step <= total and total > 0")
                return
            if step < last:
                self.record(name, "fail", f"progress went back from step {last} to {step}")
                return
            last = step
        self.record(
            name, "pass", f"{len(run.progress)} reports, last {run.progress[-1][0]}/{run.progress[-1][1]}"
        )

    def compare(self, name: str, want: list[str], run: _Run, what: str) -> None:
        if run.error is not None or run.outputs is None:
            self.record(name, "fail", f"{what} failed: {_error(run.error) if run.error else 'no outputs'}")
            return
        got = [_digest(o.path) for o in run.outputs]
        if len(got) != len(want):
            self.record(name, "fail", f"{what} gave {len(got)} outputs, the first run {len(want)}")
            return
        differ = [i for i, (a, b) in enumerate(zip(want, got, strict=True)) if a != b]
        if differ:
            self.record(
                name, "fail", f"{what} wrote different bytes for output {', '.join(map(str, differ))}"
            )
            return
        self.record(
            name, "pass", "byte-identical" if len(got) == 1 else f"all {len(got)} outputs byte-identical"
        )

    def cancellation(
        self,
        adapter: Adapter,
        task: StageType,
        inputs: list[StageInput],
        params: Mapping[str, Any],
        seed: int,
        first: _Run,
    ) -> None:
        name = f"cancellation[{task}]"
        at = next(((s, t) for s, t in first.progress if s < t), None)
        if at is None:
            self.record(name, "skip", "no work left after any progress report, so nothing to cancel between")
            return
        run = self.execute(adapter, task, inputs, params, seed, "cancel", cancel_at=at)
        if isinstance(run.error, Cancelled):
            self.record(name, "pass", f"raised Cancelled after step {at[0]}/{at[1]}")
        elif run.error is not None:
            self.record(name, "fail", f"raised {_error(run.error)} instead of Cancelled")
        else:
            self.record(
                name,
                "fail",
                f"kept running after cancel was requested at step {at[0]}/{at[1]}; "
                "call ctx.check_cancel() between steps",
            )

    def vram(
        self,
        adapter: Adapter,
        params: Mapping[str, Any],
        doc: Mapping[str, Any] | None,
        measured: int | None,
        dev: Device,
    ) -> None:
        try:
            estimate = adapter.estimate_vram(dict(params))
        except Exception as e:
            self.record("vram", "fail", f"estimate_vram: {_error(e)}")
            return
        if not isinstance(estimate, int) or isinstance(estimate, bool) or estimate < 0:
            self.record("vram", "fail", f"estimate_vram returned {estimate!r}; want bytes as an int")
            return
        budget, source = estimate, "estimate_vram"
        if doc is not None and float(doc.get("vram_gb") or 0) > 0:
            # The scheduler plans with the manifest's figure, so that is the one to keep honest.
            budget, source = int(float(doc["vram_gb"]) * GB), "the manifest's vram_gb"
        if measured is None:
            self.record("vram", "skip", f"{source} {budget / GB:.2f} GB; peak not measurable on {dev}")
            return
        limit = budget * (1 + self.tolerance)
        if measured > limit:
            self.record(
                "vram",
                "fail",
                f"measured peak {measured / GB:.2f} GB is above {source} {budget / GB:.2f} GB "
                f"(+{self.tolerance:.0%} allowed); raise it",
            )
            return
        self.record(
            "vram", "pass", f"measured peak {measured / GB:.2f} GB within {source} {budget / GB:.2f} GB"
        )


def _param_problems(spec: Any) -> list[str]:
    if not isinstance(spec, Mapping):
        return ["want a mapping with type and default"]
    kind = spec.get("type")
    if kind not in _PARAM_TYPES:
        return [f"type {kind!r} is not one of {', '.join(_PARAM_TYPES)}"]
    if "default" not in spec:
        return ["has no default"]
    default = spec["default"]
    ok_type = isinstance(default, _PARAM_TYPES[kind]) and (kind == "boolean" or not isinstance(default, bool))
    if not ok_type:
        return [f"default {default!r} is not a {kind}"]
    out = []
    if "enum" in spec and default not in spec["enum"]:
        out.append(f"default {default!r} is not one of {spec['enum']}")
    if "minimum" in spec and default < spec["minimum"]:
        out.append(f"default {default!r} is below the minimum {spec['minimum']}")
    if "maximum" in spec and default > spec["maximum"]:
        out.append(f"default {default!r} is above the maximum {spec['maximum']}")
    return out


def _expectation(task: StageType, primary: StageOutput, info: Any) -> str:
    expect = _EXPECT.get(task)
    if expect is None:
        return ""
    kind, need = expect
    if primary.kind != kind:
        return f"the first output is {primary.kind}; the stage after {task} reads {kind}"
    if need == "alpha" and not info.has_alpha:
        return "the cut-out has no alpha channel; framing needs one"
    if need in ("mesh", "uv", "textured") and info.triangles == 0:
        return "the mesh has no triangles"
    if need in ("uv", "textured") and not info.has_uv:
        return "the mesh has no UVs (TEXCOORD_0)"
    if need == "textured" and not info.textured:
        return "the mesh has no base color texture"
    return ""


def _describe(output: StageOutput, info: Any) -> str:
    if isinstance(info, artifacts.MeshInfo):
        extra = ", textured" if info.textured else ", UVs" if info.has_uv else ""
        return f"{output.kind} {info.triangles} triangles{extra}"
    if isinstance(info, artifacts.ImageInfo):
        return f"{output.kind} {info.width}x{info.height}{' RGBA' if info.has_alpha else ''}"
    return str(output.kind)
