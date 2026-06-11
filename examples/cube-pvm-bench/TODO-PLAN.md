# cube-pvm-bench: PVM 嵌套虚拟化性能基准测试

## 目标

构建全面的 benchmark 工具，量化 PVM（影子页表嵌套虚拟化）相对于原生 KVM 的性能损耗，覆盖 CPU、内存、磁盘 I/O、网络、系统调用、沙箱生命周期六大维度。

---

## 文件结构

```
examples/cube-pvm-bench/
├── go.mod                  # 依赖 cubesandbox SDK + charm TUI 库
├── Makefile                # build / clean
├── main.go                 # CLI 入口，flag 解析，流程编排
├── config.go               # Config 结构体，环境变量/flag 解析
├── workloads.go            # Workload 注册接口 + 套件定义
├── workloads_cpu.go        # CPU 套件: integer-arith, float-matrix, prime-sieve
├── workloads_memory.go     # 内存套件: seq-bandwidth, random-latency, page-fault
├── workloads_disk.go       # 磁盘套件: seq-write, seq-read, random-4k, fsync
├── workloads_network.go    # 网络套件: loopback-throughput, gateway-ping
├── workloads_syscall.go    # 系统调用套件: getpid, mmap-cycle, fork-exec
├── workloads_lifecycle.go  # 生命周期套件: create, snapshot, rollback, clone
├── runner.go               # 编排器: 创建沙箱 → 执行套件 → 收集结果
├── results.go              # 结果结构体, JSON schema, 统计聚合
├── compare.go              # 基线加载, 损耗% 计算, 显著性检测
├── stats.go                # 统计函数 (从 cube-bench 移植 + 扩展)
├── report.go               # lipgloss 面板式终端报告
├── theme.go                # 主题 (从 cube-bench 移植 + 损耗着色)
├── ui.go                   # Bubbletea 实时进度 TUI
└── README.md               # 使用文档
```

---

## CLI 接口

```
cube-pvm-bench [flags]
  -s, --suite <names>     运行指定套件: cpu,memory,disk,network,syscall,lifecycle (默认: all)
  -n, --iterations <int>  每个 workload 迭代次数 (默认: 5)
  -w, --warmup <int>      预热迭代次数 (默认: 1)
  -c, --concurrency <int> lifecycle 套件并发数 (默认: 5)
  -o, --output <file>     导出 JSON 结果
  -b, --baseline <file>   加载先前运行结果进行损耗对比
  -l, --label <string>    标记本次运行 (如 "pvm", "kvm-native")
  -t, --template <id>     模板 ID (环境变量: CUBE_TEMPLATE_ID)
      --api-url <url>     CubeAPI URL (环境变量: CUBE_API_URL)
      --api-key <key>     API 密钥 (环境变量: CUBE_API_KEY)
      --timeout <dur>     单命令超时 (默认: 120s)
      --theme <name>      dark|light|auto
      --no-tui            禁用交互式 TUI
      --json              仅输出 JSON 到 stdout
      --verbose           显示原始命令输出
```

---

## 执行流程

1. 解析 flags，验证配置 (template/API URL/key)
2. 通过 Go SDK 创建一个沙箱用于所有 in-sandbox workloads
3. 对每个选中套件，在沙箱内依次执行各 workload:
   - 预热迭代 (结果丢弃)
   - N 次测量迭代，收集 (value, unit)
4. lifecycle 套件单独处理 (需要创建/销毁独立沙箱)
5. 销毁主沙箱
6. 计算统计量，渲染报告，可选对比基线

---

## Workload 详细设计

### CPU 套件

| Workload | 命令 | 指标 | 方向 |
|----------|------|------|------|
| integer-arith | Python 循环累加 50M 整数 | Mops/s | higher=better |
| float-matrix | Python 512x512 矩阵乘 | MFLOPS | higher=better |
| prime-sieve | Eratosthenes 筛法 5M | ms | lower=better |

### 内存套件

| Workload | 命令 | 指标 | 方向 |
|----------|------|------|------|
| seq-bandwidth | Python array copy 64MB x10 | GB/s | higher=better |
| random-latency | 随机数组访问 2M 次 | ns/access | lower=better |
| page-fault | mmap 256MB 逐页触摸 | Kpages/s | higher=better |

