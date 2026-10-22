"""`forja-engine install <model>`: build the family environment and fetch pinned weights.

Every step is idempotent, so an interrupted install resumes by running it again:

1. Create `<envs>/<family>` with uv at the family's Python version.
2. Install the lane's requirements, forja-engine itself and the adapter package.
3. Download the pinned upstream source, verify its sha256, extract it, put it on the path.
4. Compile the family's extensions against the installed torch.
5. Download the model's weights at a pinned revision; verify listed files by sha256.
6. Smoke-test: import the adapter and, on GPU lanes, touch the device.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import tarfile
import time
import urllib.request
from collections.abc import Callable
from pathlib import Path
from typing import Any

from forja_engine import families, toolchain
from forja_engine.registry import Manifest, Registry

Log = Callable[[str], None]
_CHUNK = 1 << 20
_GPU_PROBE = """
import importlib.util
if importlib.util.find_spec("torch"):
    import torch
    assert torch.cuda.is_available(), "torch cannot see the GPU: check the NVIDIA driver"
    torch.ones(1, device="cuda")
"""


class InstallError(RuntimeError):
    pass


def uv_binary() -> str:
    uv = os.environ.get("FORJA_UV") or shutil.which("uv")
    if not uv:
        raise InstallError("uv is required to build model environments: https://docs.astral.sh/uv/")
    return uv


def _run(cmd: list[str | Path], log: Log, env: dict[str, str] | None = None, cwd: Path | None = None) -> None:
    log("$ " + " ".join(str(c) for c in cmd))
    proc = subprocess.run(
        [str(c) for c in cmd],
        env={**os.environ, **(env or {})},
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    for line in proc.stdout.splitlines()[-40:]:
        log("  " + line)
    if proc.returncode != 0:
        raise InstallError(
            f"command failed with exit code {proc.returncode}: {cmd[0]} {cmd[1] if len(cmd) > 1 else ''}"
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


class Installer:
    def __init__(self, registry: Registry, envs_dir: Path, models_dir: Path, log: Log = print) -> None:
        self.registry = registry
        self.envs_dir = envs_dir
        self.models_dir = models_dir
        self.log = log

    def install(self, model_id: str, lane: str | None = None, weights: bool = True) -> Path:
        manifest = self.registry.models.get(model_id)
        if manifest is None:
            raise InstallError(
                f"unknown model {model_id!r}; known: {', '.join(sorted(self.registry.models))}"
            )
        if manifest.family in ("stub", "builtin"):
            self.log(f"{model_id} runs in the engine's own environment; nothing to install")
            return Path()
        fam = families.load(manifest.family)
        lane = lane or families.detect_lane()
        if lane not in fam.lanes:
            raise InstallError(
                f"{model_id} ({fam.name}) supports lanes {', '.join(fam.lanes)}; this machine is {lane}. "
                "Route this model to a remote provider instead."
            )
        started = time.monotonic()
        build_env = dict(fam.env)
        if fam.build and (backend := fam.torch_backend.get(lane, "")).startswith("cu"):
            try:
                build_env.update(toolchain.build_env(backend, self.log))
            except toolchain.ToolchainError as e:
                raise InstallError(f"{fam.name} compiles CUDA extensions: {e}") from e
        env_dir = self.envs_dir / fam.name
        python = self.environment(fam, lane, env_dir)
        if fam.upstream:
            self.upstream(fam, env_dir, python)
        for spec in fam.build:
            target = spec.replace("{upstream}", str(env_dir / "upstream"))
            _run(
                [uv_binary(), "pip", "install", "--python", python, "--no-build-isolation", target],
                self.log,
                env=build_env,
            )
        if weights:
            self.weights(manifest)
        self.smoke(manifest, fam, python, lane)
        self._record(env_dir, manifest, lane)
        self.log(f"installed {model_id} for {lane} in {time.monotonic() - started:.0f}s")
        return env_dir

    def environment(self, fam: families.Family, lane: str, env_dir: Path) -> Path:
        uv = uv_binary()
        python = families.interpreter(env_dir)
        if not python.exists():
            _run([uv, "venv", "--python", fam.python, "--seed", env_dir], self.log)
        reqs = fam.requirements(lane)
        if not reqs.is_file():
            raise InstallError(f"{fam.name} has no requirements for lane {lane} ({reqs})")
        cmd: list[str | Path] = [uv, "pip", "install", "--python", python, "-r", reqs]
        if backend := fam.torch_backend.get(lane):
            cmd += ["--torch-backend", backend]
        cmd += ["-e", families.ENGINE_ROOT, "-e", fam.root]
        _run(cmd, self.log, env=fam.env)
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
        from huggingface_hub import snapshot_download

        dest = self.models_dir / manifest.id
        allow = w.get("allow") or None
        files = w.get("files") or []
        if files and not allow:
            allow = [f["path"] for f in files]
        self.log(f"fetching weights {w['repo']}@{w['revision'][:12]} into {dest}")
        snapshot_download(
            repo_id=w["repo"],
            revision=w["revision"],
            allow_patterns=allow,
            ignore_patterns=w.get("ignore") or None,
            local_dir=dest,
            token=os.environ.get("HF_TOKEN") or None,
        )
        for f in files:
            path = dest / f["path"]
            if "sha256" in f and sha256_file(path) != f["sha256"]:
                raise InstallError(f"{path}: sha256 mismatch; delete it and install again")
        return dest

    def smoke(self, manifest: Manifest, fam: families.Family, python: Path, lane: str) -> None:
        module = manifest.adapter.partition(":")[0]
        _run([python, "-c", f"import {module}"], self.log)
        if fam.smoke:
            _run([python, "-c", fam.smoke], self.log)
        if lane in ("cuda", "rocm"):
            _run([python, "-c", _GPU_PROBE], self.log)

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
