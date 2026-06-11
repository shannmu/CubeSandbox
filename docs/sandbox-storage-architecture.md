# Sandbox 存储架构

## Overview

CubeSandbox 的存储架构以 **NVDIMM (pmem) + virtio-blk + overlay2** 为核心。每个 sandbox 内有三类块设备：pmem0（Guest OS rootfs）、pmem1（容器镜像层）、vda（可写 CoW 层），三者通过 overlay2 组合为容器的根文件系统。所有写操作最终落在 host 内存或 host page cache 中，不直接写入物理磁盘。

## 总体架构

```
Guest VM
┌──────────────────────────────────────────────────────────────────┐
│                                                                  │
│  /dev/pmem0          /dev/pmem1           /dev/vda               │
│  (Guest OS rootfs)   (容器镜像层)          (可写 CoW 层)          │
│  ext4+DAX, ro        ext4+DAX, ro         ext4                   │
│  mount: /            mount: /pmem/xxx     mount: /run/blk-cube/  │
│                                                                  │
│  overlay2:                                                       │
│    lowerdir = /pmem/xxx     ← pmem1 (只读镜像)                   │
│    upperdir = /run/blk-cube/vda/containerd/...  ← vda (读写)     │
│    merged   = 容器根文件系统                                      │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘

Host
┌──────────────────────────────────────────────────────────────────┐
│                                                                  │
│  pmem0 后端:                                                     │
│    cube-guest-image-cpu.img → MAP_PRIVATE mmap (匿名页写入)      │
│                                                                  │
│  pmem1 后端:                                                     │
│    template ext4 镜像文件 → MAP_PRIVATE mmap (匿名页写入)        │
│                                                                  │
│  vda 后端:                                                       │
│    cubecow reflink clone 文件 → 常规文件 I/O (host page cache)   │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

## 1. pmem0 — Guest OS Rootfs

pmem0 是 sandbox 的 **Guest 操作系统根文件系统**，包含 Linux 内核所需的基础用户空间：`/bin`、`/lib`、`/etc`、`/usr` 等。

### 1.1 镜像文件

镜像路径为 `/usr/local/services/cubetoolbox/cube-image/cube-guest-image-cpu.img`，是一个预制的 ext4 文件系统镜像。所有 sandbox 共享同一个镜像文件——由于 `discard_writes=true`，每个 VM 的写入都是 CoW 隔离的。

### 1.2 Hypervisor 配置

**`CubeShim/shim/src/hypervisor/config.rs:71-96`**

CubeShim 在构建 VmConfig 时，默认添加 pmem0 设备：

```rust
impl Default for VmConfig {
    fn default() -> Self {
        let cmdline = vec![
            format!("root=/dev/pmem0"),               // 根设备
            "rootflags=dax,errors=remount-ro".into(),  // DAX 直接访问 + 只读降级
            "ro".into(),                               // 只读挂载
            "rootfstype=ext4".into(),                  // ext4 文件系统
            "net.ifnames=0".into(),                    // 经典网卡命名
            // ...
        ];

        let pmems = vec![PmemConfig {
            file: PathBuf::from(
                "/usr/local/services/cubetoolbox/cube-image/cube-guest-image-cpu.img"
            ),
            discard_writes: true,   // 写入丢弃（MAP_PRIVATE）
            ..Default::default()
        }];

        VmConfig { cmdline, pmems, /* ... */ }
    }
}
```

内核参数 `root=/dev/pmem0 rootflags=dax ro rootfstype=ext4` 使 Guest 内核在启动时直接从 pmem0 挂载根文件系统，启用 DAX（Direct Access）模式，绕过 Guest page cache 直接映射到 host 内存。

### 1.3 MAP_PRIVATE 语义

**`hypervisor/vmm/src/device_manager.rs:2793-2804`**

Hypervisor 将 pmem 后端文件映射到 Guest 物理地址空间时，根据 `discard_writes` 选择 mmap 标志：

```rust
let mmap_region = MmapRegion::build(
    Some(FileOffset::new(cloned_file, 0)),
    region_size as usize,
    PROT_READ | PROT_WRITE,
    MAP_NORESERVE
        | if pmem_cfg.discard_writes {
            MAP_PRIVATE    // 写入到匿名页，不回写文件
        } else {
            MAP_SHARED     // 写入直接回写到文件
        },
)
```

- **`MAP_PRIVATE`**：Linux 内核的 Copy-on-Write 机制。Guest 读取时直接访问文件映射页；Guest 写入时，内核分配一个匿名内存页，将修改写入该匿名页。原始文件不受影响。
- **`MAP_NORESERVE`**：不预留 swap 空间，按需分配物理页。
- **效果**：所有 sandbox 共享同一个只读镜像文件，每个 VM 的写入互不干扰，且不产生磁盘 I/O。

### 1.4 Guest 内观察

```
NAME    FSTYPE   MOUNTPOINT
pmem0   ext4     /              ← Guest OS rootfs，只读
```

`df -hT` 显示 pmem0 容量与镜像文件大小一致（通常 1-2 GiB），使用率接近 100%（预制镜像几乎已满）。

## 2. pmem1 — 容器镜像层

pmem1 是容器镜像的 **只读底层**（overlay2 lowerdir），包含用户选择的 template 所对应的 rootfs。

### 2.1 镜像文件来源

Cubelet 在准备容器时，将 template 的 rootfs 文件打包为 pmem 设备。

**`Cubelet/services/cubebox/cube_container_create.go:916-964`**

```go
func prepareImagePmems(rootfsConfig []*virtiofs.CubeRootfsInfo) oci.SpecOpts {
    return func(ctx context.Context, client oci.Client, c *containers.Container, spec *oci.Spec) error {
        for _, cfg := range rootfsConfig {
            if cfg.PmemFile == "" {
                continue
            }
            fi, _ := os.Stat(cfg.PmemFile)
            p := pmem.CubePmem{
                File:          cfg.PmemFile,     // template 的 ext4 镜像路径
                DiscardWrites: true,             // 只读 CoW
                SourceDir:     "/",              // guest 内挂载点前缀
                FsType:        "ext4",
                Size:          fi.Size(),
                ID:            generatePmemID(), // 自动生成
            }
            pmemList = append(pmemList, p)
        }
        spec.Annotations[constants.AnnotationPmem] = marshalJSON(pmemList)
        return nil
    }
}
```

每个 template 的 rootfs 镜像文件成为一个独立的 pmem 设备，通过 OCI spec annotation（`cube.pmem`）传递给 CubeShim。

### 2.2 CubeShim 装配

**`CubeShim/shim/src/sandbox/sb.rs:714-758`**

`prepare_resource()` 方法在构建 VmConfig 时，将 Cubelet 传来的 pmem 列表追加到默认的 pmem0 之后：

```rust
fn prepare_resource(&self) -> CResult<VmConfig> {
    let mut vc = VmConfig::default();       // 已含 pmem0 (guest OS image)
    vc.set_vcpus(self.conf.cpu);
    vc.set_memory(self.conf.memory);
    vc.add_nets(&self.conf.net)?;
    vc.add_disks(&self.conf.disk);          // virtio-blk 设备（vda）
    vc.add_pmems(&self.conf.pmem);          // pmem1, pmem2, ... (容器镜像层)
    // ...
    Ok(vc)
}
```

**`CubeShim/shim/src/hypervisor/config.rs:281-299`**

```rust
pub fn add_pmem(&mut self, pmem: &Pmem) -> CResult<&mut Self> {
    let pmems = self.pmems.as_mut().unwrap();
    let p = PmemConfig {
        file: PathBuf::from(&pmem.file),
        size: Some(pmem.size as u64),
        discard_writes: pmem.discard_writes,
        id: Some(pmem.id.clone()),
        ..Default::default()
    };
    pmems.push(p);
    Ok(self)
}
```

### 2.3 Guest 内观察

```
NAME    FSTYPE   MOUNTPOINT
pmem1   ext4     /pmem/xxx      ← 容器镜像只读层
```

pmem1 以 DAX 模式挂载到 `/pmem/` 下的子目录，作为 overlay2 的 lowerdir。容量和内容取决于 template 的 rootfs 镜像。

## 3. vda — 可写 CoW 层

vda 是 sandbox 的 **可写存储层**，通过 virtio-blk 设备呈现。它作为 overlay2 的 upperdir，承载容器运行时的所有文件修改。

### 3.1 cubecow 存储引擎

vda 的后端文件由 **cubecow** 存储引擎管理。cubecow 是一个基于 reflink 的 Copy-on-Write 存储引擎，运行在 host 的 XFS 文件系统上。

**`Cubelet/pkg/cubecow/doc.go`**

> cubecow is a reflink-only copy-on-write storage engine. Volumes are regular files on a reflink-capable filesystem (XFS or Btrfs) and snapshots are O(1) FICLONE-based clones of those files.

#### 3.1.1 存储布局

**`cubecow/src/engine/reflink.rs:17-47`**

cubecow 在 host 上的目录结构：

```
<root_dir>/volumes/
    +-- <vol-A>/
    |   +-- <vol-A>          ← volume 主文件（FICLONE 源）
    |   +-- <snap-1>         ← FICLONE(<vol-A>)，O(1) 克隆
    |   +-- <snap-2>         ← FICLONE(<snap-1>)
    +-- <vol-B>/
    |   +-- <vol-B>
    +-- ...
