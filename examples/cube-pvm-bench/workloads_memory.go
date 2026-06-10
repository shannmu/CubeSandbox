package main

import "fmt"

func memoryWorkloads() []Workload {
	return []Workload{
		{
			Name:           "mem-bandwidth-rd",
			Suite:          "memory",
			Description:    "lmbench bw_mem: read bandwidth 128MB",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        fmt.Sprintf(`%s/bw_mem 128m rd 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchBW,
		},
		{
			Name:           "mem-bandwidth-wr",
			Suite:          "memory",
			Description:    "lmbench bw_mem: write bandwidth 128MB",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        fmt.Sprintf(`%s/bw_mem 128m wr 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchBW,
		},
		{
			Name:           "mem-bandwidth-cp",
			Suite:          "memory",
			Description:    "lmbench bw_mem: copy bandwidth 128MB",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        fmt.Sprintf(`%s/bw_mem 128m cp 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchBW,
		},
		{
			Name:           "mem-latency",
			Suite:          "memory",
			Description:    "lmbench lat_mem_rd: random access latency 128MB stride 64",
			Unit:           "ns",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_mem_rd -t 128m 64 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchLatency,
		},
		{
			Name:           "mem-sysbench-rd",
			Suite:          "memory",
			Description:    "sysbench memory read: sequential 1-thread, 8G total",
			Unit:           "MiB/s",
			HigherIsBetter: true,
			Command:        `sysbench memory --memory-block-size=1K --memory-total-size=8G --memory-oper=read --threads=1 run`,
			ParseResult:    parseSysbenchMem,
		},
		{
			Name:           "mem-sysbench-wr",
			Suite:          "memory",
			Description:    "sysbench memory write: sequential 1-thread, 8G total",
			Unit:           "MiB/s",
			HigherIsBetter: true,
			Command:        `sysbench memory --memory-block-size=1K --memory-total-size=8G --memory-oper=write --threads=1 run`,
			ParseResult:    parseSysbenchMem,
		},
		{
			Name:           "lat-mmap",
			Suite:          "memory",
			Description:    "lmbench lat_mmap: mmap latency 64MB file, 50 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`dd if=/dev/zero of=/tmp/bench_mmap bs=1M count=64 2>/dev/null && %s/lat_mmap -N 50 64m /tmp/bench_mmap 2>&1; rm -f /tmp/bench_mmap`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
		{
			Name:           "lat-pagefault",
			Suite:          "memory",
			Description:    "lmbench lat_pagefault: page fault latency, 50 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`dd if=/dev/zero of=/tmp/bench_pf bs=1M count=64 2>/dev/null && %s/lat_pagefault -N 50 /tmp/bench_pf 2>&1; rm -f /tmp/bench_pf`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
		{
			Name:           "bw-mmap-rd",
			Suite:          "memory",
			Description:    "lmbench bw_mmap_rd: mmap read bandwidth 128MB",
			Unit:           "MB/s",
			HigherIsBetter: true,
			Command:        fmt.Sprintf(`dd if=/dev/zero of=/tmp/bench_bwmmap bs=1M count=128 2>/dev/null && %s/bw_mmap_rd 128m open2close /tmp/bench_bwmmap 2>&1; rm -f /tmp/bench_bwmmap`, lmbenchBinDir),
			ParseResult:    parseLmbenchBW,
		},
	}
}
