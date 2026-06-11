# cube-pvm-bench Workload 说明文档

## 测量值获取方式

所有测量值的获取路径有两种：

### 路径 A：sandbox 内工具自报（CPU/Memory/Disk/Network/Syscall）

```
cube-pvm-bench (Go 进程, 宿主机)
  │
  ├─ sandbox.RunCode(wrapCommand(cmd))
  │    │
  │    ▼
  │  Jupyter kernel (sandbox 内, port 49999)
  │    │
  │    ├─ subprocess.run(cmd, shell=True)  ← Python wrapper 执行 shell 命令
  │    │    │
  │    │    ▼
  │    │  工具运行（sysbench/fio/lmbench/iperf3）
  │    │  工具自己完成计时和计算
  │    │  工具将结果写入 stdout
  │    │    │
  │    │    ▼
  │    ├─ stdout 返回给 Jupyter kernel
  │    │
  │    ▼
  ├─ exec.Logs.Stdout ← Go 端拿到 stdout 文本
  │
  ├─ wl.ParseResult(stdout, stderr) ← 解析器提取数值
  │
  ▼
  samples = append(samples, value)
```

关键：计时发生在 sandbox 内部，由工具自己完成。Go 端只负责收集输出、解析数值。

### 路径 B：Go 端外部计时（Lifecycle）

```
cube-pvm-bench (Go 进程)
  │
  ├─ t0 := time.Now()                    ← 开始计时
  ├─ client.Create() / doHTTP(POST ...)   ← API 调用
  ├─ elapsed := time.Since(t0)            ← 结束计时
  │
  ▼
  samples = append(samples, elapsed.Milliseconds())
```

| Workload | 计时包含什么 |
|----------|------------|
| create | 从发 HTTP 请求到 sandbox ready 可用（含调度、启动 microVM、启动 envd） |
| snapshot | POST snapshot API 的响应时间（含内存快照 + 磁盘持久化） |
| clone | snapshot + 从快照创建新 sandbox 的总时间 |

### 总结

- **CPU/Memory/Disk/Network/Syscall**：工具在 sandbox 内自行计时，结果通过 stdout 返回，Go 端只做解析。测量值不包含 sandbox 创建时间和网络传输开销。
- **Lifecycle**：Go 端从外部计时 API 调用延迟，测的是端到端的用户可感知时间。

---

## 各工具 stdout 输出与解析方式

| 工具 | stdout 输出示例 | ParseResult 做什么 |
|------|----------------|-------------------|
| sysbench cpu | `events per second: 823.45` | 正则提取 `823.45` |
| sysbench memory | `4827.31 MiB/sec` | 正则提取 `4827.31` |
| lmbench bw_mem | `128.00 12345.67`（size MB/s 两列） | 取最后一行第二列 `12345.67` |
| lmbench lat_mem_rd | `128.00000 101.30`（size ns 两列） | 取最后一行第二列 `101.30` |
| lmbench lat_syscall | `Simple syscall: 0.0892 microseconds` | 正则提取 `0.0892` |
| lmbench lat_proc | `Process fork+exit: 98.2 microseconds` | 正则提取 `98.2` |
| lmbench lat_ctx | `2 0.00 3.12`（procs size us 三列） | 取最后数据行第二列 `3.12` |
| lmbench lat_pipe | `Pipe latency: 5.23 microseconds` | 正则提取 `5.23` |
| lmbench lat_connect | `TCP/IP connection cost...: 28.5 microseconds` | 正则提取 `28.5` |
| lmbench lat_mmap | `...64.00 15.2 microseconds` | 正则提取 `15.2` |
| lmbench lat_pagefault | `Pagefaults on /tmp/...: 2.85 microseconds` | 正则提取 `2.85` |
| lmbench bw_mmap_rd | `128.00 9876.54`（size MB/s） | 取最后一行第二列 `9876.54` |
| fio | 完整 JSON（几 KB） | 解析 JSON，取 `jobs[0].write.bw_bytes` 或 `.read.iops` 或 `.sync.lat_ns.mean` |
| iperf3 | 完整 JSON（`-J` flag） | 解析 JSON，取 `end.sum_received.bits_per_second / 1e9` |
| ping | `min/avg/max/mdev = 0.1/0.3/0.5/0.1 ms` | 正则取第二个值（avg） |
| small-file-create | shell 脚本用 `date +%s%N` 计时，python 算 `2000/elapsed` | parseFloat 取最后一行 |

