"""Every model manifest carries license metadata the app can show and filter on (spec, section 16).

A model without it would install without the license acceptance its terms may need, and the
commercial-use filter would let it through.
"""

import re
from importlib import resources
from pathlib import Path
from typing import Any

import pytest
import yaml

from forja_engine import families
from forja_engine.registry import IN_ENGINE_FAMILIES, ManifestError, parse

BUILTIN = resources.files("forja_engine") / "manifests"
MANIFESTS = sorted(Path(str(p)) for p in BUILTIN.iterdir() if p.name.endswith((".yaml", ".yml")))
SPDX = re.compile(r"(LicenseRef-[\w.\-]+|[A-Za-z0-9][\w.\-]*\+?)( (AND|OR|WITH) [\w.\-]+\+?)*")


def doc(path: Path) -> dict[str, Any]:
    return yaml.safe_load(path.read_text(encoding="utf-8"))


def test_there_are_manifests():
    assert MANIFESTS


@pytest.mark.parametrize("path", MANIFESTS, ids=lambda p: p.name)
def test_manifest_declares_its_license(path):
    m = doc(path)
    lic = m.get("license")
    assert isinstance(lic, dict), f"{path.name}: no license block"
    assert isinstance(lic.get("spdx"), str) and SPDX.fullmatch(lic["spdx"]), (
        f"{path.name}: license.spdx must be an SPDX id or expression, or LicenseRef-<name>"
    )
    assert isinstance(lic.get("commercial"), bool), f"{path.name}: license.commercial must be true or false"
    if "-NC" in lic["spdx"].upper():
        assert lic["commercial"] is False, f"{path.name}: {lic['spdx']} is non-commercial"
    restrictions = lic.get("restrictions", [])
    assert isinstance(restrictions, list) and all(isinstance(r, str) and r.strip() for r in restrictions)
    if m["family"] not in IN_ENGINE_FAMILIES:
        # Third-party terms: the app links to them where the user accepts the license.
        assert str(lic.get("url", "")).startswith("https://"), f"{path.name}: license.url is required"


@pytest.mark.parametrize(
    "name", sorted(p.parent.name for p in families.families_dir().glob("*/family.yaml")), ids=str
)
def test_family_upstream_declares_its_license(name):
    up = families.load(name).upstream
    if up is not None:
        assert up.license.strip(), f"{name}: upstream.license is required"


def test_registry_rejects_a_manifest_without_license():
    m = doc(MANIFESTS[0])
    del m["license"]
    with pytest.raises(ManifestError):
        parse(m, "no-license.yaml")