### 磁盘 I/O 套件

| Workload | 命令 | 指标 | 方向 |
|----------|------|------|------|
| seq-write | dd if=/dev/zero bs=1M count=256 conv=fdatasync | MB/s | higher=better |
| seq-read | dd if=file of=/dev/null bs=1M | MB/s | higher=better |
| random-4k | Python 随机 4K 写 10K 次 + fsync | IOPS | higher=better |
| fsync-latency | 1000x write+fsync 循环 | us | lower=better |

### 网络套件

| Workload | 命令 | 指标 | 方向 |
|----------|------|------|------|
| loopback-throughput | Python socket 回环 5s | Gbps | higher=better |
| gateway-ping | ping -c 20 默认网关 | ms RTT | lower=better |

### 系统调用套件

| Workload | 命令 | 指标 | 方向 |
|----------|------|------|------|
| getpid-loop | 1M 次 os.getpid() | ns/call | lower=better |
| mmap-cycle | 50K 次 mmap+munmap | us/cycle | lower=better |
| fork-exec | 200x subprocess /bin/true | ms/fork-exec | lower=better |

### 生命周期套件 (Go driver 直接计时)

| Workload | 操作 | 指标 | 方向 |
|----------|------|------|------|
| create | client.Create() | ms | lower=better |
| snapshot | POST /sandboxes/{id}/snapshots | ms | lower=better |
| rollback | POST /sandboxes/{id}/rollback | ms | lower=better |
| clone | snapshot + create-from-snapshot | ms | lower=better |

---

## 对比与评分

当提供 `--baseline` 时，计算每个 workload 的损耗:
- lower-is-better: `overhead% = (current - baseline) / baseline * 100`
- higher-is-better: `overhead% = (baseline - current) / baseline * 100`

正值 = PVM 更慢。颜色编码:
- 绿色: < 5% 损耗
- 黄色: 5-20% 损耗
- 红色: > 20% 损耗

总体 PVM 损耗分 = 所有损耗因子的几何平均值。

等级: S(<2%), A(2-5%), B(5-15%), C(15-30%), D(>30%)

---

## JSON 输出 Schema

```json
{
  "version": 1,
  "timestamp": "2026-06-03T10:00:00Z",
  "label": "pvm",
  "config": {
    "template": "...",
    "api_url": "...",
    "iterations": 5,
    "warmup": 1,
    "concurrency": 5,
    "suites": ["cpu","memory","disk","network","syscall","lifecycle"]
  },
  "environment": {
    "hostname": "...",
    "go_version": "go1.25",
    "sandbox_cpu_count": 2,
    "sandbox_memory_mb": 512
  },
  "suites": {
    "cpu": {
      "workloads": [{
        "name": "integer-arith",
        "unit": "Mops/s",
        "higher_is_better": true,
        "samples": [1523.4, 1518.2, 1525.1, 1520.0, 1519.8],
        "stats": {
          "count": 5, "mean": 1521.3, "stddev": 2.8,
          "min": 1518.2, "max": 1525.1,
          "p50": 1520.0, "p90": 1525.1, "p95": 1525.1, "p99": 1525.1,
          "cv": 0.0018
        }
      }]
    }
  },
  "summary": {
    "total_duration_s": 245.6,
    "suites_run": 6,
    "workloads_run": 17,
    "errors": 0
  }
}
```

---

## 依赖

- `github.com/tencentcloud/CubeSandbox/sdk/go` — 沙箱创建与命令执行
- `github.com/charmbracelet/bubbletea` — 实时 TUI
- `github.com/charmbracelet/lipgloss` — 终端样式
- `github.com/charmbracelet/bubbles` — 进度条
- `golang.org/x/term` — 终端检测

---

## 从 cube-bench 复用

复制并适配 (两者都是 `package main`，无法直接 import):
- `stats.go`: Mean, StdDev, Percentile, Min, Max, Sparkline, Histogram + 新增 GeometricMean, CV
- `theme.go`: DarkTheme/LightTheme/DetectTheme + 新增 OverheadStyle

---

## 实现顺序 (TODO)

