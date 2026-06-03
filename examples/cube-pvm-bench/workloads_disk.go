package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func diskWorkloads() []Workload {
	return []Workload{
		{
			Name:        "seq-write",
			Suite:       "disk",
			Description: "Sequential write: dd 256MB with fdatasync",
			Unit:        "MB/s",
			HigherIsBetter: true,
			Command:     `dd if=/dev/zero of=/tmp/bench_seq bs=1M count=256 conv=fdatasync 2>&1 | tail -1`,
			ParseResult: parseDDOutput,
		},
		{
			Name:        "seq-read",
			Suite:       "disk",
			Description: "Sequential read: dd 256MB from file",
			Unit:        "MB/s",
			HigherIsBetter: true,
			Command:     `echo 3 > /proc/sys/vm/drop_caches 2>/dev/null; dd if=/tmp/bench_seq of=/dev/null bs=1M 2>&1 | tail -1`,
			ParseResult: parseDDOutput,
		},
		{
			Name:        "random-4k",
			Suite:       "disk",
			Description: "Random 4K write IOPS",
			Unit:        "IOPS",
			HigherIsBetter: true,
			Command: `python3 -c "
import os,time,random
PATH='/tmp/bench_rand'
SIZE=64*1024*1024
BLOCK=4096
ITERS=10000
fd=os.open(PATH,os.O_CREAT|os.O_RDWR|os.O_TRUNC)
os.ftruncate(fd,SIZE)
data=b'x'*BLOCK
random.seed(42)
offsets=[random.randint(0,(SIZE-BLOCK)//BLOCK)*BLOCK for _ in range(ITERS)]
t=time.perf_counter()
for off in offsets:
    os.lseek(fd,off,os.SEEK_SET)
    os.write(fd,data)
os.fsync(fd)
elapsed=time.perf_counter()-t
os.close(fd)
os.unlink(PATH)
print(f'{ITERS/elapsed:.0f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "fsync-latency",
			Suite:       "disk",
			Description: "Fsync latency: 1000 write+fsync cycles",
			Unit:        "us",
			HigherIsBetter: false,
			Command: `python3 -c "
import os,time
PATH='/tmp/bench_fsync'
fd=os.open(PATH,os.O_CREAT|os.O_RDWR|os.O_TRUNC)
data=b'x'*4096
ITERS=1000
times=[]
for _ in range(ITERS):
    os.write(fd,data)
    t=time.perf_counter()
    os.fsync(fd)
    times.append(time.perf_counter()-t)
    os.lseek(fd,0,os.SEEK_SET)
os.close(fd)
os.unlink(PATH)
import statistics
print(f'{statistics.mean(times)*1e6:.1f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "many-small-files",
			Suite:       "disk",
			Description: "Code generation pattern: create 2000 small files with fsync",
			Unit:        "files/s",
			HigherIsBetter: true,
			Command: `python3 -c "
import os,time
DIR='/tmp/bench_files'
os.makedirs(DIR,exist_ok=True)
N=2000
content=b'import os\nprint(\"hello\")\n'*20
t=time.perf_counter()
for i in range(N):
    path=f'{DIR}/file_{i:04d}.py'
    fd=os.open(path,os.O_CREAT|os.O_WRONLY|os.O_TRUNC)
    os.write(fd,content)
    os.fsync(fd)
    os.close(fd)
elapsed=time.perf_counter()-t
for i in range(N): os.unlink(f'{DIR}/file_{i:04d}.py')
os.rmdir(DIR)
print(f'{N/elapsed:.0f}')
"`,
			ParseResult: parseFloat,
		},
		{
			Name:        "concurrent-io",
			Suite:       "disk",
			Description: "Concurrent disk write: 4 processes x 20MB each",
			Unit:        "MB/s",
			HigherIsBetter: true,
			Command: `python3 -c "
import multiprocessing,os,time
def writer(idx):
    path=f'/tmp/bench_cio_{idx}'
    fd=os.open(path,os.O_CREAT|os.O_RDWR|os.O_TRUNC)
    data=b'x'*4096
    for _ in range(5000):
        os.write(fd,data)
    os.fsync(fd)
    os.close(fd)
    os.unlink(path)
t=time.perf_counter()
with multiprocessing.Pool(4) as p:
    p.map(writer,range(4))
elapsed=time.perf_counter()-t
print(f'{4*5000*4/1024/elapsed:.1f}')
"`,
			ParseResult: parseFloat,
		},
	}
}

var ddSpeedRe = regexp.MustCompile(`([\d.]+)\s*(GB/s|MB/s|kB/s|bytes/s)`)

func parseDDOutput(stdout, stderr string) (float64, error) {
	combined := stdout + "\n" + stderr
	matches := ddSpeedRe.FindStringSubmatch(combined)
	if matches == nil {
		lines := strings.Split(strings.TrimSpace(combined), "\n")
		last := lines[len(lines)-1]
		fields := strings.Fields(last)
		for i, f := range fields {
			if (f == "MB/s" || f == "GB/s" || f == "kB/s") && i > 0 {
				val, err := strconv.ParseFloat(fields[i-1], 64)
				if err == nil {
					switch f {
					case "GB/s":
						return val * 1024, nil
					case "kB/s":
						return val / 1024, nil
					default:
						return val, nil
					}
				}
			}
		}
		return 0, fmt.Errorf("cannot parse dd output: %q", last)
	}

	val, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, err
	}
	switch matches[2] {
	case "GB/s":
		return val * 1024, nil
	case "kB/s":
		return val / 1024, nil
	case "bytes/s":
		return val / 1024 / 1024, nil
	default:
		return val, nil
	}
}
