# cube-pvm-bench

Comprehensive benchmark for measuring PVM (nested virtualization) overhead in CubeSandbox.

## Overview

This tool runs a suite of workloads **inside** CubeSandbox instances via the Go SDK, measuring performance across six dimensions:

| Suite | Workloads | What it measures |
|-------|-----------|-----------------|
| **cpu** | integer-arith, float-matrix, prime-sieve, **multiproc-compute**, **json-parse** | Compute throughput (single + multi-core) |
| **memory** | seq-bandwidth, random-latency, page-fault, **multiproc-mmap**, **large-alloc-fragment** | Memory subsystem + shadow page table pressure |
| **disk** | seq-write, seq-read, random-4k, fsync-latency, **many-small-files**, **concurrent-io** | Block I/O (sequential + agent-realistic patterns) |
| **network** | loopback-throughput, gateway-ping, **concurrent-conns** | Network stack (single + multi-connection) |
| **syscall** | getpid-loop, mmap-cycle, fork-exec, **pip-install-sim**, **concurrent-subprocess** | VM-exit cost under realistic process patterns |
| **lifecycle** | create, snapshot, rollback, clone | Sandbox operation latency |

**Bold** workloads simulate real AI Agent usage patterns (multi-process compute, package installation, code generation, concurrent API calls).

## Build

```bash
make
```

## Usage

### Basic run (all suites)

```bash
export CUBE_API_URL=http://your-api:3000
export CUBE_API_KEY=your-key
export CUBE_TEMPLATE_ID=your-template

./bin/cube-pvm-bench -n 5 -o results.json -l kvm-native
```

### Compare PVM vs native KVM

```bash
# Run on native KVM host
./bin/cube-pvm-bench -o baseline-kvm.json -l kvm-native

# Run on PVM host
./bin/cube-pvm-bench -o pvm.json -l pvm -b baseline-kvm.json
```

### Run specific suites

```bash
./bin/cube-pvm-bench -s cpu,memory -n 10
```

### Dry-run mode (no real sandbox)

```bash
./bin/cube-pvm-bench --dry-run -n 3 --no-tui
```

## Flags

```
  -s, --suite <names>     Suites: cpu,memory,disk,network,syscall,lifecycle (default: all)
  -n, --iterations <int>  Iterations per workload (default: 5)
  -w, --warmup <int>      Warmup iterations (default: 1)
  -c, --concurrency <int> Concurrency for lifecycle suite (default: 5)
  -o, --output <file>     Export JSON results
  -b, --baseline <file>   Load prior run for overhead comparison
  -l, --label <string>    Tag this run (e.g. "pvm", "kvm-native")
  -t, --template <id>     Template ID
      --api-url <url>     CubeAPI URL
      --api-key <key>     API key
      --timeout <dur>     Per-command timeout (default: 120s)
      --theme <name>      dark|light|auto
      --no-tui            Disable interactive TUI
      --json              JSON-only output to stdout
      --verbose           Show raw command output
      --dry-run           Simulate with synthetic data
```

## Requirements

The sandbox template must have **Python 3** installed (used by CPU, memory, syscall, and some disk workloads).

## Output

JSON schema (version 1):

```json
{
  "version": 1,
  "timestamp": "...",
  "label": "pvm",
  "config": { "template", "api_url", "iterations", "warmup", "concurrency", "suites" },
  "environment": { "hostname", "go_version", "sandbox_cpu_count", "sandbox_memory_mb" },
  "suites": {
    "<suite>": {
      "workloads": [{
        "name": "...",
        "unit": "...",
        "higher_is_better": true,
        "samples": [...],
        "stats": { "count", "mean", "stddev", "min", "max", "p50", "p90", "p95", "p99", "cv" }
      }]
    }
  },
  "summary": { "total_duration_s", "suites_run", "workloads_run", "errors" }
}
```

## Grading (with --baseline)

| Grade | Overhead | Interpretation |
|-------|----------|----------------|
| S | < 2% | Near-native performance |
| A | 2-5% | Excellent |
| B | 5-15% | Acceptable |
| C | 15-30% | Significant overhead |
| D | > 30% | Concerning |
