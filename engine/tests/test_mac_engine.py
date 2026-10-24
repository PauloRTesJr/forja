"""What an engine on an Apple Silicon Mac tells the core, in /v1/info and /v1/models.

fixtures/mac-engine-info.json and fixtures/mac-engine-models.json are the real engine's answers
with the Mac's hardware faked (conftest's fake_mac: an M2 Pro with 32 GB) and two models
installed there: BiRefNet, which a Mac runs on the CPU, and Hunyuan3D-2mv on the Apple GPU. The
core's capability routing tests against them. The engine may add fields and models; what the
fixtures say about devices, budgets and where each model runs must stay true. WRITE_FIXTURES=1
rewrites them from the engine.
"""

import json
import os
from pathlib import Path
from typing import Any

import pytest
from fastapi.testclient import TestClient

from forja_engine import families
from forja_engine.api.app import EngineConfig, create_app

FIXTURES = Path(__file__).parent / "fixtures"
INFO, MODELS = FIXTURES / "mac-engine-info.json", FIXTURES / "mac-engine-models.json"
# Shown in the fixtures, not pinned: versions move with every release, and the rest says what a
# stage or model is rather than where it runs.
UNPINNED = {"version", "api_version", "stages", "params", "description", "license", "weights", "disk_gb"}
UNPINNED |= {"adapter_version", "upstream"}
GIB = 1 << 30


@pytest.fixture
def mac_engine(fake_mac, tmp_path):
    envs = tmp_path / "envs"
    for family, models in {"birefnet": ["birefnet"], "hunyuan3d2": ["hunyuan3d2-mv"]}.items():
        python = families.interpreter(envs / family)
        python.parent.mkdir(parents=True)
        python.touch()
        state = {"lane": "mps", "models": {m: {"revision": None} for m in models}}
        (envs / family / "forja-installed.json").write_text(json.dumps(state))
    cfg = EngineConfig(
        store_root=tmp_path / "store", token="t", envs_dir=envs, models_dir=tmp_path / "models"
    )
    with TestClient(create_app(cfg), headers={"Authorization": "Bearer t"}) as c:
        yield c


def holds(want: Any, got: Any, path: str = "$") -> None:
    """Every value the fixture names is still what the engine says."""
    hint = "; if the engine changed on purpose, run WRITE_FIXTURES=1 uv run pytest tests/test_mac_engine.py"
    if isinstance(want, dict):
        assert isinstance(got, dict), f"{path} is no longer an object{hint}"
        for key, value in want.items():
            if key in UNPINNED:
                continue
            assert key in got, f"{path}.{key} is gone{hint}"
            holds(value, got[key], f"{path}.{key}")
    elif isinstance(want, list):
        assert isinstance(got, list) and len(got) == len(want), f"{path} changed length{hint}"
        for i, (w, g) in enumerate(zip(want, got, strict=True)):
            holds(w, g, f"{path}[{i}]")
    else:
        assert want == got, f"{path}: the fixture says {want!r}, the engine {got!r}{hint}"


def test_the_mac_fixtures_are_what_the_engine_says(mac_engine):
    info = mac_engine.get("/v1/info").json()
    models = mac_engine.get("/v1/models").json()
    if os.environ.get("WRITE_FIXTURES") == "1":
        INFO.write_text(json.dumps(info, indent=2) + "\n", newline="\n")
        MODELS.write_text(json.dumps(models, indent=2) + "\n", newline="\n")
    holds(json.loads(INFO.read_text()), info)
    by_id = {m["id"]: m for m in models}
    for want in json.loads(MODELS.read_text()):
        assert want["id"] in by_id, f"model {want['id']} is gone"
        holds(want, by_id[want["id"]], f"$[{want['id']}]")


def test_a_mac_engine_offers_shape_on_its_gpu_and_no_texture(mac_engine):
    """The truths the core routes by, spelled out."""
    info = mac_engine.get("/v1/info").json()
    assert [d["id"] for d in info["system"]["devices"]] == ["mps", "cpu"]
    assert (
        info["lanes"]["mps"]["budget_bytes"] == 22_906_503_168 - GIB
    )  # Metal's working set, less the reserve
    models = {m["id"]: m for m in mac_engine.get("/v1/models").json()}
    mv = models["hunyuan3d2-mv"]
    assert (mv["status"], mv["device"], mv["runs_on"], mv["fit"]) == ("ready", "mps", "mps", "fits")
    assert models["birefnet"]["device"] == "cpu" and models["birefnet"]["fit"] == "fits"
    for remote_only in ("hunyuan3d2-paint", "triposg"):
        m = models[remote_only]
        assert (m["status"], m["device"], m["runs_on"], m["fit"]) == (
            "not-installed",
            None,
            None,
            "unsupported",
        )
