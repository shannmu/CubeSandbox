#!/usr/bin/env python3
"""Extract first error from each failed workload in a cube-pvm-bench JSON result."""

import json
import sys

if len(sys.argv) < 2:
    print("Usage: python3 show_errors.py <result.json>")
    sys.exit(1)

with open(sys.argv[1]) as f:
    data = json.load(f)

for suite, sr in data["suites"].items():
    for wl in sr["workloads"]:
        errs = wl.get("errors", [])
        if errs:
            name = wl["name"]
            print(f"{suite}/{name}: {errs[0]}")
            if len(errs) > 1:
                print(f"  ... and {len(errs)-1} more errors")
