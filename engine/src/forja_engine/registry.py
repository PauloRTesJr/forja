"""Model manifests: what a model is, where its weights live, what it needs, what its license allows.

Models are data. The registry resolves a stage (`use` + optional `model`) to the adapter that
runs it and to the interpreter of the family's own virtual environment.
"""

from __future__ import annotations

import importlib
import sys
from dataclasses import dataclass, field, replace
from functools import cache
from importlib import resources
from pathlib import Path
from typing import Any

import yaml

from forja_engine import families
from forja_engine.adapters.builtin import BUILTINS
from forja_engine.sdk import VIEWS, Adapter, StageType

# Families whose adapters need nothing beyond the engine's own environment.
IN_ENGINE_FAMILIES = ("builtin", "stub")


@dataclass(frozen=True)
class License:
    spdx: str
    commercial: bool
    restrictions: tuple[str, ...] = ()
    url: str = ""


@dataclass(frozen=True)
class Manifest:
    id: str
    name: str
    family: str
    adapter: str  # "module:Class"
    tasks: tuple[StageType, ...]
    license: License
    # Mirrors the adapter class's `version`, so the core can key the cache without importing
    # code that lives in another environment. tests/test_families.py keeps the two equal.
    adapter_version: str = "1"
    requirements: dict[str, Any] = field(default_factory=dict)
    params: dict[str, Any] = field(default_factory=dict)
    weights: dict[str, Any] = field(default_factory=dict)
    # Peak VRAM at default params, from the publisher; replaced by measured peaks per device.
    vram_gb: float = 0.0
    # One line for the Models screen, and the disk an install takes (environment plus weights).
    description: str = ""
    disk_gb: float = 0.0
    # Orders models for the same stage when a preset leaves the model open (higher first).
    rank: int = 0
    # Deterministic stand-ins for tests and machines without a GPU; picked only when asked for.
    stub: bool = False
    # The views of the subject the model reads besides the front (see sdk.VIEWS); empty when it
    # takes one image. The core sends a job's side views only to stages whose model lists them.
    views: tuple[str, ...] = ()
    # Explicit interpreter; otherwise the family's environment, or the supervisor's own for
    # families that need nothing beyond the engine (built-in, stub).
    python: str | None = None
    # The family folder when the model came in a model package instead of engine/families.
    family_dir: Path | None = None
    env_dir: Path | None = None
    weights_dir: Path | None = None
    installed: bool = True

    @property
    def in_engine_env(self) -> bool:
        return self.family in IN_ENGINE_FAMILIES

    @property
    def interpreter(self) -> str:
        if self.python:
            return self.python
        if self.env_dir is not None and not self.in_engine_env:
            return str(families.interpreter(self.env_dir))
        return sys.executable

    @property
    def vram_bytes(self) -> int:
        return int(self.vram_gb * (1 << 30))

    def supports(self, backend: str) -> bool:
        """Whether the model runs on a device of this backend ("cuda", "mps", "rocm", "cpu")."""
        spec = self.requirements.get(backend) or {}
        status = spec.get("status", "supported" if backend in self.requirements else None)
        return status in ("supported", "experimental")

    def runs_on_lane(self, lane: str) -> bool:
        """Whether a machine of this torch lane can run the model: on its GPU, or on its CPU,
        which every machine has."""
        return self.supports(lane) or self.supports("cpu")

    def to_json(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "name": self.name,
            "family": self.family,
            "tasks": [str(t) for t in self.tasks],
            "license": {
                "spdx": self.license.spdx,
                "commercial": self.license.commercial,
                "restrictions": list(self.license.restrictions),
                "url": self.license.url,
            },
            "requirements": self.requirements,
            "params": self.params,
            "vram_gb": self.vram_gb,
            "description": self.description,
            "disk_gb": self.disk_gb,
            "installed": self.installed,
            "weights": {k: self.weights[k] for k in ("repo", "revision") if k in self.weights},
            "adapter_version": self.adapter_version,
            "upstream": upstream_pin(self.family, self.family_dir),
            "rank": self.rank,
            "stub": self.stub,
            "views": list(self.views),
        }


class ManifestError(ValueError):
    pass


@cache
def upstream_pin(family: str, root: Path | None = None) -> str:
    """The sha256 of the upstream source a family runs, or "" for families with none."""
    if family in IN_ENGINE_FAMILIES:
        return ""
    try:
        up = families.load(family, root).upstream
    except FileNotFoundError:
        return ""
    return up.sha256 if up else ""


