"""`forja-sdk test`: run the conformance suite on one adapter from a shell."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

from forja_sdk import artifacts, conformance
from forja_sdk.types import VIEWS, StageInput


def _value(raw: str) -> Any:
    try:
        return json.loads(raw)
    except ValueError:
        return raw


def _inputs(specs: list[str]) -> list[StageInput]:
    out = []
    for spec in specs:
        path, _, view = spec.partition("=")
        if view and view not in VIEWS:
            raise SystemExit(f"--input {spec}: view must be one of {', '.join(VIEWS)}")
        p = Path(path)
        if not p.is_file():
            raise SystemExit(f"--input {spec}: no such file")
        out.append(StageInput(p, artifacts.kind_for(p), "", view or None))
    return out


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(prog="forja-sdk", description="Forja adapter SDK tools")
    sub = parser.add_subparsers(dest="cmd", required=True)
    test = sub.add_parser("test", help="run the conformance suite on an adapter")
    test.add_argument("target", help="module:Class, or a model manifest (.yaml) that names its adapter")
    test.add_argument("--task", help="the stage type to test (default: every one the adapter serves)")
    test.add_argument("--device", help="cpu, cuda, mps or rocm (default: cpu when the adapter runs there)")
    test.add_argument("--dtype", default="fp16")
    test.add_argument(
        "--input", action="append", default=[], metavar="PATH[=VIEW]", help="an input file instead of samples"
    )
    test.add_argument("--param", action="append", default=[], metavar="KEY=VALUE", help="a stage parameter")
    test.add_argument("--seed", type=int, default=42)
    test.add_argument("--weights", type=Path, help="the folder holding the model's weights")
    test.add_argument("--offload", action="store_true", help="load as the engine does when VRAM is short")
    test.add_argument(
        "--path", action="append", default=[], type=Path, help="add a folder to the import path"
    )
    test.add_argument(
        "--skip", action="append", default=[], help="skip a check: determinism, cancellation, ..."
    )
    test.add_argument("--vram-tolerance", type=float, default=0.25)
    test.add_argument("--keep", type=Path, help="write the runs' outputs into this folder and keep them")
    test.add_argument("--json", action="store_true", help="print the report as JSON")
    args = parser.parse_args(argv)

    params = {}
    for p in args.param:
        key, sep, raw = p.partition("=")
        if not sep:
            raise SystemExit(f"--param {p}: want KEY=VALUE")
        params[key] = _value(raw)
    if args.input and not args.task:
        raise SystemExit("--input needs --task: inputs belong to one stage type")

    target: str = args.target
    manifest = None
    if target.endswith((".yaml", ".yml")):
        manifest_path = Path(target)
        manifest = conformance.load_manifest(manifest_path)
        target = str(manifest["adapter"])
        # A model package keeps its adapter in src/ next to family.yaml.
        package = manifest_path.resolve().parent.parent
        if (package / "family.yaml").is_file() and (package / "src").is_dir():
            sys.path.insert(0, str(package / "src"))
    for extra in reversed(args.path):
        sys.path.insert(0, str(extra.resolve()))

    report = conformance.run_suite(
        target,
        manifest=manifest,
        task=args.task,
        device=args.device,
        inputs=_inputs(args.input) if args.input else None,
        params=params,
        seed=args.seed,
        dtype=args.dtype,
        weights_dir=args.weights,
        offload=args.offload,
        work_dir=args.keep,
        skip=args.skip,
        vram_tolerance=args.vram_tolerance,
    )
    print(json.dumps(report.to_json(), indent=2) if args.json else report.format())
    sys.exit(0 if report.ok else 1)


if __name__ == "__main__":
    main()