- [x] 1. 初始化项目: go.mod, Makefile, config.go, main.go 骨架
- [x] 2. 实现 workloads.go 接口定义 + workload 注册
- [x] 3. 实现 results.go 结果结构体与 JSON 序列化
- [x] 4. 实现 stats.go (从 cube-bench 移植 + 扩展)
- [x] 5. 实现 runner.go 编排逻辑 (SDK 集成)
- [x] 6. 实现 workloads_cpu.go (micro-benchmark)
- [x] 7. 实现 workloads_memory.go (micro-benchmark)
- [x] 8. 实现 workloads_disk.go (micro-benchmark)
- [x] 9. 实现 workloads_network.go (micro-benchmark)
- [x] 10. 实现 workloads_syscall.go (micro-benchmark)
- [x] 11. 实现 workloads_lifecycle.go
- [x] 12. 实现 compare.go 对比逻辑
- [x] 13. 实现 theme.go (从 cube-bench 移植 + 扩展)
- [x] 14. 实现 report.go 终端报告渲染
- [x] 15. 实现 ui.go Bubbletea TUI
- [x] 16. 编写 README.md
- [ ] 17. 添加真实 Agent 负载压力测试 (见下方)

---

## Phase 2: 真实 Agent 负载压力测试

### 设计原则

负载贴近 AI Agent 在 sandbox 中的真实使用形态：
- 代码执行：多进程并行数据处理、JSON 解析
- 包安装模拟：高频 fork/exec + 文件创建 + 模块导入
- 文件操作：批量生成/读取小文件（代码生成）
- 并发 I/O：多进程同时写磁盘（pip 下载解压）
- 网络并发：多连接同时请求（API 调用）

### 为什么这些 workload 能暴露 PVM 损耗

| Workload | PVM 压力点 |
|----------|-----------|
| multiproc-compute | 多 vCPU 竞争, IPI 触发 VM-exit |
| json-parse | 大堆分配, GC → 频繁 page fault 穿过 shadow PT |
| multiproc-mmap | 大量 shadow page table 创建 (4 worker × 200MB) |
| large-alloc-fragment | 频繁 minor fault, 堆增长穿过 shadow PT |
| many-small-files | 高 syscall 频率 (每文件 open/write/fsync/close) |
| concurrent-io | 多 vCPU 并发 VM-exit (IO 路径) |
| concurrent-conns | socket syscall 风暴 + 调度器压力 |
| pip-install-sim | fork+exec+mmap (新进程 = 完整 shadow PT 重建) |
| concurrent-subprocess | 8 并发 Python 进程的 VM-exit 风暴 |

---

### CPU 套件 — 新增

#### `multiproc-compute` (多进程并行计算)

模拟 Agent 运行并行数据处理（如 pytest 并行 worker）

```python
import multiprocessing, time

def worker(_):
    s = 0
    for i in range(10_000_000):
        s += i * i
    return s

t = time.perf_counter()
with multiprocessing.Pool(4) as p:
    p.map(worker, range(4))  # 4 workers × 10M ops
elapsed = time.perf_counter() - t
print(f'{4*10/elapsed:.2f}')  # total Mops/s
```

- 指标: Mops/s, higher=better
- 压力: 多 vCPU 同时满载，测 PVM 多核调度损耗

#### `json-parse` (JSON 解析吞吐)

模拟 Agent 解析大型 LLM 工具调用返回

```python
import json, time

data = [{"id": i, "name": f"item_{i}", "values": list(range(100))} for i in range(50000)]
blob = json.dumps(data)
t = time.perf_counter()
for _ in range(5):
    parsed = json.loads(blob)
elapsed = time.perf_counter() - t
print(f'{5*len(blob)/elapsed/1e6:.2f}')  # MB/s
```

- 指标: MB/s, higher=better
- 压力: 大量堆分配 + GC，触发 page fault

---

### Memory 套件 — 新增

#### `multiproc-mmap` (多进程大规模映射)

模拟 pip install 时多个子进程各自映射共享库

