#!/usr/bin/env python3
"""Analyze and visualize cube-pvm-bench JSON results.

Usage:
    python3 analyze.py pvm.json                        # single file analysis
    python3 analyze.py pvm.json --baseline kvm.json    # PVM vs baseline comparison
    python3 analyze.py pvm.json -o report              # save as report_*.png
"""

import argparse
import json
import math
import sys
from pathlib import Path

import matplotlib.pyplot as plt
import matplotlib.ticker as ticker
import numpy as np
from matplotlib.patches import FancyBboxPatch

SUITE_ORDER = ["cpu", "memory", "disk", "network", "syscall", "lifecycle"]

SUITE_COLORS = {
    "cpu": "#4C72B0",
    "memory": "#DD8452",
    "disk": "#55A868",
    "network": "#C44E52",
    "syscall": "#8172B3",
    "lifecycle": "#937860",
}

SUITE_LABELS = {
    "cpu": "CPU",
    "memory": "Memory",
    "disk": "Disk I/O",
    "network": "Network",
    "syscall": "Syscall",
    "lifecycle": "Lifecycle",
}


def load_result(path: str) -> dict:
    with open(path) as f:
        return json.load(f)


def get_suites(data: dict) -> list[str]:
    return [s for s in SUITE_ORDER if s in data["suites"]]


def get_workloads(data: dict, suite: str) -> list[dict]:
    return data["suites"][suite].get("workloads", [])


# ---------------------------------------------------------------------------
# Comparison logic (mirrors compare.go)
# ---------------------------------------------------------------------------

def compute_overhead(current: dict, baseline: dict) -> list[dict]:
    deltas = []
    for suite_name in get_suites(current):
        if suite_name not in baseline["suites"]:
            continue
        cur_wls = {w["name"]: w for w in get_workloads(current, suite_name)}
        base_wls = {w["name"]: w for w in get_workloads(baseline, suite_name)}
        for name, cur in cur_wls.items():
            base = base_wls.get(name)
            if not base or not cur.get("stats") or not base.get("stats"):
                continue
            cur_mean = cur["stats"]["mean"]
            base_mean = base["stats"]["mean"]
            if base_mean == 0:
                continue
            hib = cur.get("higher_is_better", False)
            if hib:
                delta_pct = (base_mean - cur_mean) / base_mean * 100
            else:
                delta_pct = (cur_mean - base_mean) / base_mean * 100
            cur_cv = cur["stats"].get("cv", 0)
            base_cv = base["stats"].get("cv", 0)
            threshold = max(cur_cv, base_cv) * 200
            if threshold == 0:
                significant = abs(delta_pct) > 1.0
            else:
                significant = abs(delta_pct) > threshold
            deltas.append({
                "suite": suite_name,
                "name": name,
                "unit": cur.get("unit", ""),
                "higher_is_better": hib,
                "cur_mean": cur_mean,
                "base_mean": base_mean,
                "delta_pct": delta_pct,
                "significant": significant,
            })
    return deltas


def overall_overhead(deltas: list[dict]) -> float:
    factors = []
    for d in deltas:
        f = 1.0 + d["delta_pct"] / 100.0
        if f > 0:
            factors.append(f)
    if not factors:
        return 0.0
    geo_mean = math.exp(sum(math.log(f) for f in factors) / len(factors))
    return (geo_mean - 1.0) * 100.0


def grade_overhead(pct: float) -> str:
    if pct < 2:
        return "S"
    if pct < 5:
        return "A"
    if pct < 15:
        return "B"
    if pct < 30:
        return "C"
    return "D"


GRADE_DESC = {"S": "Near-native", "A": "Excellent", "B": "Acceptable", "C": "Significant", "D": "Concerning"}


# ---------------------------------------------------------------------------
# Text summary
# ---------------------------------------------------------------------------

