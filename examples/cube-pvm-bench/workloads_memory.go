package main

func memoryWorkloads() []Workload {
	return []Workload{
		{
			Name:        "seq-bandwidth",
			Suite:       "memory",
			Description: "Sequential memory bandwidth: copy 64MB x10",
			Unit:        "GB/s",
			HigherIsBetter: true,
			Command: `python3 -c "
import time
SIZE=64*1024*1024
src=bytearray(SIZE)
t=time.perf_counter()
for _ in range(10):
    dst=bytearray(src)
elapsed=time.perf_counter()-t
print(f'{10*SIZE/elapsed/1024/1024/1024:.2f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "random-latency",
			Suite:       "memory",
			Description: "Random memory access latency: 2M accesses",
			Unit:        "ns/access",
			HigherIsBetter: false,
			Command: `python3 -c "
import time,random,array
N=1024*1024
arr=array.array('i',range(N))
random.seed(42)
indices=[random.randint(0,N-1) for _ in range(2_000_000)]
t=time.perf_counter()
s=0
for i in indices: s+=arr[i]
elapsed=time.perf_counter()-t
print(f'{elapsed/len(indices)*1e9:.2f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "page-fault",
			Suite:       "memory",
			Description: "Page fault rate: mmap 256MB and touch each page",
			Unit:        "Kpages/s",
			HigherIsBetter: true,
			Command: `python3 -c "
import time,mmap,os
SIZE=256*1024*1024
fd=os.open('/dev/zero',os.O_RDONLY)
t=time.perf_counter()
m=mmap.mmap(fd,SIZE,mmap.MAP_PRIVATE,mmap.PROT_READ)
total=0
for off in range(0,SIZE,4096): total+=m[off]
elapsed=time.perf_counter()-t
pages=SIZE//4096
print(f'{pages/elapsed/1000:.2f}')
m.close();os.close(fd)
"`,
			ParseResult: parseFloat,
		},
	}
}
