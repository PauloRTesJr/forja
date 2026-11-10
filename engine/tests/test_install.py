import io
import json
import sys
import tarfile
import threading
import types
import urllib.error
from pathlib import Path

import pytest

from forja_engine import families, fetch, install, toolchain
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
    # Cleaned away since, its marker left behind: extracting again puts it back.
    import shutil

    shutil.rmtree(dest / "pkg")
    assert install.extract_tarball(archive, dest) == dest
    assert (dest / "pkg" / "mod.py").is_file()


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
        install, "_run", lambda cmd, log, env=None, cwd=None, **kw: commands.append([str(c) for c in cmd])
    )
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")
    monkeypatch.setattr(install.toolchain, "build_env", lambda backend, log: {"CUDA_HOME": "/cuda"})
    monkeypatch.setattr(install, "fetch_verified", lambda url, sha, cache, log, **kw: Path("/cache") / sha)
    up = tmp_path / "envs" / "triposg" / "upstream"
    monkeypatch.setattr(install, "extract_tarball", lambda archive, dest, **kw: up)
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
        install, "_run", lambda cmd, log, env=None, cwd=None, **kw: commands.append([str(c) for c in cmd])
    )
    monkeypatch.setattr(install.toolchain, "cuda_home", lambda want=None: None)
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    with pytest.raises(install.InstallError, match="nvcc not found"):
        inst.install("triposg", lane="cuda")
    assert commands == []


def test_torch_cuda_version():
    assert toolchain.torch_cuda("cu124") == (12, 4)
    assert toolchain.torch_cuda("cu128") == (12, 8)


def test_nvcc_must_match_torch(monkeypatch, tmp_path):
    monkeypatch.setattr(toolchain, "cuda_home", lambda want=None: tmp_path)
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


class Steps(install.Reporter):
    def __init__(self) -> None:
        super().__init__(log=lambda s: None)
        self.seen: list[tuple[str, str]] = []

    def step(self, step: str, status: str) -> None:
        self.seen.append((step, status))


def test_install_reports_every_step(tmp_path, monkeypatch):
    monkeypatch.setattr(install, "_run", lambda cmd, log, **kw: None)
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")
    monkeypatch.setattr(install.toolchain, "build_env", lambda backend, log: {})
    monkeypatch.setattr(install, "fetch_verified", lambda url, sha, cache, log, **kw: Path("/cache") / sha)
    monkeypatch.setattr(install, "extract_tarball", lambda archive, dest, **kw: dest)
    site = tmp_path / "site"
    site.mkdir()
    monkeypatch.setattr(install, "site_packages", lambda py: site)
    monkeypatch.setattr(install.Installer, "weights", lambda self, m: None)
    rep = Steps()
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", reporter=rep)
    inst.install("birefnet", lane="cpu")
    final = dict(rep.seen)
    assert [s for s, _ in install.STEPS] == list(final)
    # BiRefNet has no upstream source and compiles nothing.
    assert final["upstream"] == "skipped" and final["build"] == "skipped"
    assert final["environment"] == final["weights"] == final["smoke"] == "done"


def test_a_cancelled_install_stops_at_the_next_step(tmp_path, monkeypatch):
    rep = Steps()
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")

    def run(cmd, log, **kw):
        rep.cancel.set()  # the user cancels while the environment builds

    monkeypatch.setattr(install, "_run", run)
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", reporter=rep)
    with pytest.raises(install.InstallCancelled):
        inst.install("birefnet", lane="cpu")
    assert ("weights", "running") not in rep.seen


def test_run_streams_lines_and_lets_a_caller_claim_some(tmp_path):
    lines, claimed = [], []
    script = "print('hello'); print('FORJA_PROGRESS 5 10'); print('bye')"

    def claim(line: str) -> bool:
        if line.startswith("FORJA_PROGRESS"):
            claimed.append(line)
            return True
        return False

    install._run([sys.executable, "-c", script], lines.append, on_line=claim)
    assert claimed == ["FORJA_PROGRESS 5 10"]
    assert "  hello" in lines and "  bye" in lines


def test_remove_drops_the_environment_with_its_last_model(tmp_path):
    envs, models = tmp_path / "envs", tmp_path / "models"
    env = envs / "birefnet"
    (env / "Scripts").mkdir(parents=True)
    (models / "birefnet").mkdir(parents=True)
    (models / "birefnet-lite").mkdir(parents=True)
    (env / "forja-installed.json").write_text(json.dumps({"models": {"birefnet": {}, "birefnet-lite": {}}}))
    inst = install.Installer(Registry.load(), envs, models, log=lambda s: None)
    inst.remove("birefnet-lite")
    assert env.is_dir() and not (models / "birefnet-lite").exists()
    inst.remove("birefnet")
    assert not env.exists() and not (models / "birefnet").exists()
    with pytest.raises(install.InstallError, match="part of the engine"):
        inst.remove("stub-shape")


def test_interpreters_live_in_the_data_folder(tmp_path, monkeypatch):
    envs = {}
    monkeypatch.delenv("UV_PYTHON_INSTALL_DIR", raising=False)
    monkeypatch.setattr(install, "_run", lambda cmd, log, env=None, **kw: envs.setdefault(str(cmd[1]), env))
    monkeypatch.setattr(install, "uv_binary", lambda: "uv")
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    inst.environment(families.load("birefnet"), "cpu", tmp_path / "envs" / "birefnet")
    assert envs["venv"]["UV_PYTHON_INSTALL_DIR"] == str(tmp_path / "_uv" / "python")


