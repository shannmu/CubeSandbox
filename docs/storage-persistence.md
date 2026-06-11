# CubeSandbox 存储与持久化能力

## 概述

CubeSandbox 的存储架构以**内存后端 + 快照持久化**为核心设计。sandbox 运行时的文件系统由 host 内存支撑，提供极高的 I/O 性能；持久化通过 snapshot 机制实现，将整个 sandbox 状态（含文件系统）写入 host 物理磁盘。

---

## 运行时存储模型

### sandbox 文件系统

sandbox 内部的文件系统（rootfs）是内存后端的 overlay 层：

```
agent 文件操作 (open/read/write)
  │
  ▼
guest 内核 VFS
  │
  ▼
virtio-blk 设备
  │
  ▼
host 内存（CoW 层，由 cubecow 管理）
```

- 所有文件 I/O 最终落在 host 内存，不经过物理磁盘
- 典型性能：顺序读写 3-4 GB/s，随机 4K IOPS 约 150K-170K
- sandbox 被 Kill 后，内存释放，**数据全部丢失**

### 何时数据真正落盘

| 场景 | 是否写物理磁盘 | 说明 |
|------|--------------|------|
| agent 常规文件操作 | 否 | 读写均在 host 内存中完成 |
| Snapshot | **是** | 整个 sandbox 的内存状态 + 文件系统持久化到 host 磁盘 |
| Template 创建 | **是** | rootfs 镜像（ext4）写入 host 存储 |
| Host 内存不足 | **是** | 内核可能将 sandbox 内存页 swap 到 host 磁盘，导致性能劣化 |

### 持久化生命周期

```
Template (磁盘)
  │
  ├─ Create ──→ Sandbox (内存)
  │               │
  │               ├─ 运行中：所有 I/O → host 内存（快，易失）
  │               │
  │               ├─ Snapshot ──→ 持久化到 host 磁盘
  │               │                 │
  │               │                 └─ 可从快照恢复或 Clone 新 sandbox
  │               │
  │               └─ Kill ──→ 内存释放，未快照的数据丢失
  │
  └─ 其他 sandbox ...
```

---

## 各层存储能力详解

### 1. CubeHypervisor — virtio-blk 完整实现

Hypervisor 层提供完整的 virtio-blk 块设备支持：

- **DiskConfig** 结构体字段：`path`、`readonly`、`direct`、`iommu`、`num_queues`、`queue_size`、`rate_limiter_config`、`pci_segment`
- 支持的 virtio 特性：`VIRTIO_BLK_F_FLUSH`、`VIRTIO_BLK_F_CONFIG_WCE`、`VIRTIO_BLK_F_BLK_SIZE`、`VIRTIO_BLK_F_TOPOLOGY`、`VIRTIO_BLK_F_RO`、`VIRTIO_BLK_F_MQ`
- 支持 vhost-user-blk 后端
- VM 配置中通过 `disks: Option<Vec<DiskConfig>>` 挂载多块磁盘

**源码位置：**
- `hypervisor/vmm/src/vm_config.rs` — DiskConfig 定义
- `hypervisor/virtio-devices/src/block.rs` — virtio-blk 设备实现
- `hypervisor/virtio-devices/src/vhost_user/blk.rs` — vhost-user-blk 后端

### 2. CubeShim — annotation 驱动的磁盘配置

CubeShim 将 Cubelet 传递的 annotation 转换为 hypervisor 的磁盘配置：

- **annotation key**：`cube.disk`（`ANNO_DISK`）
- **Disk 结构体**：`path`、`source_dir`、`fs_type`、`size`、`fs_quota`、`rate_limiter_config`
- 磁盘在 guest 内呈现为 `/dev/vdX`，挂载点为 `/run/blk-cube/vdX`
- 磁盘 ID 命名规则：`blk-cube-0`、`blk-cube-1`、...
- 支持 VFIO 磁盘直通：通过 `cube.vfio.disk` 和 `cube.vfio.disk.rm` annotation

**源码位置：**
- `CubeShim/shim/src/sandbox/disk.rs` — Disk 结构体定义
- `CubeShim/shim/src/hypervisor/config.rs` — VmConfig::add_disks() 转换逻辑
- `CubeShim/shim/src/sandbox/config.rs` — annotation 解析
- `CubeShim/shim/src/sandbox/device.rs` — VFIO 直通设备

### 3. Cubelet — 磁盘镜像分配与管理

Cubelet 负责实际的磁盘镜像分配，并将信息写入 `cube.disk` annotation：

- **存储插件**：分配 ext4 格式的磁盘镜像文件
- **cubecow**：CoW（Copy-on-Write）/ reflink 机制，实现快照和克隆的高效磁盘分配
- **StorageInfo → CubeDiskConfig 转换**：每个带 `FilePath` + `SizeLimit` 的 volume 转为一个 virtio-blk 磁盘
- **输出**：`BackendFileInfo`（含 `FilePath`、`SizeLimit`、`Type: "ext4"`、`SourcePath: "disk"`、`FSQuota`）

**annotation 配置**：
- `cube.master.system_disk_size` — 系统盘大小（GiB）

