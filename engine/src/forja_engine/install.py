"""`forja-engine install <model>`: build the family environment and fetch pinned weights.

Every step is idempotent, so an interrupted install resumes by running it again:

1. Check the toolchain the family's compiled extensions need.
2. Create `<envs>/<family>` with uv at the family's Python version, and install the lane's
   requirements, forja-engine itself and the adapter package.
3. Download the pinned upstream source, verify its sha256, extract it, put it on the path.
4. Compile the family's extensions against the installed torch.
5. Download the model's weights at a pinned revision; verify listed files by sha256.
6. Smoke-test: import the adapter and, on GPU lanes, touch the device.

Steps report to a Reporter, which the engine's task API turns into live progress.
"""

from __future__ import annotations

import contextlib
import hashlib
import json
import os
import shutil
import stat
import subprocess
import sys
import tarfile
import threading
import time
import urllib.request
from collections import deque
from collections.abc import Callable
from pathlib import Path
from typing import Any

from forja_engine import families, toolchain
from forja_engine.registry import Manifest, Registry

Log = Callable[[str], None]
_CHUNK = 1 << 20
_PROGRESS = "FORJA_PROGRESS "
_GPU_PROBE = """
import importlib.util
if importlib.util.find_spec("torch"):
    import torch
    assert torch.cuda.is_available(), "torch cannot see the GPU: check the NVIDIA driver"
    torch.ones(1, device="cuda")
"""

# Shown in this order; each install reports every step as running, done, skipped or failed.
STEPS = (
    ("checks", "Check this computer"),
    ("environment", "Python environment"),
    ("upstream", "Model code"),
    ("build", "GPU extensions"),
    ("weights", "Weights"),
    ("smoke", "Test on this device"),
)


class InstallError(RuntimeError):
    pass


class InstallCancelled(InstallError):
    pass


class Reporter:
    """Where an install reports. This one only logs; the task API records steps and bytes."""

    def __init__(self, log: Log = print) -> None:
        self.log = log
        self.cancel = threading.Event()

    def step(self, step: str, status: str) -> None:
        pass

    def bytes(self, done: int, total: int) -> None:
        pass

    def check(self) -> None:
        if self.cancel.is_set():
            raise InstallCancelled("cancelled")


def uv_binary() -> str:
    home = Path.home()
    places = [os.environ.get("FORJA_UV"), os.environ.get("UV"), shutil.which("uv")]
    if sys.platform == "win32":
        # Where winget and uv's own installer put it, for an engine started before either ran.
        local = Path(os.environ.get("LOCALAPPDATA", home / "AppData" / "Local"))
        places += [
            str(local / "Microsoft" / "WinGet" / "Links" / "uv.exe"),
            str(home / ".local" / "bin" / "uv.exe"),
        ]
    else:
        places += [str(home / ".local" / "bin" / "uv"), str(home / ".cargo" / "bin" / "uv")]
    for p in places:
        if p and Path(p).is_file():
            return p
    raise InstallError("uv is required to build model environments: https://docs.astral.sh/uv/")


def _kill_tree(proc: subprocess.Popen[str]) -> None:
    import psutil

    with contextlib.suppress(psutil.Error):
        for child in psutil.Process(proc.pid).children(recursive=True):
            with contextlib.suppress(psutil.Error):
                child.kill()
    with contextlib.suppress(OSError):
        proc.kill()


def _run(
    cmd: list[str | Path],
    log: Log,
    env: dict[str, str] | None = None,
    cwd: Path | None = None,
    cancel: threading.Event | None = None,
    on_line: Callable[[str], bool] | None = None,
) -> None:
    """Run a command, logging its output as it comes; on_line may claim a line (returns True)."""
    log("$ " + " ".join(str(c) for c in cmd))
    proc = subprocess.Popen(
        [str(c) for c in cmd],
        env={**os.environ, **(env or {})},
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        encoding="utf-8",
        errors="replace",
        creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
    )
    stop = threading.Event()

    def watch() -> None:
        while not stop.wait(0.2):
            if cancel is not None and cancel.is_set():
                _kill_tree(proc)
                return

    watcher = threading.Thread(target=watch, daemon=True)
    watcher.start()
    tail: deque[str] = deque(maxlen=20)
    assert proc.stdout is not None
    for raw in proc.stdout:
        line = raw.rstrip()
        if on_line is not None and on_line(line):
            continue
        tail.append(line)
        log("  " + line)
    code = proc.wait()
    stop.set()
    if cancel is not None and cancel.is_set():
        raise InstallCancelled("cancelled")
    if code != 0:
        last = next((x.strip() for x in reversed(tail) if x.strip()), "")
        raise InstallError(
            f"command failed with exit code {code}: {cmd[0]} {cmd[1] if len(cmd) > 1 else ''}"
            + (f": {last}" if last else "")
        )


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        while chunk := f.read(_CHUNK):
            h.update(chunk)
    return h.hexdigest()