def test_an_install_record_cut_short_reads_as_not_installed(tmp_path):
    """The engine is stopped by killing it. A torn record used to raise from every listing of the
    models and from the engine's own start: nothing ran again until the file was deleted by hand."""
    env = tmp_path / "envs" / "triposg"
    python = families.interpreter(env)
    python.parent.mkdir(parents=True)
    python.write_bytes(b"")
    record = env / "forja-installed.json"
    for torn in (b"", b'{"models": {"tripo', b"[]", b'{"models": null}'):
        record.write_bytes(torn)
        assert install.installed_models(env) == set()
        assert not Registry.load().bind(tmp_path / "envs", tmp_path / "models").models["triposg"].installed
    # Installing again writes a whole record over it.
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", log=lambda s: None)
    inst._record(env, inst.manifest("triposg"), "cuda")
    assert install.installed_models(env) == {"triposg"}
    assert [p.name for p in env.iterdir() if p.is_file()] == ["forja-installed.json"]


def test_a_command_that_cannot_start_names_the_program():
    with pytest.raises(install.InstallError, match="could not start forja-no-such-program"):
        install._run(["forja-no-such-program", "--version"], lambda line: None)


def test_a_download_that_fails_says_so_and_leaves_no_part_file(tmp_path, monkeypatch):
    def offline(url, timeout=None):
        raise urllib.error.URLError("getaddrinfo failed")

    monkeypatch.setattr(install.urllib.request, "urlopen", offline)
    with pytest.raises(install.InstallError, match=r"could not download https://example\.invalid"):
        install.fetch_verified("https://example.invalid/src.tar.gz", "0" * 64, tmp_path / "cache", print)
    assert list((tmp_path / "cache").iterdir()) == []


def test_a_cancel_stops_a_download_a_hash_and_an_extraction_part_way(tmp_path):
    """Cancel was only looked at between steps and inside child processes: during a long download,
    the hash of a multi-GB file or an extraction, pressing it did nothing until they ended."""
    cancel = threading.Event()
    cancel.set()
    src = tmp_path / "blob"
    src.write_bytes(b"x" * (3 << 20))
    good = install.sha256_file(src)
    with pytest.raises(install.InstallCancelled):
        install.sha256_file(src, cancel=cancel)
    cache = tmp_path / "cache"
    with pytest.raises(install.InstallCancelled):
        install.fetch_verified(src.as_uri(), good, cache, lambda s: None, cancel=cancel)
    assert list(cache.iterdir()) == []  # no .part, and nothing a later install would trust

    archive = tmp_path / "src.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        info = tarfile.TarInfo("top/a.py")
        info.size = 3
        tar.addfile(info, io.BytesIO(b"a=1"))
    with pytest.raises(install.InstallCancelled):
        install.extract_tarball(archive, tmp_path / "upstream", cancel=cancel)
    assert not (tmp_path / "upstream").exists() and not (tmp_path / "upstream.tmp").exists()
    assert (install.extract_tarball(archive, tmp_path / "upstream") / "a.py").read_text() == "a=1"

    # The installer hands its own cancel to them.
    reporter = install.Reporter(lambda s: None)
    reporter.cancel.set()
    inst = install.Installer(Registry.load(), tmp_path / "envs", tmp_path / "models", reporter=reporter)
    with pytest.raises(install.InstallCancelled):
        inst._file(src.as_uri(), good, tmp_path / "models" / "m" / "extra.bin")
    assert not (tmp_path / "models" / "m" / "extra.bin").exists()


def test_weights_behind_a_licence_say_how_to_get_access(tmp_path, monkeypatch, capsys):
    """A gated repository (Pixal3D's DINOv3 encoder) failed with the hub's own page of text."""

    class GatedRepoError(Exception):
        pass

    class HfApi:
        def model_info(self, *args, **kwargs):
            raise GatedRepoError("401 Client Error")

    def refused(**kwargs):
        raise GatedRepoError("401 Client Error. Cannot access gated repo for url https://...")

    hub = types.ModuleType("huggingface_hub")
    hub.HfApi, hub.snapshot_download = HfApi, refused
    utils = types.ModuleType("huggingface_hub.utils")
    utils.filter_repo_objects = lambda *args, **kwargs: []
    monkeypatch.setitem(sys.modules, "huggingface_hub", hub)
    monkeypatch.setitem(sys.modules, "huggingface_hub.utils", utils)
    spec = {"repo": "facebook/dinov3-vitl16", "revision": "0" * 40, "dest": str(tmp_path / "w")}
    monkeypatch.setattr(sys, "argv", ["fetch", json.dumps(spec)])
    with pytest.raises(SystemExit) as stopped:
        fetch.main()
    last = capsys.readouterr().out.strip().splitlines()[-1]
    assert stopped.value.code == 1
    assert "HF_TOKEN" in last and "https://huggingface.co/facebook/dinov3-vitl16" in last
    # Anything else stays the error it is.
    assert fetch.access_problem(OSError("No space left on device"), "a/b") is None