```

所有元数据从文件系统自身重建：volume 列表来自 `readdir(volumes/)`，snapshot 列表来自 `readdir(volumes/<vol>/)`，大小和时间戳来自 `stat`。**没有独立的元数据数据库**。

#### 3.1.2 FICLONE — O(1) 快照

**`cubecow/src/engine/reflink.rs:87`**

```rust
const FICLONE: libc::c_ulong = 0x40049409;  // _IOW(0x94, 9, int)
```

`FICLONE` 是 Linux 的 reflink clone ioctl。在 XFS（`reflink=1`）上，它将一个文件的所有数据块共享给新文件，仅拷贝元数据。实际的数据拷贝推迟到任一方写入时（CoW）。这使得 sandbox 的快照和克隆操作是 **O(1) 时间复杂度**，不受文件大小影响。

#### 3.1.3 命名约定

cubecow 对象的命名由上层决定，常见模式：

| 模式 | 含义 |
|------|------|
| `tpl-<snapshotID>-rootfs` | Template 的 rootfs 卷 |
| `tpl-<snapshotID>-memory` | Template 的内存快照卷 |
| `sb-<sandboxID>-rootfs-gen<N>` | Sandbox 的 rootfs 卷（带世代号） |
| `tpl-<templateID>-build-rootfs` | Template 构建时的 rootfs |

### 3.2 ext4 镜像创建

Cubelet 使用 cubecow 的 reflink clone 机制快速创建 sandbox 的 vda 后端文件。

**`Cubelet/storage/shell.go:34-66`** — 基础镜像创建

```go
func newExt4BaseRaw(ctx context.Context, path string, size uint64, uuid string) error {
    // 1. 创建稀疏文件
    runCommand("truncate", "-s", sizeStr, path)
    // 2. 格式化为 ext4（关闭 journal 以提高性能）
    runCommand("mkfs.ext4", "-O", "^has_journal", "-U", uuid, path)
    // 3. 挂载并创建必要目录
    mount(path, tmpDir)
    mkdir(emptyDirInnerSourcePath)
    mkdir("containerd")
    umount(tmpDir)
}
```

**`Cubelet/storage/shell.go:129-162`** — reflink 快速克隆

```go
func newExt4RawByReflinkCopy(ctx context.Context, base, dest string, size uint64) error {
    // O(1) 文件克隆
    runCommand("cp", "--reflink=always", base, dest)
    // 可选：扩容
    if size != 0 {
        runCommand("truncate", "-s", sizeStr, dest)
        runCommand("e2fsck", "-fy", dest)
        runCommand("resize2fs", dest)
    }
}
```

ext4 镜像禁用了 journal（`^has_journal`），因为 sandbox 的存储是临时性的，不需要崩溃恢复保护，关闭 journal 可以减少 I/O 开销。

### 3.3 CubeShim 配置

**`CubeShim/shim/src/sandbox/disk.rs`**

```rust
pub const GUEST_MOUNT_DIR_PREFIX: &str = "/run/blk-cube";

