"""M7 gate: a model is added from a clean checkout with only a manifest and an adapter.

templates/adapter is a model package: family.yaml, requirements, a manifest and an adapter,
outside the engine and named nowhere in it or in the core. Listing its folder in
`engine.manifest_dirs` (the engine's --manifests) is all it takes.

The last test drives the real core with that config. It builds the package's environment
with uv, so it needs the network and a built core; run it with:

    make core && cd engine && FORJA_GATE_E2E=1 uv run pytest -q tests/test_gate.py
"""

import json
import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from forja_sdk.artifacts import read_glb_info

from forja_engine import install
from forja_engine.api.app import EngineConfig, create_app
from forja_engine.families import ENGINE_ROOT, interpreter
from forja_engine.registry import ManifestError, Registry
from forja_engine.store import hash_file

REPO = ENGINE_ROOT.parent
TEMPLATE = REPO / "templates" / "adapter"
FIXTURE = Path(__file__).parent / "fixtures" / "chest.png"
MODEL = "example-extrude"


def fake_install(monkeypatch, tmp_path) -> tuple[Path, list[list[str]]]:
    """Install the package with every command recorded instead of run; returns envs and commands."""
    commands: list[list[str]] = []
    monkeypatch.setattr(install, "_run", lambda cmd, log, **kw: commands.append([str(c) for c in cmd]))
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")
    envs = tmp_path / "envs"
    inst = install.Installer(Registry.load([TEMPLATE]), envs, tmp_path / "models", log=lambda s: None)
    env = inst.install(MODEL, lane="cpu")
    python = interpreter(env)  # what uv venv would have made
    python.parent.mkdir(parents=True, exist_ok=True)
    python.write_bytes(b"")
    return envs, commands


def test_the_package_is_found_through_manifest_dirs(tmp_path):
    m = Registry.load([TEMPLATE]).models[MODEL]
    assert (m.family, m.family_dir, m.adapter) == (
        "example",
        TEMPLATE.resolve(),
        "forja_example:ExampleExtrude",
    )
    assert m.to_json()["upstream"] == "" and m.to_json()["adapter_version"] == "1"
    assert MODEL not in Registry.load().models  # nothing in the engine knows it
    assert not Registry.load([TEMPLATE]).bind(tmp_path / "envs", tmp_path / "models").models[MODEL].installed


def test_install_builds_the_family_environment_from_the_package(tmp_path, monkeypatch):
    envs, commands = fake_install(monkeypatch, tmp_path)
    pip = next(c for c in commands if c[1:3] == ["pip", "install"])
    assert pip[pip.index("-r") + 1] == str(TEMPLATE.resolve() / "requirements" / "cpu.txt")
    assert pip[-2:] == ["-e", str(TEMPLATE.resolve())]  # the adapter package, next to the engine
    assert commands[0][:2] == ["uv", "venv"] and str(envs / "example") in commands[0]
    assert any(c[-1] == "import forja_example" for c in commands)  # the smoke test imports it
    assert Registry.load([TEMPLATE]).bind(envs, tmp_path / "models").models[MODEL].installed


def package(root: Path, family: str, manifest_family: str) -> Path:
    (root / "manifests").mkdir(parents=True)
    (root / "family.yaml").write_text(f"name: {family}\nlanes: [cpu]\n")
    (root / "manifests" / "m.yaml").write_text(
        f"id: m\nfamily: {manifest_family}\nadapter: x:Y\ntasks: [shape]\n"
        "license: { spdx: MIT, commercial: true }\n"
    )
    return root


@pytest.mark.parametrize(
    ("family", "manifest_family", "says"),
    [
        ("triposg", "triposg", "one of the engine's"),
        ("stub", "stub", "one of the engine's"),
        ("mine", "other", "its package is"),
    ],
)
def test_a_package_keeps_to_its_own_family(tmp_path, family, manifest_family, says):
    with pytest.raises(ManifestError, match=says):
        Registry.load([package(tmp_path / "pkg", family, manifest_family)])


