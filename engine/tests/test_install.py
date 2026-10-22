import io
import json
import tarfile
from pathlib import Path

import pytest

from forja_engine import families, install, toolchain
from forja_engine.registry import Registry


def test_fetch_verified_refuses_a_wrong_hash(tmp_path):
    src = tmp_path / "blob"
    src.write_bytes(b"model bytes")
    good = install.sha256_file(src)
    got = install.fetch_verified(src.as_uri(), good, tmp_path / "cache", print)
    assert got.read_bytes() == b"model bytes"
    with pytest.raises(install.InstallError, match="sha256"):
        install.fetch_verified(src.as_uri(), "0" * 64, tmp_path / "cache", print)


def test_extract_drops_the_top_folder(tmp_path):
    archive = tmp_path / "src.tgz"
    with tarfile.open(archive, "w:gz") as tar:
        data = b"print('hi')"
        info = tarfile.TarInfo("Repo-abc123/pkg/mod.py")
        info.size = len(data)
        tar.addfile(info, io.BytesIO(data))
    dest = install.extract_tarball(archive, tmp_path / "upstream")
    assert (dest / "pkg" / "mod.py").read_bytes() == b"print('hi')"


def test_family_specs_parse():
    for name in ("birefnet", "triposg", "hunyuan3d2"):
        fam = families.load(name)
        for lane in fam.lanes:
            assert fam.requirements(lane).is_file(), (name, lane)
        if fam.upstream:
            assert len(fam.upstream.sha256) == 64 and fam.upstream.url.startswith("https://")
    assert families.load("triposg").torch_backend["cuda"] == "cu124"


def test_install_plan(tmp_path, monkeypatch):
    """The command sequence for a CUDA family, without running anything."""
    commands = []
    monkeypatch.setattr(
        install, "_run", lambda cmd, log, env=None, cwd=None: commands.append([str(c) for c in cmd])
    )
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")
    monkeypatch.setattr(install.toolchain, "build_env", lambda backend, log: {"CUDA_HOME": "/cuda"})
    monkeypatch.setattr(install, "fetch_verified", lambda url, sha, cache, log: Path("/cache") / sha)
    up = tmp_path / "envs" / "triposg" / "upstream"
    monkeypatch.setattr(install, "extract_tarball", lambda archive, dest: up)
    site = tmp_path / "site"
    site.mkdir()
    monkeypatch.setattr(install, "site_packages", lambda py: site)
    weights = []
    monkeypatch.setattr(install.Installer, "weights", lambda self, m: weights.append(m.id))

    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    env = inst.install("triposg", lane="cuda")

    pip = next(c for c in commands if c[:3] == ["uv", "pip", "install"] and "-r" in c)
    assert pip[pip.index("--torch-backend") + 1] == "cu124"
    assert "--no-build-isolation" in commands[2] and commands[2][-1] == "diso==0.1.4"
    assert any("triposg.pipelines" in " ".join(c) for c in commands)  # family smoke import
    assert (site / "forja-triposg-upstream.pth").read_text().strip() == str(up.resolve())
    assert weights == ["triposg"]
    assert "triposg" in json.loads((env / "forja-installed.json").read_text())["models"]


def test_install_refuses_unsupported_lane(tmp_path):
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    with pytest.raises(install.InstallError, match="remote provider"):
        inst.install("hunyuan3d2-paint", lane="mps")


def test_uninstalled_models_are_refused_at_placement(tmp_path):
    from forja_engine.scheduler import Scheduler
    from forja_engine.workers import StageFailed

    reg = Registry.load().bind(tmp_path / "envs", tmp_path / "models")
    with pytest.raises(StageFailed, match="forja models install triposg"):
        Scheduler({"cuda:0": 1 << 34}).place(reg.models["triposg"])


def test_install_stops_before_building_anything_without_nvcc(tmp_path, monkeypatch):
    commands = []
    monkeypatch.setattr(
        install, "_run", lambda cmd, log, env=None, cwd=None: commands.append([str(c) for c in cmd])
    )
    monkeypatch.setattr(install.toolchain, "cuda_home", lambda: None)
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    with pytest.raises(install.InstallError, match="nvcc not found"):
        inst.install("triposg", lane="cuda")
    assert commands == []


def test_torch_cuda_version():
    assert toolchain.torch_cuda("cu124") == (12, 4)
    assert toolchain.torch_cuda("cu128") == (12, 8)


def test_nvcc_must_match_torch(monkeypatch, tmp_path):
    monkeypatch.setattr(toolchain, "cuda_home", lambda: tmp_path)
    for have, ok in (((12, 6), True), ((12, 4), True), ((12, 3), False), ((13, 0), False)):
        monkeypatch.setattr(toolchain, "nvcc_version", lambda home, have=have: have)
        if ok:
            assert toolchain.check_cuda((12, 4)) == (tmp_path, have)
        else:
            with pytest.raises(toolchain.ToolchainError, match="cannot build"):
                toolchain.check_cuda((12, 4))


def test_msvc_toolset_nvcc_accepts_wins_over_newer_ones(tmp_path):
    vs = tmp_path / "VS"
    (vs / "VC" / "Auxiliary" / "Build").mkdir(parents=True)
    (vs / "VC" / "Auxiliary" / "Build" / "vcvarsall.bat").write_text("")
    for ver in ("14.51.36231", "14.44.35207", "14.38.33130"):
        cl = vs / "VC" / "Tools" / "MSVC" / ver / "bin" / "Hostx64" / "x64"
        cl.mkdir(parents=True)
        (cl / "cl.exe").write_text("")
    limit = toolchain.max_msvc_toolset((12, 6))
    bat, toolset = toolchain.find_msvc(limit, installs=[vs])
    assert toolset == "14.44.35207" and bat.name == "vcvarsall.bat"
    with pytest.raises(toolchain.ToolchainError, match=r"VC\.14\.44"):
        toolchain.find_msvc((14, 30), installs=[vs])