pub struct Disk {
    pub path: String,        // host 上的 cubecow 文件路径
    pub source_dir: String,  // guest 内挂载子目录
    pub fs_type: String,     // "ext4"
    pub size: u64,           // 磁盘大小
    pub fs_quota: u64,       // 文件系统配额
    // ...
}
```

**`CubeShim/shim/src/hypervisor/config.rs:268-280`**

```rust
pub fn add_disks(&mut self, disk: &[Disk]) -> &mut Self {
    let disks = self.disks.as_mut().unwrap();
    for d in disk {
        let dc = DiskConfig {
            path: Some(PathBuf::from(&d.path)),
            id: Some(format!("{}-{}", utils::BLK_DEVICE_ID_PRE, disks.len())),
            ..Default::default()
        };
        disks.push(dc);
    }
    self
}
```

磁盘 ID 命名规则：`blk-cube-0`、`blk-cube-1`、...。Guest 内呈现为 `/dev/vda`、`/dev/vdb`、...。

### 3.4 Guest 内观察

```
NAME   FSTYPE   MOUNTPOINT
vda    ext4     /run/blk-cube/vda    ← 可写层
```

vda 是 ext4 文件系统，读写挂载。容量由 Cubelet 分配（通常 1-10 GiB）。

## 4. overlay2 — 容器根文件系统

sandbox 容器的根文件系统是 **overlay2**，将 pmem1（只读镜像层）和 vda（可写层）合并为一个统一的文件系统视图。

### 4.1 overlay2 组成

```
overlay2 mount:
  lowerdir = /pmem/xxx                            ← pmem1 (容器镜像只读层)
  upperdir = /run/blk-cube/vda/containerd/...     ← vda   (可写 CoW 层)
  workdir  = /run/blk-cube/vda/containerd/.../work
  merged   = 容器根文件系统（agent 看到的 /）