def print_summary(data: dict, baseline: dict | None = None):
    label = data.get("label", "unknown")
    env = data.get("environment", {})
    summary = data.get("summary", {})

    print(f"\n{'=' * 72}")
    print(f"  cube-pvm-bench Results: {label}")
    print(f"{'=' * 72}")
    print(f"  Timestamp:  {data.get('timestamp', 'N/A')}")
    print(f"  Hostname:   {env.get('hostname', 'N/A')}")
    print(f"  Sandbox:    {env.get('sandbox_cpu_count', '?')} vCPU / {env.get('sandbox_memory_mb', '?')} MB")
    print(f"  Duration:   {summary.get('total_duration_s', 0):.1f}s")
    print(f"  Workloads:  {summary.get('workloads_run', 0)}  |  Errors: {summary.get('errors', 0)}")
    print()

    deltas_map = {}
    if baseline:
        deltas = compute_overhead(data, baseline)
        deltas_map = {(d["suite"], d["name"]): d for d in deltas}

    for suite_name in get_suites(data):
        suite_label = SUITE_LABELS.get(suite_name, suite_name)
        print(f"  {suite_label}")
        print(f"  {'─' * 68}")

        header = f"  {'Workload':<24s} {'Mean':>10s} {'StdDev':>10s} {'CV%':>7s} {'Unit':<10s}"
        if baseline:
            header += f" {'Overhead':>9s}"
        print(header)

        for wl in get_workloads(data, suite_name):
            stats = wl.get("stats", {})
            name = wl["name"]
            mean = stats.get("mean", 0)
            stddev = stats.get("stddev", 0)
            cv = stats.get("cv", 0) * 100
            unit = wl.get("unit", "")

            line = f"  {name:<24s} {mean:>10.2f} {stddev:>10.2f} {cv:>6.1f}% {unit:<10s}"
            if baseline:
                key = (suite_name, name)
                if key in deltas_map:
                    d = deltas_map[key]
                    sign = "+" if d["delta_pct"] >= 0 else ""
                    line += f" {sign}{d['delta_pct']:>7.1f}%"
                else:
                    line += f" {'N/A':>8s}"
            print(line)
        print()

    if baseline and deltas_map:
        deltas = list(deltas_map.values())
        ovh = overall_overhead(deltas)
        g = grade_overhead(ovh)
        print(f"  Overall Overhead: {ovh:+.2f}%  Grade: {g} ({GRADE_DESC[g]})")
        print()


# ---------------------------------------------------------------------------
# Figure 1: Per-suite bar charts
# ---------------------------------------------------------------------------

def plot_suite_bars(data: dict, save_prefix: str | None = None):
    suites = get_suites(data)
    n = len(suites)
    cols = min(3, n)
    rows = math.ceil(n / cols)
    fig, axes = plt.subplots(rows, cols, figsize=(6 * cols, 4 * rows))
    if n == 1:
        axes = np.array([axes])
    axes = axes.flatten()

    for idx, suite_name in enumerate(suites):
        ax = axes[idx]
        wls = get_workloads(data, suite_name)
        names = [w["name"] for w in wls]
        means = [w.get("stats", {}).get("mean", 0) for w in wls]
        stddevs = [w.get("stats", {}).get("stddev", 0) for w in wls]
        units = list({w.get("unit", "") for w in wls})
        unit_label = units[0] if len(units) == 1 else "value"
        color = SUITE_COLORS.get(suite_name, "#666666")

        y_pos = np.arange(len(names))
        ax.barh(y_pos, means, xerr=stddevs, color=color, alpha=0.85,
                edgecolor="white", linewidth=0.5, capsize=3)
        ax.set_yticks(y_pos)
        ax.set_yticklabels(names, fontsize=9)
        ax.set_xlabel(unit_label, fontsize=9)
        ax.set_title(SUITE_LABELS.get(suite_name, suite_name), fontsize=12, fontweight="bold")
        ax.invert_yaxis()
        ax.grid(axis="x", alpha=0.3)

    for idx in range(n, len(axes)):
        axes[idx].set_visible(False)

    label = data.get("label", "")
    fig.suptitle(f"PVM Benchmark — Per-Suite Results{f' ({label})' if label else ''}", fontsize=14, fontweight="bold")
    fig.tight_layout(rect=[0, 0, 1, 0.95])

    if save_prefix:
        fig.savefig(f"{save_prefix}_suites.png", dpi=150, bbox_inches="tight")
        print(f"  Saved {save_prefix}_suites.png")
    return fig


# ---------------------------------------------------------------------------
# Figure 2: Radar chart
# ---------------------------------------------------------------------------

def _suite_score(data: dict, suite_name: str) -> float:
    wls = get_workloads(data, suite_name)
    scores = []
    for w in wls:
        stats = w.get("stats", {})
        mean = stats.get("mean", 0)
        if mean <= 0:
            continue
        scores.append(mean)
    if not scores:
        return 0
    return math.exp(sum(math.log(s) for s in scores) / len(scores))


