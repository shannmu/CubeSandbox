#!/usr/bin/env python3
"""Measure host memory and CPU usage of a sandbox under idle and memory-intensive workloads.

Usage:
    export CUBE_API_KEY=xxx CUBE_TEMPLATE_ID=xxx
    python3 measure_memory.py

Must run on the ECS host where the sandbox is scheduled.
"""

import os
import subprocess
import time
import threading
from e2b_code_interpreter import Sandbox

SAMPLE_INTERVAL = 1
IDLE_DURATION = 15
BUSY_DURATION = 20


def get_sandbox_pids(sandbox_id: str) -> list[str]:
    """Find PIDs of VMM processes for this sandbox."""
    try:
        result = subprocess.run(
            ["pgrep", "-f", sandbox_id],
            capture_output=True, text=True, timeout=5
        )
        return [p for p in result.stdout.strip().split("\n") if p]
    except Exception:
        return []


def get_rss_kb(pids: list[str]) -> int | None:
    """Sum VmRSS from /proc/<pid>/status for given PIDs."""
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


def get_cpu_ticks(pids: list[str]) -> int | None:
    """Sum (utime + stime) from /proc/<pid>/stat for given PIDs (in clock ticks)."""
    total = 0
    for pid in pids:
        try:
            with open(f"/proc/{pid}/stat") as f:
                fields = f.read().split(")")[-1].split()
                # fields[11] = utime, fields[12] = stime (0-indexed after ')')
                utime = int(fields[11])
                stime = int(fields[12])
                total += utime + stime
        except (FileNotFoundError, PermissionError, (IndexError, ValueError)):
            continue
    return total if total > 0 else None


def sample_resources(sandbox_id: str, duration: float) -> list[dict]:
    """Sample memory (RSS) and CPU usage every second for `duration` seconds."""
    clk_tck = os.sysconf("SC_CLK_TCK")
    samples = []
    start = time.time()

    pids = get_sandbox_pids(sandbox_id)
    prev_ticks = get_cpu_ticks(pids)
    prev_time = time.time()

    time.sleep(SAMPLE_INTERVAL)

    while time.time() - start < duration:
        pids = get_sandbox_pids(sandbox_id)
        rss_kb = get_rss_kb(pids)
        cur_ticks = get_cpu_ticks(pids)
        cur_time = time.time()

        cpu_pct = None
        if prev_ticks is not None and cur_ticks is not None:
            dt = cur_time - prev_time
            if dt > 0:
                cpu_pct = round((cur_ticks - prev_ticks) / clk_tck / dt * 100, 1)

        elapsed = round(cur_time - start, 1)
        samples.append({
            "t": elapsed,
            "rss_kb": rss_kb,
            "rss_mb": round(rss_kb / 1024, 1) if rss_kb else None,
            "cpu_pct": cpu_pct,
        })

        prev_ticks = cur_ticks
        prev_time = cur_time
        time.sleep(SAMPLE_INTERVAL)

    return samples


def run_code_background(sandbox, code):
    """Run code in sandbox in a background thread (suppresses kill-related errors)."""
    def _run():
        try:
            sandbox.run_code(code)
        except Exception:
            pass

    t = threading.Thread(target=_run, daemon=True)
    t.start()
    return t


def print_samples(samples):
    print(f"  {'t':>6s}  {'RSS':>8s}  {'CPU':>7s}")
    print(f"  {'---':>6s}  {'---':>8s}  {'---':>7s}")
    for s in samples:
        rss_str = f"{s['rss_mb']} MB" if s['rss_mb'] is not None else "N/A"
        cpu_str = f"{s['cpu_pct']}%" if s['cpu_pct'] is not None else "N/A"
        print(f"  {s['t']:5.1f}s  {rss_str:>8s}  {cpu_str:>7s}")


def case_idle() -> list[dict]:
    """Case 1: Sandbox running a lightweight task (sleep), measure baseline."""
    print(f"\n{'='*60}")
    print("Case 1: IDLE sandbox (sleep, no memory/CPU demand)")
    print(f"{'='*60}")

    template = os.environ["CUBE_TEMPLATE_ID"]
    sandbox = Sandbox.create(template=template)
    print(f"  Sandbox: {sandbox.sandbox_id}")

    try:
        run_code_background(sandbox, "import time; time.sleep(60)")
        time.sleep(2)

        samples = sample_resources(sandbox.sandbox_id, IDLE_DURATION)
        print_samples(samples)
        return samples
    finally:
        sandbox.kill()


def case_memory() -> list[dict]:
    """Case 2: Sandbox allocates and touches 512MB gradually."""
    print(f"\n{'='*60}")
    print("Case 2: MEMORY-INTENSIVE sandbox (allocate+touch 512MB)")
    print(f"{'='*60}")

    template = os.environ["CUBE_TEMPLATE_ID"]
    sandbox = Sandbox.create(template=template)
    print(f"  Sandbox: {sandbox.sandbox_id}")

    try:
        mem_code = """
import time

chunks = []
for i in range(8):
    chunk = bytearray(64 * 1024 * 1024)  # 64MB per chunk
    for offset in range(0, len(chunk), 4096):
        chunk[offset] = 0x42  # touch every page
    chunks.append(chunk)
    time.sleep(1)

time.sleep(30)
"""
        run_code_background(sandbox, mem_code)

        samples = sample_resources(sandbox.sandbox_id, BUSY_DURATION)
        print_samples(samples)
        return samples
    finally:
        sandbox.kill()


def print_summary(label: str, samples: list[dict]):
    rss_vals = [s["rss_mb"] for s in samples if s["rss_mb"] is not None]
    cpu_vals = [s["cpu_pct"] for s in samples if s["cpu_pct"] is not None]

    if rss_vals:
        print(f"  {label} RSS:  min={min(rss_vals):.1f} MB  max={max(rss_vals):.1f} MB  avg={sum(rss_vals)/len(rss_vals):.1f} MB")
    else:
        print(f"  {label} RSS:  no samples")

    if cpu_vals:
        print(f"  {label} CPU:  min={min(cpu_vals):.1f}%  max={max(cpu_vals):.1f}%  avg={sum(cpu_vals)/len(cpu_vals):.1f}%")
    else:
        print(f"  {label} CPU:  no samples")


def main():
    idle_samples = case_idle()
    memory_samples = case_memory()

    print(f"\n{'='*60}")
    print("SUMMARY")
    print(f"{'='*60}")

    print_summary("Idle", idle_samples)
    print_summary("Memory", memory_samples)

    rss_idle = [s["rss_mb"] for s in idle_samples if s["rss_mb"] is not None]
    rss_mem = [s["rss_mb"] for s in memory_samples if s["rss_mb"] is not None]
    if rss_idle and rss_mem:
        print(f"  RSS Growth: +{max(rss_mem) - min(rss_idle):.1f} MB (peak memory - idle baseline)")


if __name__ == "__main__":
    main()
