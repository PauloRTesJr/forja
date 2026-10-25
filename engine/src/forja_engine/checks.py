"""What this machine needs before models can be installed, as checks the app shows and fixes.

Each check says what was found, what is needed and why, and how to fix it: a command the
engine runs for the user (winget or the Visual Studio Installer on Windows) or steps to follow.
Toolchain checks matter only at install time: nothing compiles while models run.
"""

from __future__ import annotations

import shutil
import subprocess
import sys
from collections.abc import Iterable
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from forja_engine import families, toolchain
from forja_engine.registry import Manifest

GB = 1 << 30

# Oldest NVIDIA driver (major) that runs torch wheels built for each CUDA version.
_MIN_DRIVER = {(11, 8): 520, (12, 1): 530, (12, 4): 550, (12, 6): 560, (12, 8): 570}
_VS_COMPONENTS = (
    "Microsoft.VisualStudio.Component.VC.14.44.17.14.x86.x64",
    "Microsoft.VisualStudio.Component.Windows11SDK.26100",
)
_WINGET_ACCEPT = ("--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")


@dataclass(frozen=True)
class Fix:
    label: str
    # Run by the engine when the user asks; empty when only steps or a link can help.
    command: tuple[str, ...] = ()
    url: str = ""
    steps: tuple[str, ...] = ()
    # The installer will ask for administrator rights.
    admin: bool = False

    def to_json(self) -> dict[str, Any]:
        return {
            "label": self.label,
            "runnable": bool(self.command),
            "command": " ".join(self.command),
            "url": self.url,
            "steps": list(self.steps),
            "admin": self.admin,
        }


@dataclass(frozen=True)
class Check:
    id: str
    label: str
    ok: bool
    found: str
    need: str
    why: str
    fix: Fix | None = None
    # Models that need this check to pass.
    models: tuple[str, ...] = field(default=())

    def to_json(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "label": self.label,
            "ok": self.ok,
            "found": self.found,
            "need": self.need,
            "why": self.why,
            "fix": self.fix.to_json() if self.fix and not self.ok else None,
            "models": list(self.models),
        }


def _winget(*args: str) -> tuple[str, ...]:
    exe = shutil.which("winget")
    return (exe, "install", *args, *_WINGET_ACCEPT) if exe else ()


def _names(models: Iterable[Manifest]) -> str:
    names = [m.name for m in models]
    return names[0] if len(names) == 1 else ", ".join(names[:-1]) + " and " + names[-1]


def lane_label(lane: str) -> str:
    return {
        "cuda": "an NVIDIA GPU (CUDA)",
        "rocm": "an AMD GPU (ROCm)",
        "mps": "an Apple GPU",
        "cpu": "the CPU",
    }.get(lane, lane)


def driver_version() -> str | None:
    smi = shutil.which("nvidia-smi")
    if not smi:
        return None
    try:
        out = subprocess.run(
            [smi, "--query-gpu=driver_version", "--format=csv,noheader"],
            capture_output=True,
            text=True,
            timeout=10,
            check=True,
        ).stdout
    except (OSError, subprocess.SubprocessError):
        return None
    return out.strip().splitlines()[0].strip() if out.strip() else None


def check_runs_here(m: Manifest, fam: families.Family | None, lane: str) -> Check | None:
    if fam is None or lane in fam.lanes:
        return None
    return Check(
        id=f"runs-here:{m.id}",
        label=f"{m.name} on this computer",
        ok=False,
        found=lane_label(lane),
        need=" or ".join(lane_label(x) for x in fam.lanes),
        why=f"{m.name} has no build for this computer's hardware.",
        fix=Fix(
            label="Run it on another computer",
            steps=(
                "Install Forja on a PC that has the hardware, and share its GPU from Settings there.",
                "Then add that PC under Settings → Where models run on this computer.",
            ),
        ),
        models=(m.id,),
    )