def plot_radar(data: dict, baseline: dict | None = None, save_prefix: str | None = None):
    suites = get_suites(data)
    if len(suites) < 3:
        return None

    labels = [SUITE_LABELS.get(s, s) for s in suites]
    cur_scores = [_suite_score(data, s) for s in suites]

    max_scores = list(cur_scores)
    if baseline:
        base_scores = [_suite_score(baseline, s) if s in baseline.get("suites", {}) else 0 for s in suites]
        max_scores = [max(c, b) for c, b in zip(cur_scores, base_scores)]
    else:
        base_scores = None

    scale = [m if m > 0 else 1 for m in max_scores]
    cur_norm = [c / s for c, s in zip(cur_scores, scale)]
    if base_scores:
        base_norm = [b / s for b, s in zip(base_scores, scale)]

    N = len(suites)
    angles = np.linspace(0, 2 * np.pi, N, endpoint=False).tolist()
    angles += angles[:1]
    cur_norm += cur_norm[:1]

    fig, ax = plt.subplots(figsize=(7, 7), subplot_kw=dict(polar=True))

    cur_label = data.get("label", "current")
    ax.fill(angles, cur_norm, alpha=0.25, color="#4C72B0")
    ax.plot(angles, cur_norm, "o-", color="#4C72B0", linewidth=2, markersize=6, label=cur_label)

    if base_scores:
        base_norm += base_norm[:1]
        base_label = baseline.get("label", "baseline")
        ax.fill(angles, base_norm, alpha=0.15, color="#DD8452")
        ax.plot(angles, base_norm, "s--", color="#DD8452", linewidth=2, markersize=6, label=base_label)

    ax.set_xticks(angles[:-1])
    ax.set_xticklabels(labels, fontsize=11)
    ax.set_yticklabels([])
    ax.set_title("Performance Profile (normalized)", fontsize=13, fontweight="bold", pad=20)
    ax.legend(loc="upper right", bbox_to_anchor=(1.2, 1.1))

    fig.tight_layout()
    if save_prefix:
        fig.savefig(f"{save_prefix}_radar.png", dpi=150, bbox_inches="tight")
        print(f"  Saved {save_prefix}_radar.png")
    return fig


# ---------------------------------------------------------------------------
# Figure 3: Box plots
# ---------------------------------------------------------------------------

def plot_box_distributions(data: dict, save_prefix: str | None = None):
    suites = get_suites(data)
    n = len(suites)
    cols = min(3, n)
    rows = math.ceil(n / cols)
    fig, axes = plt.subplots(rows, cols, figsize=(6 * cols, 4 * rows))
    if n == 1:
        axes = np.array([axes])
    axes = axes.flatten()

    for idx, suite_name in enumerate(suites):
        ax = axes[idx]
        wls = get_workloads(data, suite_name)
        samples_list = [w.get("samples", []) for w in wls]
        names = [w["name"] for w in wls]
        color = SUITE_COLORS.get(suite_name, "#666666")

        bp = ax.boxplot(samples_list, vert=True, patch_artist=True,
                        labels=names, widths=0.6)
        for patch in bp["boxes"]:
            patch.set_facecolor(color)
            patch.set_alpha(0.7)
        for median in bp["medians"]:
            median.set_color("black")
            median.set_linewidth(1.5)

        ax.set_title(SUITE_LABELS.get(suite_name, suite_name), fontsize=12, fontweight="bold")
        ax.tick_params(axis="x", rotation=45, labelsize=8)
        ax.grid(axis="y", alpha=0.3)

    for idx in range(n, len(axes)):
        axes[idx].set_visible(False)

    fig.suptitle("Sample Distribution per Workload", fontsize=14, fontweight="bold")
    fig.tight_layout(rect=[0, 0, 1, 0.95])

    if save_prefix:
        fig.savefig(f"{save_prefix}_boxes.png", dpi=150, bbox_inches="tight")
        print(f"  Saved {save_prefix}_boxes.png")
    return fig


# ---------------------------------------------------------------------------
# Figure 4: CV% stability heatmap
# ---------------------------------------------------------------------------