def fetch_verified(url: str, sha256: str, cache: Path, log: Log) -> Path:
    """Download url into a content-addressed cache, refusing anything that fails the hash."""
    cache.mkdir(parents=True, exist_ok=True)
    dst = cache / sha256
    if dst.is_file() and sha256_file(dst) == sha256:
        return dst
    tmp = dst.with_suffix(".part")
    log(f"downloading {url}")
    with urllib.request.urlopen(url, timeout=60) as resp, tmp.open("wb") as f:
        shutil.copyfileobj(resp, f, _CHUNK)
    actual = sha256_file(tmp)
    if actual != sha256:
        tmp.unlink(missing_ok=True)
        raise InstallError(f"{url}: sha256 is {actual}, expected {sha256}; refusing to use it")
    os.replace(tmp, dst)
    return dst


def extract_tarball(archive: Path, dest: Path) -> Path:
    """Extract a GitHub codeload tarball, dropping its single top-level folder."""
    if dest.is_dir() and (dest / ".forja-extracted").is_file():
        return dest
    tmp = dest.with_name(dest.name + ".tmp")
    shutil.rmtree(tmp, ignore_errors=True)
    tmp.mkdir(parents=True)
    with tarfile.open(archive, "r:gz") as tar:
        tar.extractall(tmp, filter="data")
    tops = [p for p in tmp.iterdir()]
    root = tops[0] if len(tops) == 1 and tops[0].is_dir() else tmp
    shutil.rmtree(dest, ignore_errors=True)
    shutil.move(str(root), dest)
    shutil.rmtree(tmp, ignore_errors=True)
    (dest / ".forja-extracted").write_text(archive.name)
    return dest


def site_packages(python: Path) -> Path:
    out = subprocess.run(
        [str(python), "-c", "import sysconfig; print(sysconfig.get_paths()['purelib'])"],
        check=True,
        capture_output=True,
        text=True,
    )
    return Path(out.stdout.strip())


def rmtree(path: Path) -> None:
    """Delete a folder, including the read-only files pip and git leave on Windows."""

    def writable(func: Callable[[str], object], p: str, *_: object) -> None:
        os.chmod(p, stat.S_IWRITE)
        func(p)

    if not path.exists():
        return
    if sys.version_info >= (3, 12):
        shutil.rmtree(path, onexc=writable)
    else:
        shutil.rmtree(path, onerror=writable)


