package main

import "fmt"

func syscallWorkloads() []Workload {
	return []Workload{
		{
			Name:           "lat-syscall-null",
			Suite:          "syscall",
			Description:    "lmbench lat_syscall null: empty syscall latency, 200 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_syscall -N 200 null 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
		{
			Name:           "lat-syscall-read",
			Suite:          "syscall",
			Description:    "lmbench lat_syscall read: read() syscall latency, 200 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_syscall -N 200 read 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
		{
			Name:           "lat-syscall-write",
			Suite:          "syscall",
			Description:    "lmbench lat_syscall write: write() syscall latency, 200 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_syscall -N 200 write 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
		{
			Name:           "lat-proc-fork",
			Suite:          "syscall",
			Description:    "lmbench lat_proc fork: fork latency, 50 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_proc -N 50 fork 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
		{
			Name:           "lat-proc-exec",
			Suite:          "syscall",
			Description:    "lmbench lat_proc exec: fork+exec latency, 50 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_proc -N 50 exec 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
		{
			Name:           "lat-ctx",
			Suite:          "syscall",
			Description:    "lmbench lat_ctx: context switch latency (2 processes, 0K), 200 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_ctx -s 0 -N 200 2 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchCtx,
		},
		{
			Name:           "lat-select",
			Suite:          "syscall",
			Description:    "lmbench lat_select: select() on 100 fd, 200 iterations",
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_select -n 100 -N 200 file 2>&1`, lmbenchBinDir),
			ParseResult:    parseLmbenchMicroseconds,
		},
	}
}
