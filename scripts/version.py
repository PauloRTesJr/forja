"""Set Forja's one version (app, core and engine) everywhere it is written: `make version V=1.2.3`.

VERSION is the source of truth; the Makefile stamps it into the core at build time. The engine
API carries its own version (engine/src/forja_engine/api/version.py) and is not touched.
`--check` fails when a file disagrees with VERSION.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SEMVER = re.compile(r"\d+\.\d+\.\d+(-(alpha|beta|rc)\.\d+)?")

# (file, pattern whose group 2 is the version); the first match in each file is replaced.
TARGETS = [
    ("apps/desktop/package.json", r'^(  "version": ")([^"]+)(")'),
    ("engine/pyproject.toml", r'^(version = ")([^"]+)(")'),
    ("engine/src/forja_engine/__init__.py", r'^(__version__ = ")([^"]+)(")'),
    # The adapter SDK is released with the engine, which pins it exactly.
    ("engine/sdk/pyproject.toml", r'^(version = ")([^"]+)(")'),
    ("engine/sdk/src/forja_sdk/__init__.py", r'^(__version__ = ")([^"]+)(")'),
    ("engine/pyproject.toml", r'^(    "forja-engine-sdk==)([^"]+)(")'),
]


def files() -> list[tuple[Path, str]]:
    out = [(ROOT / f, p) for f, p in TARGETS]
    # Model family adapters ship inside the engine, so they carry its version.
    out += [(f, TARGETS[1][1]) for f in sorted((ROOT / "engine" / "families").glob("*/pyproject.toml"))]
    return out


def check() -> int:
    """Exit 1 unless every file carries VERSION (CI and the release workflow run this)."""
    version = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
    wrong = 0
    for path, pattern in files():
        m = re.search(pattern, path.read_text(encoding="utf-8"), flags=re.MULTILINE)
        found = m.group(2) if m else "nothing"
        if found != version:
            print(f"{path.relative_to(ROOT).as_posix()}: {found}, VERSION is {version}", file=sys.stderr)
            wrong += 1
    if wrong:
        print(f"run `make version V={version}` to write it everywhere", file=sys.stderr)
        return 1
    print(f"every file carries {version}")
    return 0


def main(argv: list[str]) -> int:
    if argv == ["--check"]:
        return check()
    if len(argv) != 1 or not SEMVER.fullmatch(argv[0]):
        print("usage: make version V=X.Y.Z (or X.Y.Z-rc.N, -beta.N, -alpha.N)", file=sys.stderr)
        return 2
    version = argv[0]
    (ROOT / "VERSION").write_text(version + "\n", newline="\n")
    for path, pattern in files():
        text = path.read_text(encoding="utf-8")
        new, n = re.subn(pattern, rf"\g<1>{version}\g<3>", text, count=1, flags=re.MULTILINE)
        if n != 1:
            print(f"{path.relative_to(ROOT)}: no version found", file=sys.stderr)
            return 1
        path.write_text(new, encoding="utf-8", newline="\n")
        print(f"{path.relative_to(ROOT).as_posix()}: {version}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
