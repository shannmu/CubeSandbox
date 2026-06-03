package main

func cpuWorkloads() []Workload {
	return []Workload{
		{
			Name:        "integer-arith",
			Suite:       "cpu",
			Description: "Integer arithmetic: sum 50M integers",
			Unit:        "Mops/s",
			HigherIsBetter: true,
			Command: `python3 -c "
import time
N=50_000_000
t=time.perf_counter()
s=0
for i in range(N): s+=i
elapsed=time.perf_counter()-t
print(f'{N/elapsed/1e6:.2f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "float-matrix",
			Suite:       "cpu",
			Description: "Floating point: 300x300 matrix multiply",
			Unit:        "MFLOPS",
			HigherIsBetter: true,
			Command: `python3 -c "
import time,random
N=300
random.seed(42)
A=[[random.random() for _ in range(N)] for _ in range(N)]
B=[[random.random() for _ in range(N)] for _ in range(N)]
t=time.perf_counter()
C=[[sum(A[i][k]*B[k][j] for k in range(N)) for j in range(N)] for i in range(N)]
elapsed=time.perf_counter()-t
print(f'{2*N**3/elapsed/1e6:.2f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "prime-sieve",
			Suite:       "cpu",
			Description: "Sieve of Eratosthenes to 5M",
			Unit:        "ms",
			HigherIsBetter: false,
			Command: `python3 -c "
import time
def sieve(n):
    s=[True]*(n+1); s[0]=s[1]=False
    for i in range(2,int(n**0.5)+1):
        if s[i]:
            for j in range(i*i,n+1,i): s[j]=False
    return sum(s)
N=5_000_000
t=time.perf_counter()
c=sieve(N)
elapsed=time.perf_counter()-t
print(f'{elapsed*1000:.2f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "multiproc-compute",
			Suite:       "cpu",
			Description: "Multi-process parallel compute: 4 workers x 10M ops",
			Unit:        "Mops/s",
			HigherIsBetter: true,
			Command: `python3 -c "
import multiprocessing,time
def worker(_):
    s=0
    for i in range(10_000_000): s+=i*i
    return s
t=time.perf_counter()
with multiprocessing.Pool(4) as p:
    p.map(worker,range(4))
elapsed=time.perf_counter()-t
print(f'{4*10/elapsed:.2f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "json-parse",
			Suite:       "cpu",
			Description: "JSON parse throughput: 50K objects x 5 rounds",
			Unit:        "MB/s",
			HigherIsBetter: true,
			Command: `python3 -c "
import json,time
data=[{'id':i,'name':f'item_{i}','values':list(range(100))} for i in range(50000)]
blob=json.dumps(data)
t=time.perf_counter()
for _ in range(5):
    parsed=json.loads(blob)
elapsed=time.perf_counter()-t
print(f'{5*len(blob)/elapsed/1e6:.2f}')
"`,
			ParseResult: parseFloat,
		},
	}
}
