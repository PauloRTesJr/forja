"""Model manifests: what a model is, where its weights live, what it needs, what its license allows.

Models are data. The registry resolves a stage (`use` + optional `model`) to the adapter that
runs it and to the interpreter of the family's own virtual environment.
"""

from __future__ import annotations

import importlib
import sys
from dataclasses import dataclass, field, replace
from importlib import resources
from pathlib import Path
from typing import Any

import yaml

from forja_engine import families
from forja_engine.adapters.builtin import BUILTINS
from forja_engine.sdk import Adapter, StageType

# Families whose adapters need nothing beyond the engine's own environment.
IN_ENGINE_FAMILIES = ("builtin", "stub")


@dataclass(frozen=True)
class License:
    spdx: str
    commercial: bool
    restrictions: tuple[str, ...] = ()


@dataclass(frozen=True)
class Manifest:
    id: str
    name: str
    family: str
    adapter: str  # "module:Class"
    tasks: tuple[StageType, ...]
    license: License
    requirements: dict[str, Any] = field(default_factory=dict)
    params: dict[str, Any] = field(default_factory=dict)
    weights: dict[str, Any] = field(default_factory=dict)
    # Peak VRAM at default params, from the publisher; replaced by measured peaks per device.
    vram_gb: float = 0.0
    # Explicit interpreter; otherwise the family's environment, or the supervisor's own for
    # families that need nothing beyond the engine (built-in, stub).
    python: str | None = None
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
            },
            "requirements": self.requirements,
            "params": self.params,
            "vram_gb": self.vram_gb,
            "installed": self.installed,
            "weights": {k: self.weights[k] for k in ("repo", "revision") if k in self.weights},
        }


class ManifestError(ValueError):
    pass


def parse(doc: dict[str, Any], source: str) -> Manifest:
    try:
        lic = doc["license"]
        return Manifest(
            id=doc["id"],
            name=doc.get("name", doc["id"]),
            family=doc["family"],
            adapter=doc["adapter"],
            tasks=tuple(StageType(t) for t in doc["tasks"]),
            license=License(lic["spdx"], bool(lic["commercial"]), tuple(lic.get("restrictions", []))),
            requirements=doc.get("requirements", {}),
            params=doc.get("params", {}),
            weights=doc.get("weights", {}),
            vram_gb=float(doc.get("vram_gb", 0)),
            python=doc.get("python"),
        )
    except (KeyError, ValueError, TypeError) as e:
        raise ManifestError(f"{source}: invalid manifest: {e}") from e


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