def plot_cv_heatmap(data: dict, save_prefix: str | None = None):
    suites = get_suites(data)
    all_names = []
    cv_matrix = []

    max_wl_count = 0
    suite_wl_names = {}
    for s in suites:
        wls = get_workloads(data, s)
        names = [w["name"] for w in wls]
        suite_wl_names[s] = names
        max_wl_count = max(max_wl_count, len(names))

    all_wl_names = []
    for s in suites:
        for n in suite_wl_names[s]:
            if n not in all_wl_names:
                all_wl_names.append(n)

    matrix = np.full((len(suites), len(all_wl_names)), np.nan)
    for i, s in enumerate(suites):
        wls = get_workloads(data, s)
        for w in wls:
            if w["name"] in all_wl_names:
                j = all_wl_names.index(w["name"])
                matrix[i, j] = w.get("stats", {}).get("cv", 0) * 100

    fig, ax = plt.subplots(figsize=(max(10, len(all_wl_names) * 0.8), max(4, len(suites) * 0.8)))

    masked = np.ma.masked_invalid(matrix)
    from matplotlib.colors import LinearSegmentedColormap
    colors_list = ["#2ecc71", "#f1c40f", "#e74c3c"]
    cmap = LinearSegmentedColormap.from_list("cv_cmap", colors_list)
    cmap.set_bad(color="#f0f0f0")

    im = ax.imshow(masked, cmap=cmap, aspect="auto", vmin=0, vmax=20)

    ax.set_xticks(range(len(all_wl_names)))
    ax.set_xticklabels(all_wl_names, rotation=45, ha="right", fontsize=8)
    ax.set_yticks(range(len(suites)))
    ax.set_yticklabels([SUITE_LABELS.get(s, s) for s in suites], fontsize=10)

    for i in range(len(suites)):
        for j in range(len(all_wl_names)):
            val = matrix[i, j]
            if not np.isnan(val):
                text_color = "white" if val > 12 else "black"
                ax.text(j, i, f"{val:.1f}", ha="center", va="center",
                        fontsize=7, color=text_color, fontweight="bold")

    cbar = fig.colorbar(im, ax=ax, shrink=0.8)
    cbar.set_label("CV %", fontsize=10)

    ax.set_title("Measurement Stability (CV%): green=stable, red=noisy", fontsize=13, fontweight="bold")
    fig.tight_layout()

    if save_prefix:
        fig.savefig(f"{save_prefix}_heatmap.png", dpi=150, bbox_inches="tight")
        print(f"  Saved {save_prefix}_heatmap.png")
    return fig


# ---------------------------------------------------------------------------
# Figure 5: Overhead comparison
# ---------------------------------------------------------------------------

