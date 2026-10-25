"""Catalog rules: every manifest carries license metadata and pins what it downloads (spec 16)."""

import copy
import json
from pathlib import Path

import jsonschema
import pytest
import yaml
from forja_sdk import VIEWS, StageType

from forja_engine import catalog, families
from forja_engine.registry import ManifestError, parse

TEMPLATE = catalog.REPO / "templates" / "adapter"
MANIFESTS = sorted(catalog.MANIFESTS.glob("*.y*ml")) + sorted((TEMPLATE / "manifests").glob("*.y*ml"))
FAMILIES = [*sorted(families.families_dir().glob("*/family.yaml")), TEMPLATE / "family.yaml"]


def schema(name: str) -> jsonschema.Draft202012Validator:
    doc = json.loads((catalog.SCHEMAS / f"{name}.schema.json").read_text(encoding="utf-8"))
    jsonschema.Draft202012Validator.check_schema(doc)
    return jsonschema.Draft202012Validator(doc)


def problems(validator: jsonschema.Draft202012Validator, doc: dict) -> list[str]:
    return [
        f"{'/'.join(map(str, e.absolute_path)) or '(top)'}: {e.message}" for e in validator.iter_errors(doc)
    ]


@pytest.mark.parametrize("path", MANIFESTS, ids=lambda p: p.name)
def test_manifests_match_the_schema(path: Path):
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    assert problems(schema("manifest"), doc) == []
    parse(doc, path.name)  # and the engine reads it


@pytest.mark.parametrize("path", FAMILIES, ids=lambda p: p.parent.name)
def test_families_match_the_schema(path: Path):
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    assert problems(schema("family"), doc) == []
    assert doc["name"] == path.parent.name or path.parent == TEMPLATE
    for lane in doc["lanes"]:
        assert (path.parent / "requirements" / f"{lane}.txt").is_file(), lane


def test_a_manifest_without_license_metadata_is_rejected():
    good = yaml.safe_load((catalog.MANIFESTS / "triposg.yaml").read_text(encoding="utf-8"))
    validator = schema("manifest")
    for drop in (("license",), ("license", "spdx"), ("license", "commercial"), ("license", "url")):
        doc = copy.deepcopy(good)
        parent = doc
        for key in drop[:-1]:
            parent = parent[key]
        del parent[drop[-1]]
        assert problems(validator, doc), f"the schema accepts a manifest without {'.'.join(drop)}"
        if drop[-1] != "url":  # the engine itself needs the SPDX id and the commercial flag
            with pytest.raises(ManifestError):
                parse(doc, "triposg.yaml")
    stub = yaml.safe_load((catalog.MANIFESTS / "stub-shape.yaml").read_text(encoding="utf-8"))
    assert "url" not in stub["license"] and problems(validator, stub) == []  # stand-ins need no terms


def test_the_schema_pins_what_install_downloads():
    doc = yaml.safe_load((catalog.MANIFESTS / "triposg.yaml").read_text(encoding="utf-8"))
    doc["weights"]["revision"] = "main"
    assert any("revision" in p for p in problems(schema("manifest"), doc))
    fam = yaml.safe_load((families.families_dir() / "triposg" / "family.yaml").read_text(encoding="utf-8"))
    del fam["upstream"]["sha256"]
    assert any("sha256" in p for p in problems(schema("family"), fam))


def test_the_schema_knows_every_stage_type_and_view():
    """Adding a stage type or a view means adding it to catalog/schema/manifest.schema.json too."""
    props = json.loads((catalog.SCHEMAS / "manifest.schema.json").read_text(encoding="utf-8"))["properties"]
    assert props["tasks"]["items"]["enum"] == [str(t) for t in StageType]
    assert tuple(props["views"]["items"]["enum"]) == VIEWS


def test_the_index_lists_every_bundled_manifest():
    want = catalog.render(catalog.build())
    assert catalog.INDEX.read_text(encoding="utf-8") == want, "run: uv run python -m forja_engine.catalog"
    index = json.loads(want)
    assert {m["id"] for m in index["models"]} == {
        yaml.safe_load(p.read_text(encoding="utf-8"))["id"] for p in catalog.MANIFESTS.glob("*.y*ml")
    }
