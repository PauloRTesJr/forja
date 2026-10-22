#!/usr/bin/env python3
"""Summarize M0 runs: per-stage time and peak VRAM, per-image totals, against the budgets.

usage: python3 report.py results/run-*.json [--csv results/stages.csv]

Each input is the JSON printed by `forja generate --json`. Standard library only.
"""

from __future__ import annotations

import argparse
import csv
import json
import statistics
from pathlib import Path

GB = 1 << 30
BUDGET_SECONDS = 90  # Mobile prop, image to textured GLB (spec section 10, decision 10)


def pct(xs: list[float], q: float) -> float:
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(q * (len(xs) - 1))))]


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("runs", nargs="+", type=Path)
    ap.add_argument("--csv", type=Path, help="also write one row per job and stage")
    args = ap.parse_args()

    jobs = [j for path in args.runs for j in json.loads(path.read_text())]
    stages: dict[str, dict[str, list[float]]] = {}
    order: list[str] = []
    totals: list[tuple[str, str, float]] = []
    rows = []
    for job in jobs:
        ran = 0.0
        for s in job["stages"]:
            if s["id"] not in stages:
                stages[s["id"]] = {"seconds": [], "vram": [], "cached": []}
                order.append(s["id"])
            agg = stages[s["id"]]
            agg["cached"].append(1.0 if s.get("cached") else 0.0)
            if s["status"] == "succeeded" and not s.get("cached"):
                agg["seconds"].append(s["seconds"])
                ran += s["seconds"]
            if s.get("peak_vram_bytes"):
                agg["vram"].append(s["peak_vram_bytes"] / GB)
            rows.append([job["name"], job["id"], s["id"], s.get("model", ""), s["status"], s.get("cached", False),
                         round(s["seconds"], 3), round(s.get("peak_vram_bytes", 0) / GB, 2)])
        totals.append((job["name"], job["status"], ran))

    print(f"# M0 report: {len(jobs)} jobs\n")
    print("| Stage | Runs | Median s | p90 s | Max s | Peak VRAM GB | Cached |")
    print("|---|---:|---:|---:|---:|---:|---:|")
    for sid in order:
        a = stages[sid]
        sec = a["seconds"]
        vram = f"{max(a['vram']):.1f}" if a["vram"] else "—"
        if sec:
            line = f"| {sid} | {len(sec)} | {statistics.median(sec):.2f} | {pct(sec, 0.9):.2f} | {max(sec):.2f}"
        else:
            line = f"| {sid} | 0 | — | — | —"
        print(f"{line} | {vram} | {int(sum(a['cached']))} |")

    done = [t for t in totals if t[1] == "succeeded"]
    print(f"\n## Image to GLB (budget {BUDGET_SECONDS} s)\n")
    if done:
        secs = [t[2] for t in done]
        over = [t for t in done if t[2] > BUDGET_SECONDS]
        print(f"- median {statistics.median(secs):.1f} s, p90 {pct(secs, 0.9):.1f} s, max {max(secs):.1f} s")
        print(f"- over budget: {len(over)} of {len(done)}" + (f" ({', '.join(t[0] for t in over)})" if over else ""))
    failed = [t for t in totals if t[1] != "succeeded"]
    if failed:
        print(f"- not succeeded: {', '.join(f'{n} ({s})' for n, s, _ in failed)}")
    print("\nTimes exclude cached stages; the first job also pays model loading.")

    if args.csv:
        with args.csv.open("w", newline="") as f:
            w = csv.writer(f)
            w.writerow(["job", "job_id", "stage", "model", "status", "cached", "seconds", "peak_vram_gb"])
            w.writerows(rows)


if __name__ == "__main__":
    main()
