"""The compilers a family's CUDA extensions need, found and checked before building.

Without nvcc, extensions such as diso quietly build CPU-only and still import, so the
failure would surface only when a stage runs. On Windows, setuptools picks the newest
Visual Studio by itself, which nvcc may reject; the build gets an explicit MSVC environment
instead.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path

Log = Callable[[str], None]

_INSTALL_CUDA = (
    "install the CUDA toolkit {want} (https://developer.nvidia.com/cuda-toolkit-archive), "
    "or point CUDA_HOME at it"
)


class ToolchainError(RuntimeError):
    pass


def torch_cuda(backend: str) -> tuple[int, int]:
    """'cu124' -> (12, 4): the CUDA version the family's torch wheels are built with."""
    m = re.fullmatch(r"cu(\d+)(\d)", backend)
    if not m:
        raise ValueError(f"not a CUDA torch backend: {backend!r}")
    return int(m.group(1)), int(m.group(2))


def cuda_home() -> Path | None:
    for var in ("CUDA_HOME", "CUDA_PATH"):
        if (d := os.environ.get(var)) and Path(d).is_dir():
            return Path(d)
    if nvcc := shutil.which("nvcc"):
        return Path(nvcc).resolve().parents[1]
    if sys.platform.startswith("linux") and Path("/usr/local/cuda").is_dir():
        return Path("/usr/local/cuda")
    return None


def nvcc_version(home: Path) -> tuple[int, int]:
    nvcc = home / "bin" / ("nvcc.exe" if sys.platform == "win32" else "nvcc")
    if not nvcc.is_file():
        raise ToolchainError(f"no nvcc in {home}")
    out = subprocess.run([str(nvcc), "--version"], capture_output=True, text=True, check=True).stdout
    m = re.search(r"release (\d+)\.(\d+)", out)
    if not m:
        raise ToolchainError(f"cannot read the version from {nvcc} --version")
    return int(m.group(1)), int(m.group(2))


def check_cuda(want: tuple[int, int]) -> tuple[Path, tuple[int, int]]:
    """Find nvcc whose major version matches torch's and whose minor is at least torch's."""
    label = f"{want[0]}.{want[1]}+"
    home = cuda_home()
    if home is None:
        raise ToolchainError("nvcc not found: " + _INSTALL_CUDA.format(want=label))
    have = nvcc_version(home)
    if have[0] != want[0] or have[1] < want[1]:
        raise ToolchainError(
            f"nvcc {have[0]}.{have[1]} at {home} cannot build for torch built with CUDA "
            f"{want[0]}.{want[1]}: " + _INSTALL_CUDA.format(want=label)
        )
    return home, have


def max_msvc_toolset(cuda: tuple[int, int]) -> tuple[int, int]:
    """Newest MSVC toolset (major, minor) this nvcc accepts.

    CUDA 12 supports Visual Studio 2017 to 2022, that is _MSC_VER below 1950 (toolset 14.4x);
    Visual Studio 2026 ships 14.5x.
    """
    if cuda[0] <= 12:
        return (14, 49)
    raise ToolchainError(f"no known MSVC toolset limit for CUDA {cuda[0]}.{cuda[1]}; add it here")


def _vs_installs() -> list[Path]:
    vswhere = Path(os.environ.get("PROGRAMFILES(X86)", r"C:\Program Files (x86)")) / (
        r"Microsoft Visual Studio\Installer\vswhere.exe"
    )
    if not vswhere.is_file():
        return []
    out = subprocess.run(
        [str(vswhere), "-all", "-products", "*", "-format", "json", "-utf8"],
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=True,
    ).stdout
    return [Path(i["installationPath"]) for i in json.loads(out or "[]")]


def find_msvc(limit: tuple[int, int], installs: list[Path] | None = None) -> tuple[Path, str]:
    """(vcvarsall.bat, toolset version) for the newest toolset at or below limit."""
    best: tuple[tuple[int, int, int], Path, str] | None = None
    for root in _vs_installs() if installs is None else installs:
        bat = root / "VC" / "Auxiliary" / "Build" / "vcvarsall.bat"
        tools = root / "VC" / "Tools" / "MSVC"
        if not bat.is_file() or not tools.is_dir():
            continue
        for d in tools.iterdir():
            m = re.fullmatch(r"(\d+)\.(\d+)\.(\d+)", d.name)
            if not m or not (d / "bin" / "Hostx64" / "x64" / "cl.exe").is_file():
                continue
            ver = (int(m.group(1)), int(m.group(2)), int(m.group(3)))
            if ver[:2] <= limit and (best is None or ver > best[0]):
                best = (ver, bat, d.name)
    if best is None:
        raise ToolchainError(
            f"no MSVC toolset at or below {limit[0]}.{limit[1]} for nvcc: in the Visual Studio "
            "Installer add 'MSVC v143 - VS 2022 C++ x64/x86 build tools' (component "
            "Microsoft.VisualStudio.Component.VC.14.44.17.14.x86.x64)"
        )
    return best[1], best[2]


def vcvars(bat: Path, toolset: str) -> dict[str, str]:
    """The environment vcvarsall.bat sets up for one toolset."""
    out = subprocess.run(
        f'"{bat}" x64 -vcvars_ver={toolset} >nul && set',
        shell=True,
        capture_output=True,
        text=True,
        encoding="oem",
        errors="replace",
    )
    lines = [line for line in out.stdout.splitlines() if "=" in line and not line.startswith("=")]
    env = dict(line.split("=", 1) for line in lines)
    if out.returncode != 0 or "VCToolsVersion" not in env:
        detail = out.stderr.strip() or out.stdout[-400:]
        raise ToolchainError(f"{bat} x64 -vcvars_ver={toolset} failed: {detail}")
    if not env.get("UCRTVersion") or "ucrt" not in env.get("INCLUDE", "").lower():
        raise ToolchainError(
            "the Windows SDK is missing, so MSVC has no C runtime headers: in the Visual Studio "
            "Installer add a Windows 11 SDK (component Microsoft.VisualStudio.Component.Windows11SDK.26100)"
        )
    return env


def build_env(torch_backend: str, log: Log) -> dict[str, str]:
    """Environment for compiling CUDA extensions against a torch built for torch_backend."""
    home, have = check_cuda(torch_cuda(torch_backend))
    log(f"nvcc {have[0]}.{have[1]} at {home}")
    env = {"CUDA_HOME": str(home), "CUDA_PATH": str(home)}
    if sys.platform == "win32":
        bat, toolset = find_msvc(max_msvc_toolset(have))
        log(f"MSVC {toolset} via {bat}")
        env.update(vcvars(bat, toolset))
        # setuptools and torch then use this environment instead of searching for Visual Studio.
        env["DISTUTILS_USE_SDK"] = "1"
        env["MSSdk"] = "1"
        env["PATH"] = os.pathsep.join([str(home / "bin"), env.get("PATH", os.environ.get("PATH", ""))])
    return env
