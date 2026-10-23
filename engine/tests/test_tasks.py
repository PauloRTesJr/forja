"""The engine's install, remove, check and fix API, with the installer and toolchain faked."""

import json
import threading
from typing import ClassVar

import pytest
from fastapi.testclient import TestClient

from forja_engine import checks
from forja_engine.api import app as engine_app
from forja_engine.api.app import EngineConfig, create_app


class FakeInstaller:
    """Reports like the real installer; `gate` holds it until the test lets it finish."""

    gate = threading.Event()
    removed: ClassVar[list[str]] = []

    def __init__(self, registry, envs_dir, models_dir, reporter):
        self.report = reporter

    def install(self, model_id, lane=None, weights=True):
        self.report.step("checks", "done")
        self.report.step("weights", "running")
        self.report.bytes(512, 1024)
        self.report.log("half way")
        while not self.gate.wait(0.02):
            self.report.check()
        self.report.check()
        self.report.step("weights", "done")

    def remove(self, model_id):
        self.removed.append(model_id)


@pytest.fixture
def client(tmp_path, monkeypatch):
    monkeypatch.setattr(engine_app, "Installer", FakeInstaller)
    FakeInstaller.gate = threading.Event()
    FakeInstaller.removed = []
    app = create_app(EngineConfig(store_root=tmp_path / "store", token="secret", local_hosts=("testclient",)))
    with TestClient(app, headers={"Authorization": "Bearer secret"}) as c:
        yield c


def follow(client, task_id):
    """Every state the event stream sends until the task ends."""
    states = []
    with client.stream("GET", f"/v1/tasks/{task_id}/events") as resp:
        for line in resp.iter_lines():
            if line.startswith("data: "):
                states.append(json.loads(line[6:]))
    return states


def test_an_install_streams_steps_and_bytes_until_it_ends(client):
    resp = client.post("/v1/models/triposg/install")
    assert resp.status_code == 202
    task = resp.json()
    assert task["kind"] == "install" and task["target"] == "triposg"
    # A second request joins the running install instead of starting another.
    assert client.post("/v1/models/triposg/install").json()["id"] == task["id"]
    FakeInstaller.gate.set()
    states = follow(client, task["id"])
    final = states[-1]
    assert final["status"] == "succeeded", final
    assert {s["id"]: s["status"] for s in final["steps"]}["weights"] == "done"
    assert "half way" in final["log"]


def test_cancelling_an_install_stops_it(client):
    task = client.post("/v1/models/triposg/install").json()
    assert client.delete(f"/v1/tasks/{task['id']}").status_code == 200
    final = follow(client, task["id"])[-1]
    assert final["status"] == "cancelled"


def test_models_say_where_they_would_run_and_how_well(client):
    models = {m["id"]: m for m in client.get("/v1/models").json()}
    assert models["stub-shape"]["runs_on"] == "cpu" and models["stub-shape"]["fit"] == "fits"
    assert "fit" in models["triposg"] and "runs_on" in models["triposg"]
    assert models["hunyuan3d2-paint"]["license"]["url"].startswith("https://")


def test_engine_models_cannot_be_removed_but_installed_ones_can(client):
    assert client.delete("/v1/models/stub-shape").status_code == 409
    task = client.delete("/v1/models/triposg").json()
    assert follow(client, task["id"])[-1]["status"] == "succeeded"
    assert FakeInstaller.removed == ["triposg"]


def failing(cmd=("installer", "--quiet")):
    return checks.Check(
        id="uv",
        label="uv",
        ok=False,
        found="not found",
        need="any",
        why="test",
        fix=checks.Fix(label="Install uv", command=cmd),
    )


def test_a_fix_runs_its_command_then_checks_again(client, monkeypatch):
    ran = []
    monkeypatch.setattr(engine_app, "_run", lambda cmd, log, **kw: ran.append(cmd))
    passing = checks.Check(id="uv", label="uv", ok=True, found="uv", need="any", why="")
    results = iter([[failing()], [passing]])
    monkeypatch.setattr(engine_app.checks, "for_models", lambda *a, **kw: next(results))
    task = client.post("/v1/checks/uv/fix").json()
    final = follow(client, task["id"])[-1]
    assert final["status"] == "succeeded", final
    assert ran == [["installer", "--quiet"]]


def test_a_fix_that_does_not_help_fails(client, monkeypatch):
    monkeypatch.setattr(engine_app, "_run", lambda cmd, log, **kw: None)
    monkeypatch.setattr(engine_app.checks, "for_models", lambda *a, **kw: [failing()])
    task = client.post("/v1/checks/uv/fix").json()
    final = follow(client, task["id"])[-1]
    assert final["status"] == "failed" and "still does not pass" in final["error"]


def test_fixes_only_run_for_callers_on_this_computer(tmp_path, monkeypatch):
    monkeypatch.setattr(engine_app.checks, "for_models", lambda *a, **kw: [failing()])
    app = create_app(EngineConfig(store_root=tmp_path / "store", token="secret"))
    with TestClient(app, headers={"Authorization": "Bearer secret"}) as c:
        assert c.post("/v1/checks/uv/fix").status_code == 403


def test_checks_list_what_a_cuda_model_needs(client, monkeypatch):
    monkeypatch.setattr(checks.families, "detect_lane", lambda: "cuda")
    monkeypatch.setattr(engine_app.families, "detect_lane", lambda: "cuda")
    monkeypatch.setattr(checks, "driver_version", lambda: "560.94")

    def no_nvcc(want):
        raise checks.toolchain.ToolchainError("nvcc not found")

    monkeypatch.setattr(checks.toolchain, "check_cuda", no_nvcc)
    monkeypatch.setattr(checks.toolchain, "cuda_home", lambda want=None: None)
    body = client.get("/v1/checks?models=triposg").json()
    by_id = {c["id"]: c for c in body["checks"]}
    assert body["lane"] == "cuda" and not body["ok"]
    assert by_id["gpu-driver"]["ok"] and by_id["gpu-driver"]["fix"] is None
    assert not by_id["cuda-toolkit"]["ok"]
    assert by_id["cuda-toolkit"]["fix"]["label"] == "Install CUDA toolkit 12.6"
    assert "TripoSG" in by_id["cuda-toolkit"]["why"]
    assert {"cpp-build-tools", "uv", "disk"} <= set(by_id)
    # Failing checks come first.
    assert [c["ok"] for c in body["checks"]] == sorted(c["ok"] for c in body["checks"])


def test_a_model_this_machine_cannot_run_says_so(client, monkeypatch):
    monkeypatch.setattr(checks.families, "detect_lane", lambda: "mps")
    monkeypatch.setattr(engine_app.families, "detect_lane", lambda: "mps")
    body = client.get("/v1/checks?models=hunyuan3d2-paint").json()
    assert [c["id"] for c in body["checks"]] == ["runs-here:hunyuan3d2-paint"]
    assert body["checks"][0]["fix"]["runnable"] is False