def parse(doc: dict[str, Any], source: str) -> Manifest:
    try:
        lic = doc["license"]
        return Manifest(
            id=doc["id"],
            name=doc.get("name", doc["id"]),
            family=doc["family"],
            adapter=doc["adapter"],
            tasks=tuple(StageType(t) for t in doc["tasks"]),
            license=License(
                lic["spdx"],
                bool(lic["commercial"]),
                tuple(lic.get("restrictions", [])),
                str(lic.get("url", "")),
            ),
            description=str(doc.get("description", "")),
            disk_gb=float(doc.get("disk_gb", 0)),
            adapter_version=str(doc.get("adapter_version", "1")),
            requirements=doc.get("requirements", {}),
            params=doc.get("params", {}),
            weights=doc.get("weights", {}),
            vram_gb=float(doc.get("vram_gb", 0)),
            rank=int(doc.get("rank", 0)),
            stub=bool(doc.get("stub", False)),
            views=_views(doc.get("views", [])),
            python=doc.get("python"),
        )
    except (KeyError, ValueError, TypeError) as e:
        raise ManifestError(f"{source}: invalid manifest: {e}") from e


def _package(root: Path) -> list[Manifest]:
    """A model package: a family folder (family.yaml, requirements/, src/) with a manifests/ folder.

    It adds models from outside the engine: install builds the family's environment from the
    package, the same way it does for engine/families.
    """
    doc = yaml.safe_load((root / "family.yaml").read_text(encoding="utf-8")) or {}
    family = str(doc.get("name", ""))
    if family in IN_ENGINE_FAMILIES or (families.families_dir() / family / "family.yaml").is_file():
        raise ManifestError(
            f"{root}: family {family!r} is one of the engine's; give the package its own name"
        )
    out = []
    for path in sorted((root / "manifests").glob("*.y*ml")):
        m = parse(yaml.safe_load(path.read_text(encoding="utf-8")), str(path))
        if m.family != family:
            raise ManifestError(f"{path}: names family {m.family!r}, but its package is {family!r}")
        out.append(replace(m, family_dir=root.resolve()))
    return out


def _views(raw: list[str]) -> tuple[str, ...]:
    views = tuple(str(v) for v in raw)
    if views and ("front" not in views or not set(views) <= set(VIEWS)):
        raise ValueError(f"views must include front and name only {', '.join(VIEWS)}")
    return tuple(v for v in VIEWS if v in views)


class Registry:
    def __init__(self, manifests: list[Manifest]) -> None:
        self.models = {m.id: m for m in manifests}

    def bind(self, envs_dir: Path, models_dir: Path) -> Registry:
        """Attach each model to its family environment and weights folder on this machine."""
        from forja_engine.install import installed_models

        bound = []
        for m in self.models.values():
            env = envs_dir / m.family
            installed = m.in_engine_env or m.python is not None or m.id in installed_models(env)
            bound.append(replace(m, env_dir=env, weights_dir=models_dir / m.id, installed=installed))
        return Registry(bound)

    @classmethod
    def load(cls, extra_dirs: list[Path] | None = None) -> Registry:
        found: list[Manifest] = []
        builtin_dir = resources.files("forja_engine") / "manifests"
        for entry in sorted(builtin_dir.iterdir(), key=lambda e: e.name):
            if entry.name.endswith((".yaml", ".yml")):
                found.append(parse(yaml.safe_load(entry.read_text()), entry.name))
        for d in extra_dirs or []:
            if (d / "family.yaml").is_file():
                found += _package(d)
                continue
            for path in sorted(d.glob("*.y*ml")):
                found.append(parse(yaml.safe_load(path.read_text()), str(path)))
        return cls(found)

    def resolve(self, task: StageType, model: str | None) -> Manifest | None:
        """The manifest for a model stage, or None for a built-in CPU stage."""
        if model is None:
            if task in BUILTINS:
                return None
            raise ManifestError(f"stage type {task} needs a model")
        manifest = self.models.get(model)
        if manifest is None:
            raise ManifestError(f"unknown model {model!r}")
        if task not in manifest.tasks:
            raise ManifestError(f"model {model!r} does not provide {task}")
        return manifest


def instantiate(adapter_ref: str) -> Adapter:
    module_name, _, cls_name = adapter_ref.partition(":")
    cls = getattr(importlib.import_module(module_name), cls_name)
    adapter = cls()
    if not isinstance(adapter, Adapter):
        raise TypeError(f"{adapter_ref} is not a forja Adapter")
    return adapter