def check_driver(models: list[Manifest], backends: dict[str, str]) -> Check:
    wants = [toolchain.torch_cuda(b) for b in backends.values()]
    need = max(_MIN_DRIVER.get(w, 550) for w in wants)
    found = driver_version()
    major = int(found.split(".")[0]) if found and found.split(".")[0].isdigit() else 0
    linux = sys.platform.startswith("linux")
    return Check(
        id="gpu-driver",
        label="NVIDIA driver",
        ok=major >= need,
        found=found or "not found",
        need=f"{need} or newer",
        why="Runs the models on your GPU.",
        fix=Fix(
            label="Update the NVIDIA driver",
            url="https://www.nvidia.com/en-us/drivers/",
            steps=(
                (
                    "Run: sudo ubuntu-drivers install, then restart."
                    if linux
                    else "Download the latest Game Ready or Studio driver for your GPU and install it."
                ),
                "Come back here: this check runs again by itself.",
            ),
        ),
        models=tuple(m.id for m in models),
    )


def check_cuda(models: list[Manifest], want: tuple[int, int]) -> Check:
    label = f"{want[0]}.{want[1]}"
    try:
        home, have = toolchain.check_cuda(want)
        ok, found = True, f"{have[0]}.{have[1]} at {home}"
    except toolchain.ToolchainError:
        ok, near = False, toolchain.cuda_home(want)
        found = "not found"
        if near is not None:
            try:
                have = toolchain.nvcc_version(near)
                found = f"{have[0]}.{have[1]} at {near}"
            except (toolchain.ToolchainError, OSError, subprocess.SubprocessError):
                found = f"unreadable at {near}"
    # 12.6 is the newest 12.x the families' builds are tested with.
    version = "12.6" if want[0] == 12 and want[1] <= 6 else label
    url = f"https://developer.nvidia.com/cuda-{version.replace('.', '-')}-0-download-archive"
    if sys.platform == "win32":
        fix = Fix(
            label=f"Install CUDA toolkit {version}",
            command=_winget("--id", "Nvidia.CUDA", "--version", version, "-e", "--silent"),
            url=url,
            steps=(f"Install CUDA toolkit {version} from NVIDIA, keeping the default options.",),
            admin=True,
        )
    else:
        fix = Fix(
            label=f"Install CUDA toolkit {version}",
            url=url,
            steps=(
                f"Install cuda-toolkit-{version.replace('.', '-')} from NVIDIA's package repository.",
                "Or point CUDA_HOME at an installed toolkit.",
            ),
        )
    return Check(
        id="cuda-toolkit",
        label="CUDA toolkit",
        ok=ok,
        found=found,
        need=f"{label} or a newer {want[0]}.x",
        why=f"Builds the GPU code of {_names(models)} once, while installing.",
        fix=fix,
        models=tuple(m.id for m in models),
    )


def check_cpp(models: list[Manifest], want: tuple[int, int]) -> Check:
    why = f"Compiles the GPU code of {_names(models)} together with the CUDA toolkit."
    ids = tuple(m.id for m in models)
    if sys.platform != "win32":
        compiler = shutil.which("g++") or shutil.which("c++")
        return Check(
            id="cpp-build-tools",
            label="C++ compiler",
            ok=compiler is not None,
            found=compiler or "not found",
            need="g++",
            why=why,
            fix=Fix(label="Install build tools", steps=("Run: sudo apt install build-essential",)),
            models=ids,
        )
    try:
        limit = toolchain.max_msvc_toolset(want)
        _, toolset = toolchain.find_msvc(limit)
    except toolchain.ToolchainError:
        limit, toolset = (14, 49), ""
    sdk = toolchain.windows_sdk()
    found = ", ".join(
        p for p in (f"MSVC {toolset}" if toolset else "", f"Windows SDK {sdk}" if sdk else "") if p
    )
    installs = toolchain.vs_installs()
    setup = toolchain.vs_installer()
    adds = tuple(x for c in _VS_COMPONENTS for x in ("--add", c))
    if installs and setup:
        command: tuple[str, ...] = (
            str(setup),
            "modify",
            "--installPath",
            str(installs[0]),
            *adds,
            "--passive",
            "--norestart",
        )
    else:
        override = " ".join(("--passive", "--wait", "--norestart", *adds))
        command = _winget("--id", "Microsoft.VisualStudio.2022.BuildTools", "-e", "--override", override)
    return Check(
        id="cpp-build-tools",
        label="C++ build tools",
        ok=bool(toolset and sdk),
        found=found or "not found",
        need=f"MSVC v143 ({limit[0]}.{limit[1]} or older) and a Windows 11 SDK",
        why=why + f" CUDA {want[0]} rejects the compiler of Visual Studio 2026.",
        fix=Fix(
            label="Add the C++ build tools",
            command=command,
            url="https://visualstudio.microsoft.com/visual-cpp-build-tools/",
            steps=(
                "In the Visual Studio Installer, add 'MSVC v143 - VS 2022 C++ x64/x86 build tools "
                "(v14.44)' and 'Windows 11 SDK (10.0.26100)'.",
            ),
            admin=True,
        ),
        models=ids,
    )