class Installer:
    def __init__(
        self,
        registry: Registry,
        envs_dir: Path,
        models_dir: Path,
        log: Log = print,
        reporter: Reporter | None = None,
    ) -> None:
        self.registry = registry
        self.envs_dir = envs_dir
        self.models_dir = models_dir
        self.report = reporter or Reporter(log)
        self.log = self.report.log

    def manifest(self, model_id: str) -> Manifest:
        manifest = self.registry.models.get(model_id)
        if manifest is None:
            raise InstallError(
                f"unknown model {model_id!r}; known: {', '.join(sorted(self.registry.models))}"
            )
        return manifest

    def _step(self, name: str, fn: Callable[[], None] | None) -> None:
        if fn is None:
            self.report.step(name, "skipped")
            return
        self.report.check()
        self.report.step(name, "running")
        try:
            fn()
        except BaseException:
            self.report.step(name, "failed")
            raise
        self.report.step(name, "done")

    def install(self, model_id: str, lane: str | None = None, weights: bool = True) -> Path:
        manifest = self.manifest(model_id)
        if manifest.family in ("stub", "builtin"):
            self.log(f"{model_id} runs in the engine's own environment; nothing to install")
            for name, _ in STEPS:
                self.report.step(name, "skipped")
            return Path()
        fam = families.load(manifest.family)
        lane = lane or families.detect_lane()
        if lane not in fam.lanes:
            self.report.step("checks", "failed")
            raise InstallError(
                f"{model_id} ({fam.name}) supports lanes {', '.join(fam.lanes)}; this machine is {lane}. "
                "Route this model to a remote provider instead."
            )
        started = time.monotonic()
        build_env = dict(fam.env)
        env_dir = self.envs_dir / fam.name
        python = families.interpreter(env_dir)

        def checks() -> None:
            if fam.build and (backend := fam.torch_backend.get(lane, "")).startswith("cu"):
                try:
                    build_env.update(toolchain.build_env(backend, self.log))
                except toolchain.ToolchainError as e:
                    raise InstallError(f"{fam.name} compiles CUDA extensions: {e}") from e

        def environment() -> None:
            self.environment(fam, lane, env_dir)

        def upstream() -> None:
            self.upstream(fam, env_dir, python)

        def build() -> None:
            for spec in fam.build:
                target = spec.replace("{upstream}", str(env_dir / "upstream"))
                _run(
                    [uv_binary(), "pip", "install", "--python", python, "--no-build-isolation", target],
                    self.log,
                    env=build_env,
                    cancel=self.report.cancel,
                )

        def fetch() -> None:
            self.weights(manifest)

        def smoke() -> None:
            self.smoke(manifest, fam, python, lane)

        self._step("checks", checks)
        self._step("environment", environment)
        self._step("upstream", upstream if fam.upstream else None)
        self._step("build", build if fam.build else None)
        self._step("weights", fetch if weights and manifest.weights else None)
        self._step("smoke", smoke)
        self._record(env_dir, manifest, lane)
        self.log(f"installed {model_id} for {lane} in {time.monotonic() - started:.0f}s")
        return env_dir

    def environment(self, fam: families.Family, lane: str, env_dir: Path) -> Path:
        uv = uv_binary()
        python = families.interpreter(env_dir)
        if not python.exists():
            _run([uv, "venv", "--python", fam.python, "--seed", env_dir], self.log, cancel=self.report.cancel)
        reqs = fam.requirements(lane)
        if not reqs.is_file():
            raise InstallError(f"{fam.name} has no requirements for lane {lane} ({reqs})")
        cmd: list[str | Path] = [uv, "pip", "install", "--python", python, "-r", reqs]
        if backend := fam.torch_backend.get(lane):
            cmd += ["--torch-backend", backend]
        cmd += ["-e", families.ENGINE_ROOT, "-e", fam.root]
        _run(cmd, self.log, env=fam.env, cancel=self.report.cancel)
        return python

    def upstream(self, fam: families.Family, env_dir: Path, python: Path) -> None:
        assert fam.upstream
        up = fam.upstream
        archive = fetch_verified(up.url, up.sha256, self.envs_dir / "_downloads", self.log)
        src = extract_tarball(archive, env_dir / "upstream")
        self.log(f"upstream source ({up.license}) at {src}")
        pth = site_packages(python) / f"forja-{fam.name}-upstream.pth"
        pth.write_text("".join(f"{(src / p).resolve()}\n" for p in up.pythonpath))

    def weights(self, manifest: Manifest) -> Path:
        w: dict[str, Any] = manifest.weights
        if not w:
            return Path()
        if w.get("hub", "huggingface") != "huggingface":
            raise InstallError(f"{manifest.id}: weights hub {w.get('hub')!r} is not supported")
        dest = self.models_dir / manifest.id
        allow = w.get("allow") or None
        files = w.get("files") or []
        if files and not allow:
            allow = [f["path"] for f in files]
        self.log(f"fetching weights {w['repo']}@{w['revision'][:12]} into {dest}")
        spec = {
            "repo": w["repo"],
            "revision": w["revision"],
            "allow": allow,
            "ignore": w.get("ignore") or None,
            "dest": str(dest),
        }

        def progress(line: str) -> bool:
            if not line.startswith(_PROGRESS):
                return False
            done, total = (int(x) for x in line[len(_PROGRESS) :].split())
            self.report.bytes(done, total)
            return True

        # A child process, so cancelling stops a multi-GB file mid-way; HF_TOKEN reaches it
        # through the environment, never the command line.
        _run(
            [sys.executable, "-m", "forja_engine.fetch", json.dumps(spec)],
            self.log,
            cancel=self.report.cancel,
            on_line=progress,
        )
        for f in files:
            path = dest / f["path"]
            if "sha256" in f and sha256_file(path) != f["sha256"]:
                raise InstallError(f"{path}: sha256 mismatch; delete it and install again")
        return dest

    def smoke(self, manifest: Manifest, fam: families.Family, python: Path, lane: str) -> None:
        module = manifest.adapter.partition(":")[0]
        _run([python, "-c", f"import {module}"], self.log, cancel=self.report.cancel)
        if fam.smoke:
            _run([python, "-c", fam.smoke], self.log, cancel=self.report.cancel)
        if lane in ("cuda", "rocm"):
            _run([python, "-c", _GPU_PROBE], self.log, cancel=self.report.cancel)

    def remove(self, model_id: str) -> None:
        """Delete a model's weights, and its family environment when nothing else uses it."""
        manifest = self.manifest(model_id)
        if manifest.in_engine_env:
            raise InstallError(f"{model_id} is part of the engine; there is nothing to remove")
        env_dir = self.envs_dir / manifest.family
        self.log(f"removing {self.models_dir / model_id}")
        rmtree(self.models_dir / model_id)
        path = env_dir / "forja-installed.json"
        state = json.loads(path.read_text()) if path.is_file() else {"models": {}}
        state["models"].pop(model_id, None)
        if state["models"]:
            path.write_text(json.dumps(state, indent=2))
            return
        self.log(f"removing {env_dir}: no other model uses it")
        rmtree(env_dir)

    def _record(self, env_dir: Path, manifest: Manifest, lane: str) -> None:
        env_dir.mkdir(parents=True, exist_ok=True)
        path = env_dir / "forja-installed.json"
        state = json.loads(path.read_text()) if path.is_file() else {"models": {}}
        state["lane"] = lane
        state["models"][manifest.id] = {
            "revision": (manifest.weights or {}).get("revision"),
            "installed_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }
        path.write_text(json.dumps(state, indent=2))


def installed_models(env_dir: Path) -> set[str]:
    path = env_dir / "forja-installed.json"
    if not path.is_file() or not families.interpreter(env_dir).exists():
        return set()
    return set(json.loads(path.read_text()).get("models", {}))
