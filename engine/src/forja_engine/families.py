"""Model families: one isolated uv virtual environment per family.

A family folder (`engine/families/<name>/`) holds:

- `family.yaml`: Python version, supported lanes, pinned upstream source, compiled extras.
- `requirements/<lane>.txt`: the family's dependencies for one torch lane.
- `src/forja_<name>/`: the adapter package (Apache-2.0, ours).

Upstream model code is downloaded at install time from a pinned commit and verified by
sha256 (docs/decisions.md, decision 9); it never lives in this repository.
"""

from __future__ import annotations

import os
import platform
import shutil
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml

import forja_engine

# engine/src/forja_engine/__init__.py -> engine/
ENGINE_ROOT = Path(forja_engine.__file__).resolve().parents[2]
LANES = ("cuda", "rocm", "mps", "cpu")


def families_dir() -> Path:
    return Path(os.environ.get("FORJA_FAMILIES_DIR", ENGINE_ROOT / "families"))


@dataclass(frozen=True)
class Upstream:
    url: str
    sha256: str
    license: str
    # Folders inside the extracted source added to the environment's import path.
    pythonpath: tuple[str, ...] = (".",)


@dataclass(frozen=True)
class Family:
    name: str
    root: Path
    python: str
    lanes: tuple[str, ...]
    upstream: Upstream | None = None
    # Installed with --no-build-isolation after the requirements, in order, because they
    # compile against the torch already in the environment. "{upstream}" expands to the
    # extracted source folder.
    build: tuple[str, ...] = ()
    env: dict[str, str] = field(default_factory=dict)
    # uv --torch-backend per lane (e.g. cuda: cu124), so torch comes from PyTorch's index
    # on every OS while everything else resolves from PyPI.
    torch_backend: dict[str, str] = field(default_factory=dict)
    # Imports that prove the upstream code and its compiled extensions load.
    smoke: str = ""

    def requirements(self, lane: str) -> Path:
        return self.root / "requirements" / f"{lane}.txt"

    def builds(self, environment: dict[str, str] | None = None) -> tuple[str, ...]:
        """The build entries for this machine: an entry may end in an environment marker
        ("flash-attn==2.7.4 ; sys_platform == 'linux'") when it exists for some OSes only.
        `environment` overrides marker variables, as for another machine."""
        from packaging.markers import Marker

        out = []
        for spec in self.build:
            _, sep, marker = spec.partition(";")
            if not sep or Marker(marker.strip()).evaluate(environment):
                out.append(spec)
        return tuple(out)


def load(name: str) -> Family:
    root = families_dir() / name
    spec_path = root / "family.yaml"
    if not spec_path.is_file():
        raise FileNotFoundError(f"no model family {name!r} at {root}")
    doc: dict[str, Any] = yaml.safe_load(spec_path.read_text())
    up = doc.get("upstream")
    lanes = tuple(doc["lanes"])
    for lane in lanes:
        if lane not in LANES:
            raise ValueError(f"{spec_path}: unknown lane {lane!r}")
    return Family(
        name=doc["name"],
        root=root,
        python=str(doc.get("python", "3.11")),
        lanes=lanes,
        upstream=Upstream(up["url"], up["sha256"], up["license"], tuple(up.get("pythonpath", ["."])))
        if up
        else None,
        build=tuple(doc.get("build", [])),
        env={k: str(v) for k, v in (doc.get("env") or {}).items()},
        torch_backend={k: str(v) for k, v in (doc.get("torch_backend") or {}).items()},
        smoke=str(doc.get("smoke", "")),
    )


def interpreter(env_dir: Path) -> Path:
    if sys.platform == "win32":
        return env_dir / "Scripts" / "python.exe"
    return env_dir / "bin" / "python"


def detect_lane() -> str:
    """The torch lane this machine should use, from what is installed rather than assumed."""
    if sys.platform == "darwin":
        return "mps" if platform.machine() == "arm64" else "cpu"
    if shutil.which("nvidia-smi"):
        return "cuda"
    if shutil.which("rocminfo") and sys.platform.startswith("linux"):
        return "rocm"
    return "cpu"