def plot_overhead(data: dict, baseline: dict, save_prefix: str | None = None):
    deltas = compute_overhead(data, baseline)
    if not deltas:
        print("  No matching workloads for comparison.")
        return None

    deltas.sort(key=lambda d: (SUITE_ORDER.index(d["suite"]) if d["suite"] in SUITE_ORDER else 99, d["name"]))

    names = [f"{d['suite']}/{d['name']}" for d in deltas]
    pcts = [d["delta_pct"] for d in deltas]
    colors = []
    for p in pcts:
        ap = abs(p)
        if ap < 5:
            colors.append("#2ecc71")
        elif ap < 15:
            colors.append("#f39c12")
        else:
            colors.append("#e74c3c")

    fig, ax = plt.subplots(figsize=(10, max(6, len(names) * 0.35)))

    y_pos = np.arange(len(names))
    bars = ax.barh(y_pos, pcts, color=colors, alpha=0.85, edgecolor="white", linewidth=0.5)
    ax.set_yticks(y_pos)
    ax.set_yticklabels(names, fontsize=8)
    ax.axvline(x=0, color="black", linewidth=0.8)
    ax.set_xlabel("Overhead %", fontsize=11)
    ax.invert_yaxis()
    ax.grid(axis="x", alpha=0.3)

    for i, (bar, pct) in enumerate(zip(bars, pcts)):
        sign = "+" if pct >= 0 else ""
        x_pos = pct + (1 if pct >= 0 else -1)
        ha = "left" if pct >= 0 else "right"
        ax.text(x_pos, i, f"{sign}{pct:.1f}%", va="center", ha=ha, fontsize=7, fontweight="bold")

    ovh = overall_overhead(deltas)
    g = grade_overhead(ovh)
    cur_label = data.get("label", "current")
    base_label = baseline.get("label", "baseline")
    title = f"PVM Overhead: {cur_label} vs {base_label}\n"
    title += f"Overall: {ovh:+.2f}%  |  Grade: {g} ({GRADE_DESC[g]})"
    ax.set_title(title, fontsize=13, fontweight="bold")

    ax.axvspan(-2, 2, alpha=0.08, color="green")
    ax.axvspan(2, 5, alpha=0.05, color="yellow")

    fig.tight_layout()

    if save_prefix:
        fig.savefig(f"{save_prefix}_overhead.png", dpi=150, bbox_inches="tight")
        print(f"  Saved {save_prefix}_overhead.png")
    return fig


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def print_summary_to_string(data: dict, baseline: dict | None = None) -> str:
    import io
    buf = io.StringIO()
    _print = lambda *a, **kw: print(*a, **kw, file=buf)

    label = data.get("label", "unknown")
    env = data.get("environment", {})
    summary = data.get("summary", {})

    _print(f"{'=' * 72}")
    _print(f"  cube-pvm-bench Results: {label}")
    _print(f"{'=' * 72}")
    _print(f"  Timestamp:  {data.get('timestamp', 'N/A')}")
    _print(f"  Hostname:   {env.get('hostname', 'N/A')}")
    _print(f"  Sandbox:    {env.get('sandbox_cpu_count', '?')} vCPU / {env.get('sandbox_memory_mb', '?')} MB")
    _print(f"  Duration:   {summary.get('total_duration_s', 0):.1f}s")
    _print(f"  Workloads:  {summary.get('workloads_run', 0)}  |  Errors: {summary.get('errors', 0)}")
    _print()

    deltas_map = {}
    if baseline:
        deltas = compute_overhead(data, baseline)
        deltas_map = {(d["suite"], d["name"]): d for d in deltas}

    for suite_name in get_suites(data):
        suite_label = SUITE_LABELS.get(suite_name, suite_name)
        _print(f"  {suite_label}")
        _print(f"  {'─' * 68}")

        header = f"  {'Workload':<24s} {'Mean':>10s} {'StdDev':>10s} {'CV%':>7s} {'Unit':<10s}"
        if baseline:
            header += f" {'Overhead':>9s}"
        _print(header)

        for wl in get_workloads(data, suite_name):
            stats = wl.get("stats", {})
            name = wl["name"]
            mean = stats.get("mean", 0)
            stddev = stats.get("stddev", 0)
            cv = stats.get("cv", 0) * 100
            unit = wl.get("unit", "")

            line = f"  {name:<24s} {mean:>10.2f} {stddev:>10.2f} {cv:>6.1f}% {unit:<10s}"
            if baseline:
                key = (suite_name, name)
                if key in deltas_map:
                    d = deltas_map[key]
                    sign = "+" if d["delta_pct"] >= 0 else ""
                    line += f" {sign}{d['delta_pct']:>7.1f}%"
                else:
                    line += f" {'N/A':>8s}"
            _print(line)
        _print()

    if baseline and deltas_map:
        deltas = list(deltas_map.values())
        ovh = overall_overhead(deltas)
        g = grade_overhead(ovh)
        _print(f"  Overall Overhead: {ovh:+.2f}%  Grade: {g} ({GRADE_DESC[g]})")
        _print()

    return buf.getvalue()


def main():
    parser = argparse.ArgumentParser(
        description="Analyze and visualize cube-pvm-bench results",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("result_file", help="Primary JSON result file (e.g. result.json)")
    parser.add_argument("-b", "--baseline", help="Baseline JSON file for overhead comparison (e.g. kvm.json)")
    parser.add_argument("-o", "--output", help="Output prefix for PNG files (omit to show interactive)")
    parser.add_argument("--summary-only", action="store_true", help="Print text summary only, no charts")
    parser.add_argument("--save-summary", metavar="FILE", help="Save text summary to file (e.g. summary.txt)")
    args = parser.parse_args()

    data = load_result(args.result_file)
    baseline = load_result(args.baseline) if args.baseline else None

    summary_text = print_summary_to_string(data, baseline)
    print(summary_text)

    if args.save_summary:
        with open(args.save_summary, "w") as f:
            f.write(summary_text)
        print(f"  Summary saved to {args.save_summary}")

    if args.summary_only:
        return

    plt.style.use("seaborn-v0_8-whitegrid")
    plt.rcParams.update({
        "font.family": "sans-serif",
        "figure.facecolor": "white",
    })

    figs = []
    print("  Generating charts...")
    figs.append(plot_suite_bars(data, save_prefix=args.output))
    figs.append(plot_radar(data, baseline, save_prefix=args.output))
    figs.append(plot_box_distributions(data, save_prefix=args.output))
    figs.append(plot_cv_heatmap(data, save_prefix=args.output))

    if baseline:
        figs.append(plot_overhead(data, baseline, save_prefix=args.output))

    if not args.output:
        print("  Showing interactive plots (close windows to exit)...")
        plt.show()
    else:
        print(f"  Done. All charts saved with prefix '{args.output}_'")


if __name__ == "__main__":
    main()
