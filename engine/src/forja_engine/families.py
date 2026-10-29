"""Model families: one isolated uv virtual environment per family.

A family folder (`engine/families/<name>/`) holds:

- `family.yaml`: Python version, supported lanes, pinned upstream source, compiled extras.
- `requirements/<lane>.txt`: the family's dependencies for one torch lane.
- `src/forja_<name>/`: the adapter package (Apache-2.0, ours).

A family can also live outside the engine, as a model package: the same folder plus a
`manifests/` folder, listed in `engine.manifest_dirs` (see registry.Registry.load).

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
    # Where the source is extracted in the environment: "upstream" for the family's own,
    # "upstream-<name>" for each other one it lists under `extra`.
    folder: str = "upstream"


@dataclass(frozen=True)
class Family:
    name: str
    root: Path
    python: str
    lanes: tuple[str, ...]
    upstream: Upstream | None = None
    # Further pinned sources the upstream code imports but does not ship (Pixal3D's NAF).
    extra_upstreams: tuple[Upstream, ...] = ()
    # Installed with --no-build-isolation after the requirements, in order, because they
    # compile against the torch already in the environment. "{upstream}" expands to the
    # extracted source folder. Per lane: a lane may install only the models that need no
    # compiled extension (Hunyuan3D-2's shape model on an Apple GPU, without the rasterizer).
    build_by_lane: dict[str, tuple[str, ...]] = field(default_factory=dict)
    env: dict[str, str] = field(default_factory=dict)
    # uv --torch-backend per lane (e.g. cuda: cu124), so torch comes from PyTorch's index
    # on every OS while everything else resolves from PyPI.
    torch_backend: dict[str, str] = field(default_factory=dict)
    # Per lane: imports that prove the upstream code and its compiled extensions load.
    smoke_by_lane: dict[str, str] = field(default_factory=dict)

    def requirements(self, lane: str) -> Path:
        return self.root / "requirements" / f"{lane}.txt"

    def builds(self, lane: str, environment: dict[str, str] | None = None) -> tuple[str, ...]:
        """The lane's build entries for this machine: an entry may end in an environment marker
        ("flash-attn==2.7.4 ; sys_platform == 'linux'") when it exists for some OSes only.
        `environment` overrides marker variables, as for another machine."""
        from packaging.markers import Marker

        out = []
        for spec in self.build_by_lane.get(lane, ()):
            _, sep, marker = split_build(spec)[1].partition(";")
            if not sep or Marker(marker.strip()).evaluate(environment):
                out.append(spec)
        return tuple(out)

    def smoke(self, lane: str) -> str:
        return self.smoke_by_lane.get(lane, "")


#: The one option a build entry may start with: install that package without its dependencies,
#: for upstream packages that list their siblings as unpinned git URLs (TRELLIS.2's o_voxel).
NO_DEPS = "--no-deps"


def split_build(spec: str) -> tuple[list[str], str]:
    """A build entry's uv options and its requirement."""
    spec = spec.strip()
    if spec.startswith(NO_DEPS + " "):
        return [NO_DEPS], spec[len(NO_DEPS) :].strip()
    return [], spec


def _upstream(raw: dict[str, Any], folder: str = "upstream") -> Upstream:
    return Upstream(raw["url"], raw["sha256"], raw["license"], tuple(raw.get("pythonpath", ["."])), folder)


def _per_lane(raw: Any, lanes: tuple[str, ...], source: Path, key: str) -> dict[str, Any]:
    """A family.yaml value that is either the same on every lane or a mapping of lane to value."""
    if not isinstance(raw, dict):
        return dict.fromkeys(lanes, raw)
    unknown = set(raw) - set(lanes)
    if unknown:
        raise ValueError(
            f"{source}: {key} names lanes the family does not have: {', '.join(sorted(unknown))}"
        )
    return dict(raw)


def load(name: str, root: Path | None = None) -> Family:
    """The family `name`, from `root` when it is a model package, else from families_dir()."""
    root = root or families_dir() / name
    spec_path = root / "family.yaml"
    if not spec_path.is_file():
        raise FileNotFoundError(f"no model family {name!r} at {root}")
    doc: dict[str, Any] = yaml.safe_load(spec_path.read_text())
    up = doc.get("upstream")
    lanes = tuple(doc["lanes"])
    for lane in lanes:
        if lane not in LANES:
            raise ValueError(f"{spec_path}: unknown lane {lane!r}")
    build_by_lane = {
        lane: tuple(v or ()) for lane, v in _per_lane(doc.get("build", []), lanes, spec_path, "build").items()
    }
    for spec in (s for specs in build_by_lane.values() for s in specs):
        if split_build(spec)[1].startswith("-"):
            raise ValueError(f"{spec_path}: build entry {spec!r}: the only option is {NO_DEPS}")
    return Family(
        name=doc["name"],
        root=root,
        python=str(doc.get("python", "3.11")),
        lanes=lanes,
        upstream=_upstream(up) if up else None,
        extra_upstreams=tuple(_upstream(x, f"upstream-{x['name']}") for x in (up or {}).get("extra", [])),
        build_by_lane=build_by_lane,
        env={k: str(v) for k, v in (doc.get("env") or {}).items()},
        torch_backend={k: str(v) for k, v in (doc.get("torch_backend") or {}).items()},
        smoke_by_lane={
            lane: str(v or "")
            for lane, v in _per_lane(doc.get("smoke", ""), lanes, spec_path, "smoke").items()
        },
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