```python
import multiprocessing, mmap, time

def worker(_):
    regions = []
    for _ in range(50):
        m = mmap.mmap(-1, 4*1024*1024)  # 50 × 4MB = 200MB per worker
        m[0:4096] = b'x' * 4096
        regions.append(m)
    for m in regions:
        m.close()

t = time.perf_counter()
with multiprocessing.Pool(4) as p:
    p.map(worker, range(4))  # 4 workers × 200MB = 800MB 页表压力
elapsed = time.perf_counter() - t
print(f'{4*50*4/elapsed:.0f}')  # MB/s
```

- 指标: MB/s, higher=better
- 压力: 大量 shadow page table 条目创建，PVM 核心开销路径

#### `large-alloc-fragment` (大量堆碎片分配)

模拟 Agent 构建大型数据结构（DataFrame, dict）

```python
import time

t = time.perf_counter()
items = {}
for i in range(500_000):
    items[f'key_{i}'] = [i] * 10  # 500K keys × 10 元素 list
total = sum(len(v) for v in items.values())
elapsed = time.perf_counter() - t
print(f'{elapsed*1000:.1f}')
```

- 指标: ms, lower=better
- 压力: 频繁 minor page fault，堆持续增长

---

### Disk 套件 — 新增

#### `many-small-files` (批量小文件创建)

模拟 Agent 代码生成（写 2000 个 .py 文件）

```python
import os, time

DIR = '/tmp/bench_files'
os.makedirs(DIR, exist_ok=True)
N = 2000
content = b'import os\nprint("hello")\n' * 20  # ~500B per file
t = time.perf_counter()
for i in range(N):
    path = f'{DIR}/file_{i:04d}.py'
    fd = os.open(path, os.O_CREAT | os.O_WRONLY | os.O_TRUNC)
    os.write(fd, content)
    os.fsync(fd)
    os.close(fd)
elapsed = time.perf_counter() - t
for i in range(N):
    os.unlink(f'{DIR}/file_{i:04d}.py')
os.rmdir(DIR)
print(f'{N/elapsed:.0f}')
```

- 指标: files/s, higher=better
- 压力: 高频 syscall (open/write/fsync/close × 2000)

#### `concurrent-io` (并发磁盘写)

模拟 pip 下载解压时多进程同时写磁盘

```python
import multiprocessing, os, time

def writer(idx):
    path = f'/tmp/bench_cio_{idx}'
    fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_TRUNC)
    data = b'x' * 4096
    for _ in range(5000):
        os.write(fd, data)
    os.fsync(fd)
    os.close(fd)
    os.unlink(path)

t = time.perf_counter()
with multiprocessing.Pool(4) as p:
    p.map(writer, range(4))
elapsed = time.perf_counter() - t
print(f'{4*5000*4/1024/elapsed:.1f}')  # MB/s aggregate
```

- 指标: MB/s, higher=better
- 压力: 4 进程并发 IO，多 vCPU VM-exit 竞争

---

### Network 套件 — 新增

#### `concurrent-conns` (多连接并发)

模拟 Agent 并行调用多个 API

```python
import socket, time, threading, queue

CONNS = 20
DURATION = 3
results = queue.Queue()

def drain(c):
    while True:
        d = c.recv(65536)
        if not d:
            break
    c.close()

def server():
    s = socket.socket()
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(('127.0.0.1', 19998))
    s.listen(CONNS)
    while True:
        c, _ = s.accept()
        threading.Thread(target=drain, args=(c,), daemon=True).start()

def client(idx):
    total = 0
    data = b'x' * 65536
    s = socket.socket()
    s.connect(('127.0.0.1', 19998))
    start = time.perf_counter()
    while time.perf_counter() - start < DURATION:
        s.sendall(data)
        total += len(data)
    s.close()
    results.put(total)

threading.Thread(target=server, daemon=True).start()
time.sleep(0.1)
threads = [threading.Thread(target=client, args=(i,)) for i in range(CONNS)]
for t in threads: t.start()
for t in threads: t.join()
total = sum(results.get() for _ in range(CONNS))
print(f'{total*8/DURATION/1e9:.2f}')
```

- 指标: Gbps, higher=better
- 压力: 20 并发连接 socket 调用风暴 + 线程调度

---

### Syscall 套件 — 新增

#### `pip-install-sim` (pip install 模拟)

模拟真实 pip install 的模式: fork Python → import → 写文件 → 退出