**源码位置：**
- `Cubelet/pkg/constants/const.go` — AnnotationsMountListKey 定义
- `Cubelet/services/cubebox/annotation.go` — annotation 构建逻辑
- `Cubelet/storage/local.go` — 本地存储分配
- `Cubelet/storage/plugin.go` — 存储插件配置

### 4. CubeMaster — Volume 类型

CubeMaster 的 sandbox 创建请求支持以下 Volume 类型：

| Volume 类型 | 说明 | 持久化 |
|------------|------|--------|
| EmptyDir | 临时卷，可配置 size limit 和 medium | 否（随 sandbox 销毁） |
| SandboxPath | sandbox 内部路径挂载 | 否 |
| HostDirVolumeSources | host 目录挂载（通过 virtio-fs bind mount） | 取决于 host 目录 |
| Image | 镜像卷 | 否 |

**源码位置：**
- `CubeMaster/pkg/service/sandbox/types/types.go` — CreateCubeSandboxReq、Volume 定义
- `CubeMaster/pkg/service/sandbox/hostdir_mount.go` — host 目录挂载实现

### 5. CubeAPI — 公开 API

| 字段 | 位置 | 说明 |
|------|------|------|
| `volumeMounts` | NewSandbox（创建请求） | 接受但**未实际生效**（代码中硬编码为 None） |
| `volumeMounts` | ListedSandbox / SandboxDetail（响应） | 只读，返回已挂载的 volume 信息 |
| `diskSizeMB` | ListedSandbox / SandboxDetail（响应） | 只读，系统盘大小 |

**源码位置：**
- `CubeAPI/src/models/mod.rs` — 数据模型定义
- `CubeAPI/src/services/sandboxes.rs` — create_sandbox() 实现（volumes 未传递）

### 6. Go SDK

| 方法/结构体 | 磁盘相关 |
|------------|---------|
| `CreateOptions` | **无** disk/volume 参数 |
| `SandboxInfo` | `DiskSizeMB`（只读）、`VolumeMounts`（只读） |

唯一可用的路径：通过 `CreateOptions.Metadata` 传入 `host-mount` key 实现 host 目录挂载。

**源码位置：**
- `sdk/go/models.go` — CreateOptions、SandboxInfo 定义

---

## 用户可用的存储方式

### 方式一：内存文件系统（默认）

所有 sandbox 默认使用内存后端文件系统，无需额外配置。

- 性能：顺序 3-4 GB/s，随机 4K 约 150K+ IOPS
- 容量：受 sandbox 内存配额限制
- 持久化：通过 snapshot API
- 适用场景：agent 代码执行、包安装、临时文件操作

### 方式二：Host 目录挂载（通过 metadata）

通过 `host-mount` metadata 挂载 host 目录到 sandbox 内部（virtio-fs）：

```json
{
  "metadata": {
    "host-mount": "[{\"hostPath\":\"/data/shared\",\"mountPath\":\"/mnt/data\",\"readOnly\":false}]"
  }
}
```

- 性能：取决于 host 存储设备
- 持久化：数据直接在 host 文件系统上，sandbox 销毁后仍存在
- 限制：需要 host 上的目录预先存在；安全隔离较弱

### 方式三：Snapshot 持久化

通过 API 对运行中的 sandbox 做快照：

```
POST /sandboxes/{sandbox_id}/snapshots
```

- 保存内容：整个 sandbox 的内存状态 + 文件系统
- 恢复方式：从快照创建新 sandbox（clone）
- 适用场景：保存 agent 工作进度、环境预热

---

## 当前限制

1. **SDK/API 未暴露磁盘挂载能力**：Go SDK 的 `CreateOptions` 没有 disk/volume 参数，CubeAPI 的 `volumeMounts` 字段虽然存在但未实际传递给 CubeMaster
2. **无独立持久卷**：不支持类似 Kubernetes PV/PVC 的独立持久卷，数据持久化只能通过 snapshot
3. **无块设备直通（用户层）**：virtio-blk 和 VFIO 直通在内部完整实现，但未对外开放配置接口
4. **snapshot 是全量快照**：不支持增量快照或仅文件级别的持久化

---

## 性能参考（benchmark 实测）

以下数据来自 cube-pvm-bench 在 sandbox 内的实测，反映内存后端文件系统的性能：

| 指标 | 典型值 | 说明 |
|------|--------|------|
| 顺序写 | ~3800 MB/s | fio, bs=1M, direct=1 |
| 顺序读 | ~4000 MB/s | fio, bs=1M, direct=1 |
| 随机读 4K | ~158K IOPS | fio, iodepth=64 |
| 随机写 4K | ~170K IOPS | fio, iodepth=64 |
| fsync 延迟 | ~1400 us | fio, 4K write + fdatasync |

对比参考：

| 存储类型 | 顺序读写 | 随机 4K IOPS |
|---------|---------|-------------|
| CubeSandbox 内存后端 | 3-4 GB/s | 150K-170K |
| NVMe SSD | 3-7 GB/s | 500K-1M |
| SATA SSD | ~500 MB/s | 50-100K |
| HDD | 100-200 MB/s | 100-200 |
