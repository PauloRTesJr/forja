"""Content-addressed artifact store.

Layout is shared with the Go core (`core/internal/store`): `<root>/objects/<2 hex>/<62 hex>`.
When the core and the engine run on one machine they point at the same root, so artifacts
move between them by path, with zero copies. A file's modification time is when it was last
stored or asked for: the core's sweeps (core/internal/sweep) leave recent files alone.
"""

from __future__ import annotations

import contextlib
import os
import shutil
import tempfile
from pathlib import Path

import blake3

PREFIX = "blake3:"
_CHUNK = 1 << 20


def hash_file(path: Path) -> str:
    hasher = blake3.blake3(max_threads=blake3.blake3.AUTO)
    hasher.update_mmap(str(path))
    return PREFIX + hasher.hexdigest()


def _hex(ref: str) -> str:
    if not ref.startswith(PREFIX) or len(ref) != len(PREFIX) + 64:
        raise ValueError(f"not a blake3 ref: {ref!r}")
    digest = ref[len(PREFIX) :]
    int(digest, 16)
    return digest


class ArtifactStore:
    def __init__(self, root: Path) -> None:
        self.root = root
        self.objects = root / "objects"
        self.tmp = root / "tmp"
        self.objects.mkdir(parents=True, exist_ok=True)
        self.tmp.mkdir(parents=True, exist_ok=True)

    def path(self, ref: str) -> Path:
        digest = _hex(ref)
        return self.objects / digest[:2] / digest[2:]

    def has(self, ref: str) -> bool:
        try:
            return self.path(ref).is_file()
        except ValueError:
            return False

    def touch(self, ref: str) -> None:
        """Marks an artifact as used now; one that is gone stays gone."""
        with contextlib.suppress(OSError, ValueError):
            os.utime(self.path(ref))

    def scratch_dir(self) -> Path:
        # Scratch lives under the store root so `put_file` is a same-volume rename.
        return Path(tempfile.mkdtemp(dir=self.tmp))

    def put_file(self, src: Path) -> str:
        """Move `src` into the store and return its ref. Idempotent for identical content."""
        ref = hash_file(src)
        dst = self.path(ref)
        if dst.exists():
            src.unlink(missing_ok=True)
            self.touch(ref)
            return ref
        dst.parent.mkdir(parents=True, exist_ok=True)
        os.replace(src, dst)
        return ref

    def new_upload(self) -> Path:
        fd, name = tempfile.mkstemp(dir=self.tmp)
        os.close(fd)
        return Path(name)

    def put_verified(self, ref: str, tmp: Path) -> str:
        """Move an uploaded file into the store, refusing it unless it hashes to `ref`."""
        actual = hash_file(tmp)
        if actual != ref:
            tmp.unlink(missing_ok=True)
            raise ValueError(f"hash mismatch: expected {ref}, got {actual}")
        return self.put_file(tmp)

    def copy_in(self, src: Path) -> str:
        tmp = self.new_upload()
        shutil.copyfile(src, tmp)
        return self.put_file(tmp)