---

## Workload 详细说明

### CPU Suite (sysbench)

| Workload | 命令 | 测量方式 | 结果含义 |
|----------|------|----------|----------|
| **cpu-1thread** | `sysbench cpu --cpu-max-prime=20000 --threads=1 --time=10` | 单线程计算 2~20000 内所有素数，持续 10 秒 | events/s = 每秒完成多少轮素数计算 |
| **cpu-multithread** | `sysbench cpu --cpu-max-prime=20000 --threads=$(nproc)` | 同上但用所有 CPU 核 | 总 events/s，反映多核并行计算能力 |

---

### Memory Suite (lmbench + sysbench)

| Workload | 命令 | 测量方式 | 结果含义 |
|----------|------|----------|----------|
| **mem-bandwidth-rd** | `bw_mem 128m rd` | 分配 128MB 缓冲区，单线程顺序读，计时 | MB/s 读带宽（超出 L3，测 DRAM） |
| **mem-bandwidth-wr** | `bw_mem 128m wr` | 同上，顺序写 | MB/s 写带宽 |
| **mem-bandwidth-cp** | `bw_mem 128m cp` | 同上，从 src 拷贝到 dst（memcpy） | MB/s 拷贝带宽 |
| **mem-latency** | `lat_mem_rd -t 128m 64` | 128MB 指针追踪链，stride=64B（一个 cache line），串行访问 | ns/次 = 单次随机访问穿透到 DRAM 的延迟 |
| **mem-sysbench-rd** | `sysbench memory --block-size=1K --total-size=4G --oper=read` | 反复读 1KB 块，总共读 4GB | MiB/s 顺序读吞吐 |
| **mem-sysbench-wr** | `sysbench memory --block-size=1K --total-size=4G --oper=write` | 反复写 1KB 块，总共写 4GB | MiB/s 顺序写吞吐 |
| **lat-mmap** | `lat_mmap 64m /tmp/file` | 反复 mmap+touch+munmap 一个 64MB 文件 | us = 一次 mmap 操作的平均延迟 |
| **lat-pagefault** | `lat_pagefault /tmp/file` | mmap 文件后顺序访问未驻留页，触发 page fault | us = 一次 page fault 的处理延迟 |
| **bw-mmap-rd** | `bw_mmap_rd 128m open2close /tmp/file` | mmap 128MB 文件后顺序读取全部内容 | MB/s = 通过 mmap 读文件的带宽 |

---

### Disk Suite (fio)

| Workload | 命令关键参数 | 测量方式 | 结果含义 |
|----------|-------------|----------|----------|
| **seq-write** | `--rw=write --bs=1m --size=256m --direct=1 --iodepth=32` | libaio 异步提交 1MB 写请求，队列深度 32，绕过页缓存 | MB/s 顺序写带宽 |
| **seq-read** | dd 创建文件 + `--rw=read --bs=1m --size=256m --direct=1 --iodepth=32` | 同上，读取已有文件 | MB/s 顺序读带宽 |
| **rand-read-4k** | dd 创建文件 + `--rw=randread --bs=4k --iodepth=64 --runtime=10` | 4KB 随机位置读，队列深度 64，跑 10 秒 | IOPS = 每秒完成的随机读次数 |
| **rand-write-4k** | `--rw=randwrite --bs=4k --iodepth=64 --runtime=10` | 4KB 随机位置写，跑 10 秒 | IOPS = 每秒随机写次数 |
| **fsync-latency** | `--rw=write --bs=4k --ioengine=sync --fdatasync=1` | 同步写 4KB + 每次调 fdatasync 强制落盘 | us = 一次 write+fdatasync 的平均延迟 |
| **mixed-randrw** | `--rw=randrw --rwmixread=70 --bs=4k --iodepth=32` | 70% 随机读 + 30% 随机写混合 | IOPS = 读+写总 IOPS |
| **small-file-create** | shell 循环创建 2000 个小文件 + sync | 模拟 agent 生成代码文件 | files/s = 每秒创建文件数 |

