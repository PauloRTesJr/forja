"""The Apple Silicon lane: which families install there, and what an install runs."""

import json
from pathlib import Path

import pytest

from forja_engine import checks, families, install
from forja_engine.registry import Registry
from forja_engine.scheduler import Scheduler

GIB = 1 << 30


def test_builds_and_smoke_imports_can_differ_per_lane():
    fam = families.load("hunyuan3d2")
    assert fam.lanes == ("cuda", "mps")
    assert fam.builds("cuda") == ("{upstream}/hy3dgen/texgen/custom_rasterizer",)
    assert fam.builds("mps") == () and "custom_rasterizer" not in fam.smoke("mps")
    assert "hy3dgen.shapegen" in fam.smoke("mps")
    # The list form applies to every lane, as before.
    triposg = families.load("triposg")
    assert triposg.builds("cuda") == ("diso==0.1.4",) and triposg.smoke("cuda")


def test_a_per_lane_value_must_name_the_family_lanes(tmp_path, monkeypatch):
    fam = tmp_path / "odd"
    fam.mkdir()
    (fam / "family.yaml").write_text("name: odd\nlanes: [cuda]\nbuild: { mps: [x] }\n")
    monkeypatch.setenv("FORJA_FAMILIES_DIR", str(tmp_path))
    with pytest.raises(ValueError, match="mps"):
        families.load("odd")


def test_every_backend_a_model_runs_on_is_a_lane_of_its_family():
    """A manifest that runs on a backend its family cannot install would pass every check and
    then fail at install."""
    for m in Registry.load().models.values():
        if m.in_engine_env:
            continue
        fam = families.load(m.family)
        for backend in families.LANES:
            if m.supports(backend):
                assert backend in fam.lanes, f"{m.id} runs on {backend}, but {m.family} has no {backend} lane"


def plan(monkeypatch, tmp_path, model, lane):
    commands = []
    monkeypatch.setattr(
        install, "_run", lambda cmd, log, env=None, cwd=None, **kw: commands.append([str(c) for c in cmd])
    )
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")
    monkeypatch.setattr(install, "fetch_verified", lambda url, sha, cache, log: Path("/cache") / sha)
    monkeypatch.setattr(install, "extract_tarball", lambda archive, dest: dest)
    site = tmp_path / "site"
    site.mkdir(exist_ok=True)
    monkeypatch.setattr(install, "site_packages", lambda py: site)
    monkeypatch.setattr(install.Installer, "weights", lambda self, m: None)

    def no_toolchain(backend, log):
        raise AssertionError("an mps install needs no CUDA toolkit")

    monkeypatch.setattr(install.toolchain, "build_env", no_toolchain)
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    env = inst.install(model, lane=lane)
    return commands, env


def test_the_shape_model_installs_on_an_apple_gpu_without_compiling(monkeypatch, tmp_path):
    commands, env = plan(monkeypatch, tmp_path, "hunyuan3d2-mv", "mps")
    pip = next(c for c in commands if c[:3] == ["uv", "pip", "install"] and "-r" in c)
    assert pip[pip.index("-r") + 1].endswith(str(Path("hunyuan3d2", "requirements", "mps.txt")))
    assert "--torch-backend" not in pip  # PyPI's macOS torch carries MPS
    assert not any("--no-build-isolation" in c for c in commands)
    scripts = [c[-1] for c in commands if c[1:2] == ["-c"]]
    assert "import torch, hy3dgen.shapegen" in scripts
    assert any('device="mps"' in s for s in scripts) and not any('device="cuda"' in s for s in scripts)
    state = json.loads((env / "forja-installed.json").read_text())
    assert state["lane"] == "mps" and "hunyuan3d2-mv" in state["models"]


def test_birefnet_installs_on_a_mac_and_skips_the_gpu_probe(monkeypatch, tmp_path):
    commands, _ = plan(monkeypatch, tmp_path, "birefnet", "mps")
    assert not any('device="mps"' in c[-1] for c in commands if c[1:2] == ["-c"])


@pytest.mark.parametrize("model", ["hunyuan3d2-paint", "triposg"])
def test_cuda_only_models_are_refused_on_an_apple_gpu(tmp_path, model):
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    with pytest.raises(install.InstallError, match="runs on lanes cuda; this machine is mps"):
        inst.install(model, lane="mps")


def test_a_mac_checks_only_what_its_models_need(fake_mac, tmp_path):
    reg = Registry.load()
    real = [m for m in reg.models.values() if not m.stub]
    found = {c.id: c for c in checks.for_models(real, tmp_path)}
    for model in ("hunyuan3d2-paint", "triposg"):
        c = found[f"runs-here:{model}"]
        assert not c.ok and c.found == "an Apple GPU" and c.need == "an NVIDIA GPU (CUDA)"
        assert c.fix and c.fix.label == "Run it on another computer"
    assert "runs-here:hunyuan3d2-mv" not in found and "runs-here:birefnet" not in found
    # Nothing compiles and no NVIDIA driver is needed on the mps lane.
    assert not {"gpu-driver", "cuda-toolkit", "cpp-build-tools"} & set(found)


def test_on_a_mac_shape_runs_on_the_apple_gpu_and_texture_elsewhere(tmp_path):
    envs = tmp_path / "envs"
    for family, models in {"birefnet": ["birefnet"], "hunyuan3d2": ["hunyuan3d2-mv"]}.items():
        python = families.interpreter(envs / family)
        python.parent.mkdir(parents=True)
        python.touch()
        state = {"lane": "mps", "models": {m: {} for m in models}}
        (envs / family / "forja-installed.json").write_text(json.dumps(state))
    reg = Registry.load().bind(envs, tmp_path / "models")
    mac, small = Scheduler({"mps": 21 * GIB, "cpu": 31 * GIB}), Scheduler({"mps": 4 * GIB, "cpu": 7 * GIB})
    try:
        assert mac.place(reg.models["hunyuan3d2-mv"]) == "mps"
        assert mac.fit(reg.models["hunyuan3d2-mv"]) == ("mps", "fits")
        assert mac.place(reg.models["birefnet"]) == "cpu"
        for model in ("hunyuan3d2-paint", "triposg"):
            assert mac.fit(reg.models[model]) == (None, "unsupported")
        # An 8 GB Mac keeps about 4 GB for the GPU: below the shape model's minimum.
        assert small.fit(reg.models["hunyuan3d2-mv"]) == ("mps", "too-big")
    finally:
        mac.pool.shutdown()
        small.pool.shutdown()
