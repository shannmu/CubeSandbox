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
		{
			Name:        "pip-install-sim",
			Suite:       "syscall",
			Description: "Pip install simulation: fork + import + file IO per iter",
			Unit:        "ms/iter",
			HigherIsBetter: false,
			Command: `python3 << 'PYEOF'
import subprocess,time
N=50
t=time.perf_counter()
for i in range(N):
    code = f'import json,os;d=dict(pkg=str({i}),deps=list(range(20)));path="/tmp/pkg_{i}.json";f=open(path,"w");json.dump(d,f);f.close();os.unlink(path)'
    subprocess.run(['python3','-c',code],capture_output=True)
elapsed=time.perf_counter()-t
print(f'{elapsed/N*1000:.1f}')
PYEOF`,
			ParseResult: parseFloat,
		},
		{
			Name:        "concurrent-subprocess",
			Suite:       "syscall",
			Description: "Concurrent subprocesses: 40 Python procs x 8 workers",
			Unit:        "procs/s",
			HigherIsBetter: true,
			Command: `python3 -c "
import subprocess,time,concurrent.futures
CMD=['python3','-c','s=sum(range(1_000_000));print(s)']
N=40
t=time.perf_counter()
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as ex:
    futs=[ex.submit(subprocess.run,CMD,capture_output=True) for _ in range(N)]
    concurrent.futures.wait(futs)
elapsed=time.perf_counter()-t
print(f'{N/elapsed:.1f}')
"`,
			ParseResult: parseFloat,
		},
	}
}