```

- **lowerdir（pmem1）**：包含 template 预装的文件（Python、Node.js 运行时、系统库等）。只读，Guest 内核不会修改。
- **upperdir（vda）**：记录容器运行时的所有文件变更（新建、修改、删除）。文件删除在 upperdir 中表现为 whiteout 文件。
- **merged**：overlay2 驱动将两层合并后的视图，agent 进程看到的根文件系统。

### 4.2 读写路径

**读操作**：
1. overlay2 先查 upperdir（vda）。如果文件存在（被修改过），直接从 vda 读取。
2. 如果 upperdir 不存在，fallback 到 lowerdir（pmem1）。由于 pmem1 以 DAX 模式挂载，读取直接映射到 host 内存中的镜像文件页。

**写操作**：
1. 如果文件首次写入（来自 lowerdir），overlay2 执行 copy-up：将文件从 lowerdir 拷贝到 upperdir，然后在 upperdir 上修改。
2. 后续写入直接在 upperdir（vda）上进行。
3. vda 的写入通过 virtio-blk 传递到 host，写入 cubecow 管理的文件。由于 cubecow 文件通常足够小，host page cache 会完全缓存，**实际 I/O 落在 host 内存**。

### 4.3 Guest 内 mount 信息

```
$ mount | grep overlay
overlay on /run/containerd/io.containerd.runtime.v2.task/default/... type overlay
  (rw,relatime,lowerdir=/pmem/...,upperdir=/run/blk-cube/vda/containerd/...,
   workdir=/run/blk-cube/vda/containerd/.../work)
