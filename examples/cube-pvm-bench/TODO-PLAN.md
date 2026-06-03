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

- [ ] 1. 初始化项目: go.mod, Makefile, config.go, main.go 骨架
- [ ] 2. 实现 workloads.go 接口定义 + workload 注册
- [ ] 3. 实现 results.go 结果结构体与 JSON 序列化
- [ ] 4. 实现 stats.go (从 cube-bench 移植 + 扩展)
- [ ] 5. 实现 runner.go 编排逻辑 (SDK 集成)
- [ ] 6. 实现 workloads_cpu.go
- [ ] 7. 实现 workloads_memory.go
- [ ] 8. 实现 workloads_disk.go
- [ ] 9. 实现 workloads_network.go
- [ ] 10. 实现 workloads_syscall.go
- [ ] 11. 实现 workloads_lifecycle.go
- [ ] 12. 实现 compare.go 对比逻辑
- [ ] 13. 实现 theme.go (从 cube-bench 移植 + 扩展)
- [ ] 14. 实现 report.go 终端报告渲染
- [ ] 15. 实现 ui.go Bubbletea TUI
- [ ] 16. 编写 README.md
- [ ] 17. 验证: --dry-run 模式编译运行通过

---

## 验证步骤

1. `cd examples/cube-pvm-bench && make` — 编译成功
2. `./bin/cube-pvm-bench --dry-run -n 2 --no-tui` — 合成数据运行，产出报告
3. `./bin/cube-pvm-bench -s cpu -n 3 -o kvm.json -l kvm-native` — 对真实沙箱运行 CPU 套件
4. `./bin/cube-pvm-bench -s cpu -n 3 -o pvm.json -l pvm -b kvm.json` — PVM vs 基线对比
5. JSON 输出可被 `jq` 正确解析