```python
import subprocess, time

N = 50
t = time.perf_counter()
for i in range(N):
    subprocess.run(['python3', '-c', f'''
import json, os, sys
d = {{"pkg": "{i}", "deps": list(range(20))}}
path = "/tmp/pkg_{i}.json"
with open(path, "w") as f:
    json.dump(d, f)
os.unlink(path)
'''], capture_output=True)
elapsed = time.perf_counter() - t
print(f'{elapsed/N*1000:.1f}')
```

- 指标: ms/iter, lower=better
- 压力: 每次迭代 fork + 新进程完整 shadow PT 重建 + import + file IO

#### `concurrent-subprocess` (并发子进程)

模拟 Agent 同时运行测试/lint/format 等工具

```python
import subprocess, time, concurrent.futures

CMD = ['python3', '-c', 's=sum(range(1_000_000));print(s)']
N = 40
t = time.perf_counter()
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as ex:
    futs = [ex.submit(subprocess.run, CMD, capture_output=True) for _ in range(N)]
    concurrent.futures.wait(futs)
elapsed = time.perf_counter() - t
print(f'{N/elapsed:.1f}')
```

- 指标: procs/s, higher=better
- 压力: 8 并发 Python 进程 VM-exit 风暴

---

### 需要修改的文件

| 文件 | 变更 |
|------|------|
| `workloads_cpu.go` | 追加 multiproc-compute, json-parse |
| `workloads_memory.go` | 追加 multiproc-mmap, large-alloc-fragment |
| `workloads_disk.go` | 追加 many-small-files, concurrent-io |
| `workloads_network.go` | 追加 concurrent-conns |
| `workloads_syscall.go` | 追加 pip-install-sim, concurrent-subprocess |
| `runner.go` | 更新 dryBaselines map |
| `README.md` | 更新 workload 列表 |

---

## 验证步骤

1. `cd examples/cube-pvm-bench && make` — 编译成功
2. `./bin/cube-pvm-bench --dry-run -n 2 --no-tui` — 合成数据运行，产出报告
3. `./bin/cube-pvm-bench -s cpu -n 3 -o kvm.json -l kvm-native` — 对真实沙箱运行 CPU 套件
4. `./bin/cube-pvm-bench -s cpu -n 3 -o pvm.json -l pvm -b kvm.json` — PVM vs 基线对比
5. JSON 输出可被 `jq` 正确解析