```

## 5. Sandbox 启动流程（存储视角）

```
Cubelet                    CubeShim                  Hypervisor
   │                          │                          │
   │ 1. cubecow reflink clone │                          │
   │    创建 vda 后端文件      │                          │
   │                          │                          │
   │ 2. prepareImagePmems     │                          │
   │    打包 pmem annotation  │                          │
   │                          │                          │
   │ ─── OCI spec ──────────→ │                          │
   │                          │                          │
   │                          │ 3. prepare_resource()    │
   │                          │    VmConfig::default()   │
   │                          │    → pmem0 (guest OS)    │
   │                          │    add_pmems()           │
   │                          │    → pmem1 (容器镜像)    │
   │                          │    add_disks()           │
   │                          │    → vda (可写层)        │
   │                          │                          │
   │                          │ ─── VmConfig ──────────→ │
   │                          │                          │
   │                          │                          │ 4. 创建 NVDIMM 设备
   │                          │                          │    pmem0: MAP_PRIVATE mmap
   │                          │                          │    pmem1: MAP_PRIVATE mmap
   │                          │                          │
   │                          │                          │ 5. 创建 virtio-blk 设备
   │                          │                          │    vda: 打开 cubecow 文件
   │                          │                          │
   │                          │                          │ 6. 启动 Guest 内核
   │                          │                          │    root=/dev/pmem0 (rootfs)
   │                          │                          │    pmem1 挂载到 /pmem/
   │                          │                          │    vda 挂载到 /run/blk-cube/
   │                          │                          │    overlay2 合并挂载
```

### 5.1 关键步骤详解

**Step 1 — cubecow reflink clone**：Cubelet 调用 `cp --reflink=always` 从 template 的基础 ext4 镜像创建 sandbox 专属的 vda 后端文件。这是 O(1) 操作，不拷贝数据块。

**Step 2 — prepareImagePmems**：Cubelet 将 template 的 rootfs 镜像文件路径写入 OCI spec 的 `cube.pmem` annotation，标记 `DiscardWrites: true`。

**Step 3 — prepare_resource**：CubeShim 组装 VmConfig。pmem0 来自默认配置（guest OS 镜像），pmem1 来自 annotation（容器镜像），vda 来自 `cube.disk` annotation（cubecow 文件）。

**Step 4 — NVDIMM 设备创建**：Hypervisor 将 pmem 文件以 `MAP_PRIVATE | MAP_NORESERVE` 映射到 Guest 物理地址空间。Guest 通过 NVDIMM 协议直接访问映射内存，支持 DAX（绕过 Guest page cache）。

**Step 5 — virtio-blk 设备创建**：Hypervisor 打开 cubecow 文件作为 vda 的后端。Guest 的块 I/O 请求通过 virtqueue 传递到 host，由 Hypervisor 执行 `pread`/`pwrite`。

**Step 6 — Guest 内核挂载**：内核根据 cmdline 参数挂载 pmem0 为根文件系统，guest-agent 启动后挂载 pmem1 和 vda，最后通过 overlay2 合并为容器的根文件系统。

## 6. I/O 路径分析

### 6.1 pmem0/pmem1 的 I/O 路径（DAX）

```
Guest read/write
  → NVDIMM 地址空间（直接内存访问，无 virtqueue）
  → Host mmap 区域
  → 读：文件映射页（共享）
  → 写：匿名内存页（MAP_PRIVATE CoW）
```

DAX 模式下，Guest 对 pmem 设备的访问 **不经过 virtqueue**，而是直接通过内存映射访问 host 的 mmap 区域。这提供了接近原生内存的读取性能。写入由于 `MAP_PRIVATE`，会触发内核的 CoW，分配匿名页。

### 6.2 vda 的 I/O 路径（virtio-blk）

```
Guest read/write
  → virtio-blk virtqueue (avail/used ring)
  → VM exit → Hypervisor 用户态
  → pread/pwrite cubecow 文件
  → Host page cache (通常命中)
  → [极少] Host 物理磁盘