def check_uv(models: list[Manifest]) -> Check:
    from forja_engine.install import InstallError, uv_binary

    try:
        found, ok = uv_binary(), True
    except InstallError:
        found, ok = "not found", False
    if sys.platform == "win32":
        fix = Fix(
            label="Install uv",
            command=_winget("--id", "astral-sh.uv", "-e", "--silent"),
            url="https://docs.astral.sh/uv/",
        )
    else:
        fix = Fix(
            label="Install uv",
            url="https://docs.astral.sh/uv/",
            steps=("Run: curl -LsSf https://astral.sh/uv/install.sh | sh",),
        )
    return Check(
        id="uv",
        label="uv",
        ok=ok,
        found=found,
        need="any recent version",
        why="Builds each model's own Python environment.",
        fix=fix,
        models=tuple(m.id for m in models),
    )


def check_disk(models: list[Manifest], models_dir: Path) -> Check:
    need = sum(m.disk_gb for m in models)
    probe = models_dir
    while not probe.exists() and probe.parent != probe:
        probe = probe.parent
    free = shutil.disk_usage(probe).free
    return Check(
        id="disk",
        label="Disk space",
        ok=free >= need * GB,
        found=f"{free / GB:.0f} GB free on {probe.anchor or probe}",
        need=f"{need:.0f} GB",
        why="Model weights and their environments.",
        fix=Fix(label="Free up space", steps=("Free up space on this drive, then check again.",)),
        models=tuple(m.id for m in models),
    )


def for_models(manifests: list[Manifest], models_dir: Path, lane: str | None = None) -> list[Check]:
    """Every check the given models need on this machine, failing ones first."""
    lane = lane or families.detect_lane()
    out: list[Check] = []
    real = [m for m in manifests if not m.in_engine_env and m.python is None]
    fams: dict[str, families.Family] = {}
    for m in real:
        try:
            fams[m.family] = families.load(m.family)
        except FileNotFoundError:
            continue
    here = []
    for m in real:
        if (c := check_runs_here(m, fams.get(m.family), lane)) is not None:
            out.append(c)
        else:
            here.append(m)
    if not here:
        return out
    backends = {m.id: fams[m.family].torch_backend.get(lane, "") for m in here if m.family in fams}
    cuda = {k: v for k, v in backends.items() if v.startswith("cu")}
    if lane == "cuda":
        on_cuda = [m for m in here if m.id in cuda] or here
        out.append(check_driver(on_cuda, cuda or {"": "cu124"}))
    builders = [m for m in here if m.id in cuda and fams[m.family].builds()]
    if builders:
        want = max(toolchain.torch_cuda(cuda[m.id]) for m in builders)
        out.append(check_cuda(builders, want))
        out.append(check_cpp(builders, want))
    out.append(check_uv(here))
    missing = [m for m in here if not m.installed]
    if missing:
        out.append(check_disk(missing, models_dir))
    out.sort(key=lambda c: c.ok)
    return out