## 测试结果
╭────────────────────────────────────────────────────────────────────────────────╮
│                                                                                │
│     Configuration                                                              │
│                                                                                │
│     Template tpl-ec2cc659fbdb417d9250b9fb                                      │
│     API URL http://127.0.0.1:3000                                              │
│     Suites cpu, memory, disk, network, syscall, lifecycle                      │
│     Iterations 5 (warmup: 1)                                                   │
│     Concurrency 5                                                              │
│     Timeout 2m0s                                                               │
│     Label pvm                                                                  │
│                                                                                │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯

  CPU
    integer-arith      [1/5] = 8.56 Mops/s
    integer-arith      [2/5] = 8.41 Mops/s
    integer-arith      [3/5] = 8.16 Mops/s
    integer-arith      [4/5] = 8.52 Mops/s
    integer-arith      [5/5] = 8.06 Mops/s
    float-matrix       [1/5] = 18.25 MFLOPS
    float-matrix       [2/5] = 17.88 MFLOPS
    float-matrix       [3/5] = 18.10 MFLOPS
    float-matrix       [4/5] = 18.23 MFLOPS
    float-matrix       [5/5] = 15.08 MFLOPS
    prime-sieve        [1/5] = 632.46 ms
    prime-sieve        [2/5] = 639.07 ms
    prime-sieve        [3/5] = 668.17 ms
    prime-sieve        [4/5] = 619.10 ms
    prime-sieve        [5/5] = 682.03 ms
    multiproc-compute  [1/5] = 22.22 Mops/s
    multiproc-compute  [2/5] = 21.69 Mops/s
    multiproc-compute  [3/5] = 22.25 Mops/s
    multiproc-compute  [4/5] = 21.83 Mops/s
    multiproc-compute  [5/5] = 21.77 Mops/s
    json-parse         [1/5] = 40.12 MB/s
    json-parse         [2/5] = 40.18 MB/s
    json-parse         [3/5] = 40.93 MB/s
    json-parse         [4/5] = 40.28 MB/s
    json-parse         [5/5] = 41.58 MB/s

  MEMORY
    seq-bandwidth      [1/5] = 0.77 GB/s
    seq-bandwidth      [2/5] = 0.78 GB/s
    seq-bandwidth      [3/5] = 0.79 GB/s
    seq-bandwidth      [4/5] = 0.78 GB/s
    seq-bandwidth      [5/5] = 0.78 GB/s
    random-latency     [1/5] = 215.50 ns/access
    random-latency     [2/5] = 213.65 ns/access
    random-latency     [3/5] = 196.79 ns/access
    random-latency     [4/5] = 197.26 ns/access
    random-latency     [5/5] = 193.35 ns/access
    page-fault         [1/5] = 292.47 Kpages/s
    page-fault         [2/5] = 300.09 Kpages/s
    page-fault         [3/5] = 299.31 Kpages/s
    page-fault         [4/5] = 292.64 Kpages/s
    page-fault         [5/5] = 299.55 Kpages/s
    multiproc-mmap     [1/5] = 5167.00 MB/s
    multiproc-mmap     [2/5] = 5395.00 MB/s
    multiproc-mmap     [3/5] = 5132.00 MB/s
    multiproc-mmap     [4/5] = 5306.00 MB/s
    multiproc-mmap     [5/5] = 5210.00 MB/s
    large-alloc-fragment [1/5] = 734.80 ms
    large-alloc-fragment [2/5] = 736.70 ms
    large-alloc-fragment [3/5] = 730.00 ms
    large-alloc-fragment [4/5] = 738.60 ms
    large-alloc-fragment [5/5] = 716.70 ms

  DISK
    seq-write          [1/5] = 1331.20 MB/s
    seq-write          [2/5] = 1331.20 MB/s
    seq-write          [3/5] = 1433.60 MB/s
    seq-write          [4/5] = 1536.00 MB/s
    seq-write          [5/5] = 1536.00 MB/s
    seq-read           [1/5] = 3174.40 MB/s
    seq-read           [2/5] = 3276.80 MB/s
    seq-read           [3/5] = 3174.40 MB/s
    seq-read           [4/5] = 3174.40 MB/s
    seq-read           [5/5] = 3072.00 MB/s
    random-4k          [1/5] = 102774.00 IOPS
    random-4k          [2/5] = 96945.00 IOPS
    random-4k          [3/5] = 98401.00 IOPS
    random-4k          [4/5] = 100688.00 IOPS
    random-4k          [5/5] = 102695.00 IOPS
    fsync-latency      [1/5] = 45.20 us
    fsync-latency      [2/5] = 47.20 us
    fsync-latency      [3/5] = 45.40 us
    fsync-latency      [4/5] = 44.10 us
    fsync-latency      [5/5] = 44.30 us
    many-small-files   [1/5] = 4965.00 files/s
    many-small-files   [2/5] = 5040.00 files/s
    many-small-files   [3/5] = 4595.00 files/s
    many-small-files   [4/5] = 4999.00 files/s
    many-small-files   [5/5] = 4680.00 files/s
    concurrent-io      [1/5] = 357.50 MB/s
    concurrent-io      [2/5] = 377.70 MB/s
    concurrent-io      [3/5] = 375.90 MB/s
    concurrent-io      [4/5] = 348.30 MB/s
    concurrent-io      [5/5] = 340.90 MB/s

  NETWORK
    loopback-throughput [1/5] = 40.88 Gbps
    loopback-throughput [2/5] = 40.21 Gbps
    loopback-throughput [3/5] = 40.47 Gbps
    loopback-throughput [4/5] = 42.71 Gbps
    loopback-throughput [5/5] = 41.99 Gbps
    concurrent-conns   [1/5] = 22.98 Gbps
    concurrent-conns   [2/5] = 22.51 Gbps
    concurrent-conns   [3/5] = 23.42 Gbps
    concurrent-conns   [4/5] = 23.13 Gbps
    concurrent-conns   [5/5] = 22.96 Gbps

  SYSCALL
    getpid-loop        [1/5] = 425.90 ns/call
    getpid-loop        [2/5] = 432.00 ns/call
    getpid-loop        [3/5] = 426.90 ns/call
    getpid-loop        [4/5] = 429.30 ns/call
    getpid-loop        [5/5] = 433.60 ns/call
    mmap-cycle         [1/5] = 6.13 us/cycle
    mmap-cycle         [2/5] = 6.04 us/cycle
    mmap-cycle         [3/5] = 5.97 us/cycle
    mmap-cycle         [4/5] = 6.11 us/cycle
    mmap-cycle         [5/5] = 6.01 us/cycle
    fork-exec          [1/5] = 1.40 ms/call
    fork-exec          [2/5] = 1.40 ms/call
    fork-exec          [3/5] = 1.42 ms/call
    fork-exec          [4/5] = 1.45 ms/call
    fork-exec          [5/5] = 1.40 ms/call
    pip-install-sim    [1/5] = 96.90 ms/iter
    pip-install-sim    [2/5] = 97.60 ms/iter
    pip-install-sim    [3/5] = 97.70 ms/iter
    pip-install-sim    [4/5] = 97.70 ms/iter
    pip-install-sim    [5/5] = 98.00 ms/iter
    concurrent-subprocess [1/5] = 39.40 procs/s
    concurrent-subprocess [2/5] = 40.30 procs/s
    concurrent-subprocess [3/5] = 40.50 procs/s
    concurrent-subprocess [4/5] = 40.00 procs/s
    concurrent-subprocess [5/5] = 39.60 procs/s

  LIFECYCLE
    create             [1/5] = 86.82 ms
    create             [2/5] = 88.40 ms
    create             [3/5] = 81.66 ms
    create             [4/5] = 89.50 ms
    create             [5/5] = 78.28 ms
    snapshot           [1/5] = 0.50 ms
    snapshot           [2/5] = 0.46 ms
    snapshot           [3/5] = 0.43 ms
    snapshot           [4/5] = 0.46 ms
    snapshot           [5/5] = 0.42 ms
    clone              [1/5] = 74.56 ms
    clone              [2/5] = 83.66 ms
    clone              [3/5] = 84.90 ms
    clone              [4/5] = 82.72 ms
    clone              [5/5] = 76.29 ms


