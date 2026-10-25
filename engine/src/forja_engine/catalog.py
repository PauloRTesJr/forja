"""The catalog index (catalog/index.json): one entry per bundled manifest.

It lets a client list the official models, filter them by license and check a manifest's
bytes without running an engine. Rebuild it after adding or changing a manifest:

    uv run python -m forja_engine.catalog           # rewrite catalog/index.json
    uv run python -m forja_engine.catalog --check   # exit 1 when it is out of date
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path
from typing import Any

import yaml

from forja_engine.families import ENGINE_ROOT
from forja_engine.registry import parse

REPO = ENGINE_ROOT.parent
MANIFESTS = ENGINE_ROOT / "src" / "forja_engine" / "manifests"
INDEX = REPO / "catalog" / "index.json"
SCHEMAS = REPO / "catalog" / "schema"
INDEX_VERSION = 1


def build(manifests: Path = MANIFESTS) -> dict[str, Any]:
    models = []
    for path in sorted(manifests.glob("*.y*ml")):
        raw = path.read_bytes()
        m = parse(yaml.safe_load(raw), path.name)
        models.append(
            {
                "id": m.id,
                "name": m.name,
                "family": m.family,
                "tasks": [str(t) for t in m.tasks],
                "license": {
                    "spdx": m.license.spdx,
                    "commercial": m.license.commercial,
                    "restrictions": list(m.license.restrictions),
                },
                "adapter_version": m.adapter_version,
                "vram_gb": m.vram_gb,
                "disk_gb": m.disk_gb,
                "rank": m.rank,
                "stub": m.stub,
                "manifest": path.relative_to(REPO).as_posix(),
                "sha256": hashlib.sha256(raw).hexdigest(),
            }
        )
    return {"version": INDEX_VERSION, "models": models}


def render(index: dict[str, Any]) -> str:
    return json.dumps(index, indent=2, ensure_ascii=False) + "\n"


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(prog="python -m forja_engine.catalog")
    parser.add_argument("--check", action="store_true", help="exit 1 when catalog/index.json is out of date")
    args = parser.parse_args(argv)
    text = render(build())
    current = INDEX.read_text(encoding="utf-8") if INDEX.is_file() else ""
    if args.check:
        if text != current:
            sys.exit(f"{INDEX} is out of date: run uv run python -m forja_engine.catalog")
        return
    if text != current:
        INDEX.write_text(text, encoding="utf-8", newline="\n")
        print(f"wrote {INDEX}")


if __name__ == "__main__":
    main()