def test_the_engine_runs_a_job_with_the_packaged_model(tmp_path, monkeypatch):
    envs, _ = fake_install(monkeypatch, tmp_path)
    monkeypatch.syspath_prepend(str(TEMPLATE / "src"))  # what the editable install provides
    cfg = EngineConfig(
        store_root=tmp_path / "store",
        manifest_dirs=[TEMPLATE],
        in_process_models=True,
        envs_dir=envs,
        models_dir=tmp_path / "models",
    )
    with TestClient(create_app(cfg)) as client:
        listed = {m["id"]: m for m in client.get("/v1/models").json()}
        assert listed[MODEL]["status"] == "ready" and listed[MODEL]["runs_on"] == "cpu"
        image = hash_file(FIXTURE)
        assert client.put(f"/v1/artifacts/{image}", content=FIXTURE.read_bytes()).status_code == 201
        stages = [
            {"id": "bg", "use": "background-removal", "model": "stub-background", "inputs": [image]},
            {"id": "frame", "use": "framing", "inputs": ["stage:bg"]},
            {
                "id": "shape",
                "use": "shape",
                "model": MODEL,
                "params": {"resolution": 48},
                "inputs": ["stage:frame"],
            },
            {"id": "cleanup", "use": "mesh.cleanup", "inputs": ["stage:shape"]},
            {"id": "export", "use": "export", "inputs": ["stage:cleanup"]},
        ]
        run = client.post("/v1/runs", json={"stages": stages}).json()
        with client.stream("GET", f"/v1/runs/{run['id']}/events") as resp:
            for _ in resp.iter_lines():
                pass
        final = client.get(f"/v1/runs/{run['id']}").json()
        assert final["status"] == "succeeded", final
        glb = client.get(f"/v1/artifacts/{final['stages']['export']['outputs'][0]['hash']}").content
        assert read_glb_info(glb).triangles > 500


# --- the real core ------------------------------------------------------------------------

e2e = pytest.mark.skipif(
    os.environ.get("FORJA_GATE_E2E") != "1", reason="set FORJA_GATE_E2E=1 (network, core)"
)


@e2e
def test_the_core_runs_a_job_with_the_packaged_model(tmp_path):
    forja = REPO / "core" / "bin" / ("forja.exe" if sys.platform == "win32" else "forja")
    if not forja.is_file():
        pytest.fail(f"{forja} is missing: run make core")
    config, data = tmp_path / "config", tmp_path / "data"
    config.mkdir()
    (config / "config.toml").write_text(
        "[models]\nstubs = true\n\n[models.stages]\n"
        f'shape = "{MODEL}"\n\n[engine]\n'
        f'command = ["uv", "run", "--quiet", "--project", "{ENGINE_ROOT.as_posix()}", "forja-engine"]\n'
        f'manifest_dirs = ["{TEMPLATE.as_posix()}"]\n',
        encoding="utf-8",
    )
    socket = (
        rf"\\.\pipe\forja-gate-{os.getpid()}"
        if sys.platform == "win32"
        else str(Path(tempfile.gettempdir()) / f"forja-gate-{os.getpid()}.sock")
    )
    env = {**os.environ, "FORJA_SOCKET": socket, "FORJA_DATA_DIR": str(data), "FORJA_CONFIG_DIR": str(config)}

    def cli(*args: str, timeout: float = 120) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [str(forja), *args], env=env, capture_output=True, text=True, encoding="utf-8", timeout=timeout
        )

    log = (tmp_path / "daemon.log").open("w", encoding="utf-8")
    daemon = subprocess.Popen([str(forja), "daemon"], env=env, stdout=log, stderr=subprocess.STDOUT)
    try:
        deadline = time.monotonic() + 60
        while cli("daemon", "status").returncode != 0:
            assert daemon.poll() is None and time.monotonic() < deadline, (
                tmp_path / "daemon.log"
            ).read_text()
            time.sleep(0.5)
        installed = cli("models", "install", MODEL, timeout=1800)
        assert installed.returncode == 0, installed.stdout + installed.stderr
        # An install reports success a moment before the core refreshes its model list, and a
        # job submitted in between is refused; wait for the list, as a script should.
        deadline = time.monotonic() + 30
        while not any(
            m["id"] == MODEL and m["status"] in ("ready", "loaded")
            for m in json.loads(cli("--json", "models", "ls").stdout)
        ):
            assert time.monotonic() < deadline, "the core never listed the model as ready"
            time.sleep(0.5)
        made = cli("--json", "generate", str(FIXTURE), timeout=600)
        assert made.returncode == 0, made.stdout + made.stderr
        (job,) = json.loads(made.stdout)
        shape = next(s for s in job["stages"] if s["use"] == "shape")
        assert job["status"] == "succeeded" and job["asset_id"] and shape["model"] == MODEL, job
        asset = json.loads(cli("--json", "assets", "show", job["asset_id"]).stdout)
        print(json.dumps({"job": job["id"], "preset": job["preset"], "asset": asset}, indent=2))
    finally:
        cli("daemon", "stop")
        try:
            daemon.wait(timeout=30)
        except subprocess.TimeoutExpired:
            daemon.kill()
        log.close()
