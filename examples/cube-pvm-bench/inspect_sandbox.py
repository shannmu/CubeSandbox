#!/usr/bin/env python3
"""Inspect sandbox filesystem structure, block devices, and mount points."""

import os
from e2b_code_interpreter import Sandbox

commands = [
    ("lsblk -f", "Block devices and filesystems"),
    ("lspci", "PCI devices"),
    ("mount | grep -v cgroup", "Mount points"),
    ("df -hT", "Disk usage"),
    ("ls -la /run/blk-cube/ 2>/dev/null || echo '/run/blk-cube not found'", "virtio-blk mount dir"),
    ("ls -la /tmp/", "Tmp directory"),
    ("stat -f /tmp/ 2>/dev/null || echo 'stat -f not supported'", "Tmp filesystem info"),
    ("cat /proc/mounts | grep -E 'pmem|vd|tmpfs|ext4'", "Relevant mounts from /proc"),
    ("cat /proc/cmdline", "Kernel cmdline"),
]

with Sandbox.create(template=os.environ["CUBE_TEMPLATE_ID"]) as sb:
    print(f"Sandbox: {sb.sandbox_id}")
    print()

    for cmd, desc in commands:
        code = f"""
import subprocess, sys
r = subprocess.run({cmd!r}, shell=True, capture_output=True, text=True)
sys.stdout.write(r.stdout)
if r.stderr:
    sys.stderr.write(r.stderr)
"""
        r = sb.run_code(code)
        stdout = "".join(r.logs.stdout) if r.logs.stdout else ""
        stderr = "".join(r.logs.stderr) if r.logs.stderr else ""
        print(f"=== {desc} ({cmd}) ===")
        print(stdout.strip() if stdout.strip() else "(empty)")
        if stderr.strip():
            print(f"[stderr] {stderr.strip()}")
        print()
