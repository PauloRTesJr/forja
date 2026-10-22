"""Model manifests: what a model is, where its weights live, what it needs, what its license allows.

Models are data. The registry resolves a stage (`use` + optional `model`) to the adapter that
runs it and to the interpreter of the family's own virtual environment.
"""

from __future__ import annotations

import importlib
import sys
from dataclasses import dataclass, field
from importlib import resources
from pathlib import Path
from typing import Any

import yaml

from forja_engine.adapters.builtin import BUILTINS
from forja_engine.sdk import Adapter, StageType

BUILTIN_FAMILY = "builtin"


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
    # Interpreter of the family's virtual environment; None = the supervisor's own.
    python: str | None = None

    @property
    def interpreter(self) -> str:
        return self.python or sys.executable

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
            python=doc.get("python"),
        )
    except (KeyError, ValueError, TypeError) as e:
        raise ManifestError(f"{source}: invalid manifest: {e}") from e


class Registry:
    def __init__(self, manifests: list[Manifest]) -> None:
        self.models = {m.id: m for m in manifests}

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