---

### Network Suite (iperf3 + lmbench)

| Workload | 命令 | 测量方式 | 结果含义 |
|----------|------|----------|----------|
| **tcp-stream** | `iperf3 -c 127.0.0.1 -t 10` | 单 TCP 连接打满 loopback，持续 10 秒 | Gbps = TCP 单流吞吐 |
| **tcp-parallel** | `iperf3 -c 127.0.0.1 -t 10 -P 4` | 4 条 TCP 并行流 | Gbps = 多流总吞吐 |
| **lat-connect** | `lat_connect localhost` | 反复 connect()+close() 到本地 TCP 端口 | us = 一次 TCP 三次握手延迟 |

---

### Syscall Suite (lmbench)

| Workload | 命令 | 测量方式 | 结果含义 |
|----------|------|----------|----------|
| **lat-syscall-null** | `lat_syscall null` | 反复调用 `getppid()`（最轻量 syscall） | us = 一次内核态往返延迟 |
| **lat-syscall-read** | `lat_syscall read` | 反复调用 `read(fd, buf, 0)` | us = read syscall 延迟 |
| **lat-syscall-write** | `lat_syscall write` | 反复调用 `write(fd, buf, 0)` | us = write syscall 延迟 |
| **lat-pipe** | `lat_pipe` | 两个进程通过 pipe 互相传 1 字节 token | us = 一次 pipe 往返延迟（含 2 次上下文切换） |
| **lat-proc-fork** | `lat_proc fork` | 反复 fork() 子进程然后 exit | us = 一次 fork 延迟 |
| **lat-proc-exec** | `lat_proc exec` | fork() + exec(/bin/true) | us = 一次 fork+exec 延迟 |
| **lat-ctx** | `lat_ctx -s 0 2` | 2 个进程通过 pipe 互相传 token 强制上下文切换 | us = 一次上下文切换延迟 |
| **lat-select** | `lat_select -n 100 file` | 对 100 个 fd 调用 select() | us = 一次 select 系统调用延迟 |

---

### Lifecycle Suite (SDK API)

| Workload | 测量方式 | 结果含义 |
|----------|----------|----------|
| **create** | Go 端计时：`client.Create()` 从发请求到 sandbox ready | ms = 冷启动一个新 sandbox 的端到端延迟 |
| **snapshot** | Go 端计时：`POST /sandboxes/{id}/snapshots` | ms = 对运行中 sandbox 做一次快照的延迟 |
| **clone** | Go 端计时：snapshot + 从快照创建新 sandbox | ms = 克隆一个 sandbox 的总延迟 |

---

## 统计指标

每个 workload 跑 N 次迭代（默认 `-n 5`），框架从 samples 数组自动计算：

| 指标 | 含义 |
|------|------|
| mean | 算术平均值 |
| stddev | 标准差 |
| min / max | 最小值 / 最大值 |
| p50 | 中位数（第 50 百分位） |
| p90 | 第 90 百分位 |
| p95 | 第 95 百分位 |
| p99 | 第 99 百分位 |
| cv | 变异系数 = stddev / mean，反映测量稳定性 |

所有统计指标保存在 JSON 结果的 `stats` 字段中。
