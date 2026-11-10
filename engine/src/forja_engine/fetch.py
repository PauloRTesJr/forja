"""Downloads a model's pinned weights in a child process of the installer, printing progress.

Run as `python -m forja_engine.fetch '<json spec>'`. A separate process lets a cancel stop a
multi-GB file mid-way. Progress lines are `FORJA_PROGRESS <bytes done> <bytes total>`, with a
total of 0 when the hub would not say.
"""

from __future__ import annotations

import json
import sys
import threading
from pathlib import Path


def folder_bytes(root: Path) -> int:
    """Bytes under root, including the hub's partial downloads in .cache."""
    total = 0
    if root.is_dir():
        for p in root.rglob("*"):
            try:
                if p.is_file():
                    total += p.stat().st_size
            except OSError:
                continue
    return total


def access_problem(e: BaseException, repo: str) -> str | None:
    """What to do when the hub refuses the download for lack of access, or None for other errors.

    A gated repository answers 401 without a token and 403 with one that has not accepted its
    terms; the hub's own message is a page of text that names neither step.
    """
    status = getattr(getattr(e, "response", None), "status_code", None)
    if type(e).__name__ != "GatedRepoError" and status not in (401, 403):
        return None
    return (
        f"the weights at https://huggingface.co/{repo} need a Hugging Face account that accepted their "
        "terms: accept them on that page, set HF_TOKEN to an access token of that account in the "
        "environment Forja starts from, restart Forja and install again"
    )


def main() -> None:
    spec = json.loads(sys.argv[1])
    from huggingface_hub import HfApi, snapshot_download
    from huggingface_hub.utils import filter_repo_objects

    dest = Path(spec["dest"])
    total = 0
    try:
        info = HfApi().model_info(spec["repo"], revision=spec["revision"], files_metadata=True)
        wanted = filter_repo_objects(
            info.siblings or [],
            allow_patterns=spec.get("allow"),
            ignore_patterns=spec.get("ignore"),
            key=lambda s: s.rfilename,
        )
        total = sum(s.size or 0 for s in wanted)
    except Exception as e:  # sizes only drive the progress bar; the download decides success
        print(f"could not read file sizes from the hub: {e}", flush=True)

    done = threading.Event()

    def report() -> None:
        while not done.wait(1.0):
            print(f"FORJA_PROGRESS {min(folder_bytes(dest), total or 1 << 62)} {total}", flush=True)

    threading.Thread(target=report, daemon=True).start()
    try:
        snapshot_download(
            repo_id=spec["repo"],
            revision=spec["revision"],
            allow_patterns=spec.get("allow"),
            ignore_patterns=spec.get("ignore"),
            local_dir=dest,
        )
    except Exception as e:
        done.set()
        problem = access_problem(e, spec["repo"])
        if problem is None:
            raise
        # The installer reports a failed command by its last line of output.
        print(problem, flush=True)
        sys.exit(1)
    done.set()
    print(f"FORJA_PROGRESS {total or folder_bytes(dest)} {total}", flush=True)


if __name__ == "__main__":
    main()
