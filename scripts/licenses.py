"""Fail when a dependency Forja ships carries a strong copyleft license.

    cd engine && uv run python ../scripts/licenses.py python
    cd apps/desktop && pnpm licenses list --prod --json | python ../../scripts/licenses.py npm

`python` walks forja-engine's runtime dependencies in the running environment (dev tools and
model families are left out); `npm` reads pnpm's list of the app's production dependencies.
The core's Go modules are checked by go-licenses (`make licenses`). Model families' upstream
code is not a dependency: it is fetched at install time under its own license, which each
manifest declares (engine/tests/test_manifest_licenses.py). Blender is GPL but is the user's own
program, run as a separate process and never shipped.

Weak copyleft (LGPL, MPL) passes: those libraries stay separate, replaceable files.
"""

from __future__ import annotations

import json
import re
import sys
from importlib import metadata
from typing import Any

# SPDX ids (prefixes) and license names whose terms extend to the program that includes them.
STRONG_IDS = ("GPL-", "AGPL-", "SSPL-", "EUPL-", "OSL-", "Sleepycat", "RPL-")
STRONG_NAMES = (
    "gnu general public license",
    "gnu affero general public license",
    "server side public license",
    "european union public licence",
    "open software license",
)

# Packages whose license metadata misleads the scan, with the reason. Empty is the goal.
EXCEPTIONS: dict[str, str] = {}


def strong_id(token: str) -> bool:
    token = token.strip().removesuffix("+")
    if token.upper().startswith("LGPL"):
        return False
    return any(token.upper().startswith(p.upper()) for p in STRONG_IDS) or token.upper() in ("GPL", "AGPL")


def strong_name(text: str) -> bool:
    t = " ".join(text.lower().split())
    t = t.replace("gnu lesser general public license", "").replace("gnu library general public license", "")
    return any(n in t for n in STRONG_NAMES)


def copyleft(expr: str) -> bool:
    """Whether an SPDX expression, or a license name, leaves no choice but strong copyleft."""
    expr = expr.strip()
    if not expr:
        return False
    if not re.fullmatch(r"[\w.+\-() ]+", expr) or len(expr.split()) > 12:
        return strong_name(expr)  # free text, often the whole license
    tokens = re.findall(r"\(|\)|[^\s()]+", expr)
    pos = 0

    def alternatives() -> bool:  # a OR b: copyleft only if every choice is
        nonlocal pos
        result = conjunction()
        while pos < len(tokens) and tokens[pos].upper() == "OR":
            pos += 1
            result = conjunction() and result
        return result

    def conjunction() -> bool:  # a AND b: copyleft if any part is
        nonlocal pos
        result = atom()
        while pos < len(tokens) and tokens[pos].upper() == "AND":
            pos += 1
            result = atom() or result
        return result

    def atom() -> bool:
        nonlocal pos
        if pos >= len(tokens):
            return False
        tok = tokens[pos]
        pos += 1
        if tok == "(":
            inner = alternatives()
            if pos < len(tokens) and tokens[pos] == ")":
                pos += 1
            return inner
        words = [tok]
        while pos < len(tokens) and tokens[pos].upper() not in ("(", ")", "AND", "OR"):
            words.append(tokens[pos])
            pos += 1
        if len(words) > 2 and words[1].upper() == "WITH":
            words = words[:1]  # an exception (e.g. Classpath) does not change the base license
        if len(words) == 1:
            return strong_id(words[0])
        return strong_name(" ".join(words))

    return alternatives()


def python_licenses(dist: metadata.Distribution) -> str:
    md = dist.metadata

    def field(name: str) -> str:
        return " ".join(md.get_all(name) or [])

    if expr := field("License-Expression"):
        return expr
    tagged = [c for c in md.get_all("Classifier") or [] if c.startswith("License ::")]
    classifiers = [c.split("::")[-1].strip() for c in tagged]
    classifiers = [c for c in classifiers if c not in ("OSI Approved", "")]
    if classifiers:
        # Several license classifiers mean a choice between them.
        return " OR ".join(f"({c})" for c in classifiers)
    return field("License")


def python_closure(root: str) -> list[metadata.Distribution]:
    from packaging.markers import default_environment
    from packaging.requirements import Requirement
    from packaging.utils import canonicalize_name

    env = {k: str(v) for k, v in default_environment().items()} | {"extra": ""}
    seen: dict[str, metadata.Distribution] = {}
    todo = [root]
    while todo:
        name = canonicalize_name(todo.pop())
        if name in seen:
            continue
        try:
            dist = metadata.distribution(name)
        except metadata.PackageNotFoundError:
            print(f"warning: {name} is required but not installed", file=sys.stderr)
            continue
        seen[name] = dist
        for raw in dist.requires or []:
            req = Requirement(raw)
            if req.marker is None or req.marker.evaluate(env):
                todo.append(req.name)
    seen.pop(canonicalize_name(root), None)
    return sorted(seen.values(), key=lambda d: d.metadata["Name"].lower())


def report(rows: list[tuple[str, str, str]]) -> int:
    """rows: (package, version, license). Prints each and returns the exit code."""
    bad = 0
    for name, version, lic in rows:
        shown = " ".join(lic.split())
        shown = shown if len(shown) <= 70 else shown[:67] + "..."
        if lic.strip().lower() in ("", "unknown"):
            verdict = "unknown"
        elif copyleft(lic) and name not in EXCEPTIONS:
            verdict = "COPYLEFT"
            bad += 1
        else:
            verdict = "ok"
        print(f"{verdict:9} {name} {version}: {shown or '-'}")
    if bad:
        print(f"\n{bad} dependencies carry a strong copyleft license; Forja ships them, so it cannot.")
    return 1 if bad else 0


def main(argv: list[str]) -> int:
    if argv == ["python"]:
        dists = python_closure("forja-engine")
        rows = [(d.metadata["Name"], d.version, python_licenses(d)) for d in dists]
        return report(rows)
    if argv == ["npm"]:
        listing: dict[str, list[dict[str, Any]]] = json.load(sys.stdin)
        rows = [
            (str(p["name"]), ",".join(str(v) for v in p.get("versions", [])), lic)
            for lic, pkgs in listing.items()
            for p in pkgs
        ]
        return report(sorted(rows))
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
