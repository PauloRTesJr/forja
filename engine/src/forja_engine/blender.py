"""The user's Blender, for the built-in stages it backs (mesh.remesh, mesh.uv.smart).

Blender runs as a separate process, in the background with factory settings so add-ons and the
startup file cannot change results, and is never linked or imported: its GPL does not reach
Forja. It is found at FORJA_BLENDER (the core passes its `blender.path` setting to the engine it
starts), else on PATH, else where Blender's installers put it: the places the core looks for
exports (core/internal/export/find_*.go). An engine without Blender does not offer these stages,
so routing never sends them there.
"""

from __future__ import annotations

import glob
import json
import os
import re
import subprocess
import sys
import threading
from dataclasses import dataclass
from pathlib import Path
from typing import Any

ENV = "FORJA_BLENDER"
MIN_VERSION = (4, 2)
SCRIPT = Path(__file__).with_name("blender_script.py")
_VERSION = re.compile(r"Blender (\d+)\.(\d+)(?:\.(\d+))?")
_NO_WINDOW = 0x08000000 if sys.platform == "win32" else 0  # CREATE_NO_WINDOW


@dataclass(frozen=True)
class Blender:
    path: str
    version: str


class BlenderError(RuntimeError):
    pass


_lock = threading.Lock()
_probed: dict[str, tuple[int, float, str | None, str | None]] = {}  # path -> (size, mtime, version, error)


def _on_path(name: str) -> str | None:
    """The first `name` in an absolute PATH folder. Unlike shutil.which on Windows, never the
    current folder: a blender.exe inside an unzipped asset pack must not run."""
    for folder in os.environ.get("PATH", "").split(os.pathsep):
        if not folder or not os.path.isabs(folder):
            continue
        exe = Path(folder) / name
        if exe.is_file() and os.access(exe, os.X_OK):
            return str(exe)
    return None


def candidates() -> list[str]:
    out = []
    # The program itself, not a .cmd or .bat wrapper of the same name.
    on_path = _on_path("blender.exe" if sys.platform == "win32" else "blender")
    if on_path:
        out.append(on_path)
    if sys.platform == "win32":
        out += _windows()
    elif sys.platform == "darwin":
        out += [
            "/Applications/Blender.app/Contents/MacOS/Blender",
            str(Path.home() / "Applications/Blender.app/Contents/MacOS/Blender"),
        ]
    else:
        out += [
            "/snap/bin/blender",
            "/var/lib/flatpak/exports/bin/org.blender.Blender",
            str(Path.home() / ".local/share/flatpak/exports/bin/org.blender.Blender"),
        ]
    return out


def _windows() -> list[str]:
    out: list[str] = []
    # Also tells mypy, checking on another OS, that the registry calls below are Windows-only.
    if sys.platform != "win32":
        return out
    try:
        import winreg

        with winreg.OpenKey(winreg.HKEY_CLASSES_ROOT, r"blendfile\shell\open\command") as key:
            command = str(winreg.QueryValue(key, None)).strip()
        exe = command[1:].split('"', 1)[0] if command.startswith('"') else command.split(" ", 1)[0]
        if exe:
            # It names blender-launcher.exe, which leaves before Blender finishes.
            out.append(str(Path(exe).with_name("blender.exe")))
    except OSError:
        pass
    for env in ("ProgramW6432", "ProgramFiles"):
        root = os.environ.get(env)
        if root:
            found = glob.glob(os.path.join(root, "Blender Foundation", "Blender*", "blender.exe"))
            out += sorted(found, key=_folder_version, reverse=True)
    steam = os.environ.get("PROGRAMFILES(X86)")
    if steam:
        out.append(os.path.join(steam, "Steam", "steamapps", "common", "Blender", "blender.exe"))
    return out


def _folder_version(path: str) -> tuple[int, int]:
    m = re.search(r"(\d+)\.(\d+)", Path(path).parent.name)
    return (int(m.group(1)), int(m.group(2))) if m else (0, 0)


def probe(path: str) -> Blender:
    """The Blender at path, asked for its version once per change of the file."""
    try:
        st = os.stat(path)
    except OSError:
        raise BlenderError(f"there is no Blender at {path}") from None
    with _lock:
        seen = _probed.get(path)
    if seen is None or seen[:2] != (st.st_size, st.st_mtime):
        version, error = _ask_version(path)
        seen = (st.st_size, st.st_mtime, version, error)
        with _lock:
            _probed[path] = seen
    if seen[3]:
        raise BlenderError(seen[3])
    return Blender(path, str(seen[2]))


def _ask_version(path: str) -> tuple[str | None, str | None]:
    try:
        out = subprocess.run(
            [path, "--version"], capture_output=True, text=True, timeout=60, creationflags=_NO_WINDOW
        ).stdout
    except (OSError, subprocess.TimeoutExpired) as e:
        return None, f"{path} did not answer as Blender: {e}"
    m = _VERSION.search(out or "")
    if not m:
        return None, f"{path} did not answer as Blender"
    version = ".".join(g for g in m.groups() if g)
    if (int(m.group(1)), int(m.group(2))) < MIN_VERSION:
        need = ".".join(map(str, MIN_VERSION))
        return None, f"Blender {version} at {path} is too old: these stages need {need} or newer"
    return version, None


def find() -> Blender:
    """FORJA_BLENDER when set, else the first new enough Blender on PATH or where installers put it."""
    configured = os.environ.get(ENV, "").strip()
    if configured:
        return probe(configured)
    too_old = None
    for path in candidates():
        try:
            return probe(path)
        except BlenderError as e:
            if too_old is None and "too old" in str(e):
                too_old = e
    if too_old:
        raise too_old
    raise BlenderError(
        f"Blender {MIN_VERSION[0]}.{MIN_VERSION[1]} or newer was not found on the computer that runs the "
        "engine: install it, or set its path in Settings (blender.path) or in FORJA_BLENDER"
    )


def problem() -> str | None:
    """Why the Blender stages cannot run here, or None when they can."""
    try:
        find()
    except BlenderError as e:
        return str(e)
    return None


def run(job: dict[str, Any], workdir: Path, timeout: float) -> dict[str, Any]:
    """Runs blender_script.py on a job in the background; returns the result it prints."""
    blender = find()
    job_file = workdir / "job.json"
    job_file.write_text(json.dumps(job), encoding="utf-8")
    cmd = [
        blender.path,
        "--background",
        "--factory-startup",
        "--python-exit-code",
        "1",
        "--python",
        str(SCRIPT),
        "--",
        str(job_file),
    ]
    try:
        proc = subprocess.run(
            cmd, capture_output=True, text=True, timeout=timeout, creationflags=_NO_WINDOW, errors="replace"
        )
    except subprocess.TimeoutExpired:
        raise BlenderError(f"Blender {blender.version} did not finish within {timeout:.0f} s") from None
    result: dict[str, Any] | None = None
    for line in proc.stdout.splitlines():
        if line.startswith("FORJA "):
            result = json.loads(line[6:])
    if result is None:
        tail = " / ".join(x.strip() for x in (proc.stdout + proc.stderr).strip().splitlines()[-6:])
        raise BlenderError(
            f"Blender {blender.version} stopped without a result (exit {proc.returncode}): {tail}"
        )
    if result.get("error"):
        raise BlenderError(f"Blender {blender.version}: {result['error']}")
    return result