```

vda 的 I/O 需要经过 virtqueue 和 VM exit/entry，开销高于 pmem 的 DAX 访问。但由于 cubecow 文件通常较小（几百 MB 到几 GB），host page cache 会完全缓存文件内容，实际的磁盘 I/O 极少发生。

### 6.3 overlay2 的 I/O 路径

| 操作 | 路径 | 备注 |
|------|------|------|
| 读未修改文件 | overlay2 → pmem1 (DAX) | 最快，直接内存访问 |
| 读已修改文件 | overlay2 → vda (virtio-blk) → page cache | 较快 |
| 首次写入 | overlay2 copy-up → vda | 需先从 pmem1 拷贝到 vda |
| 后续写入 | overlay2 → vda (virtio-blk) → page cache | 稳定 |
| 创建新文件 | overlay2 → vda (直接写入 upperdir) | 无 copy-up |

## 7. 实际观察数据

以下数据来自 `inspect_sandbox.py` 对运行中 sandbox 的实际采集。

### 7.1 块设备（lsblk -f）

```
NAME    FSTYPE   LABEL   UUID                                 MOUNTPOINT
pmem0   ext4             xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx  /
pmem1   ext4             xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx  /pmem/...
vda     ext4             xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx  /run/blk-cube/vda
```

### 7.2 磁盘空间（df -hT）

```
Filesystem     Type      Size  Used Avail Use% Mounted on
/dev/pmem0     ext4      1.5G  1.3G  100M  93% /          ← Guest OS，几乎满
/dev/pmem1     ext4      2.0G  1.8G  100M  95% /pmem/...  ← 模版镜像
/dev/vda       ext4      5.0G  200M  4.5G   5% /run/blk-cube/vda  ← 可写层
overlay        overlay   5.0G  2.0G  2.8G  42% /...       ← 合并视图
```

### 7.3 内核启动参数

```
root=/dev/pmem0 rootflags=dax,errors=remount-ro ro rootfstype=ext4 ...
```

### 7.4 PCI 设备

```
00:04.0 SCSI storage controller: Red Hat, Inc. Virtio block device  ← vda
```

pmem0/pmem1 不出现在 PCI 列表中——它们是 NVDIMM 设备，通过 ACPI NFIT 表注册，不走 PCI 总线。

## 8. 代码索引

| 组件 | 代码位置 | 职责 |
|------|----------|------|
| **cubecow engine** | `cubecow/src/engine/reflink.rs` | FICLONE reflink 存储引擎，volume/snapshot 管理 |
| **cubecow Go SDK** | `Cubelet/pkg/cubecow/doc.go` | cubecow 的 Go FFI 绑定 |
| **ext4 镜像创建** | `Cubelet/storage/shell.go` | `newExt4BaseRaw`、`newExt4RawByReflinkCopy` |
| **pmem 打包** | `Cubelet/services/cubebox/cube_container_create.go` | `prepareImagePmems` — 镜像文件 → pmem annotation |
| **VM 资源装配** | `CubeShim/shim/src/sandbox/sb.rs` | `prepare_resource` — 组装 VmConfig |
| **VmConfig 配置** | `CubeShim/shim/src/hypervisor/config.rs` | pmem/disk 配置转换，VmConfig 默认值 |
| **NVDIMM 设备创建** | `hypervisor/vmm/src/device_manager.rs` | MAP_PRIVATE/MAP_SHARED mmap，NVDIMM 注册 |
| **virtio-blk 设备** | `hypervisor/virtio-devices/src/block.rs` | virtio-blk 设备实现 |
| **Disk 结构定义** | `CubeShim/shim/src/sandbox/disk.rs` | Guest 挂载路径 `/run/blk-cube`，Disk 数据结构 |
| **Pmem 结构定义** | `CubeShim/shim/src/sandbox/pmem.rs` | Guest 挂载路径 `/pmem`，Pmem 数据结构 |