╭────────────────────────────────────────────────────────────────────────────────╮
│                                                                                │
│     Summary                                                                    │
│                                                                                │
│     Label pvm                                                                  │
│     Total Time 228.3s                                                          │
│     Suites 6                                                                   │
│     Workloads 28                                                               │
│     Errors 10                                                                  │
│     Sandbox 2 vCPU / 2000 MB                                                   │
│                                                                                │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯

╭────────────────────────────────────────────────────────────────────────────────╮
│                                                                                │
│    CPU  (92.9s)                                                                │
│                                                                                │
│    WORKLOAD                  VALUE     UNIT    CV%                             │
│                                                                                │
│  ────────────────────────────────────────────────────────────────────────────  │
│    integer-arith       8.34 Mops/s   2.7%                                      │
│                         █▅▂▇▁  8.1 .. 8.6                                      │
│    float-matrix      17.51 MFLOPS   7.8%                                       │
│                         █▇▇▇▁  15.1 .. 18.2                                    │
│    prime-sieve      648.17 ms   4.0%                                           │
│                         ▂▃▆▁█  619.1 .. 682.0                                  │
│    multiproc-compute      21.95 Mops/s   1.2%                                  │
│                         ▇▁█▂▁  21.7 .. 22.2                                    │
│    json-parse        40.62 MB/s   1.5%                                         │
│                         ▁▁▄▁█  40.1 .. 41.6                                    │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯

