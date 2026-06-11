#!/usr/bin/env python3
"""Verify sandbox memory prefault (MAP_POPULATE) via the SDK.

Usage:
    export CUBE_API_KEY=xxx CUBE_TEMPLATE_ID=xxx
    python3 test_prealloc.py

Must run on the ECS host where sandboxes are scheduled.

Creates two sandboxes — one with prefault=True, one with prefault=False —
and compares host-side RSS of the VMM processes. With prefault, RSS should
reach near-max immediately at creation; without it, RSS stays low until
pages are accessed.
"""

import os
import subprocess
import sys
import time
import threading
from cubesandbox import Sandbox

SAMPLE_INTERVAL = 1.0
SAMPLE_COUNT = 15


def fail(msg: str):
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(1)


def get_sandbox_pids(sandbox_id: str) -> list[str]:
    try:
        result = subprocess.run(
            ["pgrep", "-f", sandbox_id],
            capture_output=True, text=True, timeout=5,
        )
        return [p for p in result.stdout.strip().split("\n") if p]
    except Exception:
        return []


def get_rss_kb(pids: list[str]) -> int | None:
    total = 0
    for pid in pids:
        try:
            with open(f"/proc/{pid}/status") as f:
                for line in f:
                    if line.startswith("VmRSS:"):
                        total += int(line.split()[1])
                        break
        except (FileNotFoundError, PermissionError):
            continue
    return total if total > 0 else None


def sample_rss(sandbox_id: str, duration_s: float, label: str) -> list[dict]:
    """Sample VMM RSS every SAMPLE_INTERVAL for duration_s seconds."""
    samples = []
    start = time.time()
    while time.time() - start < duration_s:
        pids = get_sandbox_pids(sandbox_id)
        rss_kb = get_rss_kb(pids)
        samples.append({
            "t": round(time.time() - start, 1),
            "rss_mb": round(rss_kb / 1024, 1) if rss_kb else None,
        })
        time.sleep(SAMPLE_INTERVAL)
    return samples


def print_samples(label: str, samples: list[dict]):
    print(f"\n  {label}:")
    print(f"  {'t':>6s}  {'RSS':>10s}")
    print(f"  {'---':>6s}  {'---':>10s}")
    for s in samples:
        rss = f"{s['rss_mb']:.1f} MB" if s['rss_mb'] else "N/A"
        print(f"  {s['t']:5.1f}s  {rss:>10s}")


def summarize(label: str, samples: list[dict]) -> dict:
    vals = [s["rss_mb"] for s in samples if s["rss_mb"] is not None]
    if not vals:
        return {"label": label, "count": 0}
    return {
        "label": label,
        "samples": len(vals),
        "min": min(vals),
        "max": max(vals),
        "mean": sum(vals) / len(vals),
    }


def case_prefault(sandbox: Sandbox) -> list[dict]:
    """Sample RSS of a sandbox created with given prefault setting."""
    sandbox_id = sandbox.sandbox_id
    # Keep sandbox alive with a sleep task
    def keep_alive():
        try:
            sandbox.run_code("import time; time.sleep(60)")
        except Exception:
            pass
    threading.Thread(target=keep_alive, daemon=True).start()

    time.sleep(2)  # wait for sandbox to fully start
    return sample_rss(sandbox_id, SAMPLE_COUNT, "")


def main():
    required = ["CUBE_TEMPLATE_ID"]
    missing = [v for v in required if v not in os.environ]
    if missing:
        fail(f"Missing env vars: {', '.join(missing)}")

    template = os.environ["CUBE_TEMPLATE_ID"]

    print("=" * 60)
    print("  CubeSandbox Prefault Verification")
    print("=" * 60)
    print()

    # ── Case 1: prefault=False (default, lazy-load) ──
    print("Case 1: Sandbox WITHOUT prefault (lazy-load default)")
    print("-" * 60)
    sb_lazy = Sandbox.create(template=template)
    print(f"  sandbox: {sb_lazy.sandbox_id}")
    samples_lazy = None
    try:
        samples_lazy = case_prefault(sb_lazy)
        print_samples("Lazy-load RSS", samples_lazy)
    finally:
        sb_lazy.kill()

    # ── Case 2: prefault=True ──
    print(f"\nCase 2: Sandbox WITH prefault=True")
    print("-" * 60)
    sb_prefault = Sandbox.create(template=template, prefault=True)
    print(f"  sandbox: {sb_prefault.sandbox_id}")
    samples_prefault = None
    try:
        samples_prefault = case_prefault(sb_prefault)
        print_samples("Prefault RSS", samples_prefault)
    finally:
        sb_prefault.kill()

    # ── Summary ──
    print(f"\n{'=' * 60}")
    print("SUMMARY")
    print(f"{'=' * 60}")

    sum_lazy = summarize("Lazy-load", samples_lazy or [])
    sum_prefault = summarize("Prefault", samples_prefault or [])

    for s in [sum_lazy, sum_prefault]:
        if s["samples"] == 0:
            print(f"  {s['label']}: no samples collected")
        else:
            print(f"  {s['label']}:")
            print(f"    RSS  range: {s['min']:.1f} .. {s['max']:.1f} MB")
            print(f"    RSS  mean:  {s['mean']:.1f} MB")

    if sum_lazy["samples"] > 0 and sum_prefault["samples"] > 0:
        ratio = sum_prefault["mean"] / sum_lazy["mean"]
        print(f"\n  Prefault / Lazy ratio: {ratio:.1f}x")
        if ratio > 2:
            print("  >>> Prefault is working: RSS significantly higher upfront")
        elif ratio > 1.2:
            print("  >> Prefault may be working (moderate difference)")
        else:
            print("  >> RSS difference is small — prefault may not be active")


if __name__ == "__main__":
    main()
