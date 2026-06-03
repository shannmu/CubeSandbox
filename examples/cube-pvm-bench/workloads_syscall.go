package main

func syscallWorkloads() []Workload {
	return []Workload{
		{
			Name:        "getpid-loop",
			Suite:       "syscall",
			Description: "getpid() syscall overhead: 1M calls",
			Unit:        "ns/call",
			HigherIsBetter: false,
			Command: `python3 -c "
import os,time
N=1_000_000
t=time.perf_counter()
for _ in range(N): os.getpid()
elapsed=time.perf_counter()-t
print(f'{elapsed/N*1e9:.1f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "mmap-cycle",
			Suite:       "syscall",
			Description: "mmap/munmap cycle: 50K iterations",
			Unit:        "us/cycle",
			HigherIsBetter: false,
			Command: `python3 -c "
import mmap,time
N=50_000
SIZE=4096
t=time.perf_counter()
for _ in range(N):
    m=mmap.mmap(-1,SIZE)
    m.close()
elapsed=time.perf_counter()-t
print(f'{elapsed/N*1e6:.2f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "fork-exec",
			Suite:       "syscall",
			Description: "fork+exec overhead: 200 subprocess calls",
			Unit:        "ms/call",
			HigherIsBetter: false,
			Command: `python3 -c "
import subprocess,time
N=200
t=time.perf_counter()
for _ in range(N):
    subprocess.run(['/bin/true'],capture_output=True)
elapsed=time.perf_counter()-t
print(f'{elapsed/N*1000:.2f}')
"`,
			ParseResult: parseFloat,
		},
	}
}