╭────────────────────────────────────────────────────────────────────────────────╮
│                                                                                │
│    MEMORY  (27.1s)                                                             │
│                                                                                │
│    WORKLOAD                  VALUE     UNIT    CV%                             │
│                                                                                │
│  ────────────────────────────────────────────────────────────────────────────  │
│    seq-bandwidth       0.78 GB/s   0.9%                                        │
│                         ▁▄█▄▄  0.8 .. 0.8                                      │
│    random-latency     203.31 ns/access   5.1%                                  │
│                         █▇▂▂▁  193.3 .. 215.5                                  │
│    page-fault       296.81 Kpages/s   1.3%                                     │
│                         ▁█▇▁▇  292.5 .. 300.1                                  │
│    multiproc-mmap    5242.00 MB/s   2.1%                                       │
│                         ▁█▁▅▃  5132.0 .. 5395.0                                │
│    large-alloc-fragment     731.36 ms   1.2%                                   │
│                         ▆▇▅█▁  716.7 .. 738.6                                  │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯

╭────────────────────────────────────────────────────────────────────────────────╮
│                                                                                │
│    DISK  (9.6s)                                                                │
│                                                                                │
│    WORKLOAD                  VALUE     UNIT    CV%                             │
│                                                                                │
│  ────────────────────────────────────────────────────────────────────────────  │
│    seq-write       1433.60 MB/s   7.1%                                         │
│                         ▁▁▄██  1331.2 .. 1536.0                                │
│    seq-read        3174.40 MB/s   2.3%                                         │
│                         ▄█▄▄▁  3072.0 .. 3276.8                                │
│    random-4k     100300.60 IOPS   2.6%                                         │
│                         █▁▂▅▇  96945.0 .. 102774.0                             │
│    fsync-latency      45.24 us   2.7%                                          │
│                         ▃█▃▁▁  44.1 .. 47.2                                    │
│    many-small-files    4855.80 files/s   4.2%                                  │
│                         ▆█▁▇▂  4595.0 .. 5040.0                                │
│    concurrent-io     360.06 MB/s   4.6%                                        │
│                         ▄█▇▂▁  340.9 .. 377.7                                  │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯

╭────────────────────────────────────────────────────────────────────────────────╮
│                                                                                │
│    NETWORK  (51.3s)                                                            │
│                                                                                │
│    WORKLOAD                  VALUE     UNIT    CV%                             │
│                                                                                │
│  ────────────────────────────────────────────────────────────────────────────  │
│    loopback-throughput      41.25 Gbps   2.6%                                  │
│                         ▂▁▁█▅  40.2 .. 42.7                                    │
│    gateway-ping       0.00 ms   0.0% [5 err]                                   │
│    concurrent-conns      23.00 Gbps   1.4%                                     │
│                         ▄▁█▅▄  22.5 .. 23.4                                    │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯

╭────────────────────────────────────────────────────────────────────────────────╮
│                                                                                │
│    SYSCALL  (44.9s)                                                            │
│                                                                                │
│    WORKLOAD                  VALUE     UNIT    CV%                             │
│                                                                                │
│  ────────────────────────────────────────────────────────────────────────────  │
│    getpid-loop      429.54 ns/call   0.8%                                      │
│                         ▁▆▁▄█  425.9 .. 433.6                                  │
│    mmap-cycle         6.05 us/cycle   1.1%                                     │
│                         █▄▁▇▂  6.0 .. 6.1                                      │
│    fork-exec          1.41 ms/call   1.5%                                      │
│                         ▁▁▃█▁  1.4 .. 1.4                                      │
│    pip-install-sim      97.58 ms/iter   0.4%                                   │
│                         ▁▅▆▆█  96.9 .. 98.0                                    │
│    concurrent-subprocess      39.96 procs/s   1.2%                             │
│                         ▁▆█▄▂  39.4 .. 40.5                                    │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯

╭────────────────────────────────────────────────────────────────────────────────╮
│                                                                                │
│    LIFECYCLE  (2.3s)                                                           │
│                                                                                │
│    WORKLOAD                  VALUE     UNIT    CV%                             │
│                                                                                │
│  ────────────────────────────────────────────────────────────────────────────  │
│    create            84.93 ms   5.6%                                           │
│                         ▆▇▃█▁  78.3 .. 89.5                                    │
│    snapshot           0.45 ms   6.4%                                           │
│                         █▄▁▄▁  0.4 .. 0.5                                      │
│    rollback           0.00 ms   0.0% [5 err]                                   │
│    clone             80.43 ms   5.8%                                           │
│                         ▁▇█▆▂  74.6 .. 84.9                                    │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯
  Results saved to pvm.json
