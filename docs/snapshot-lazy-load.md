# CubeSandbox 快照 Lazy Load 实现详解

## 1. 概述

CubeSandbox 的快照 Lazy Load 机制使 MicroVM 能在毫秒级从模板快照恢复，同时只消耗实际访问到的内存量。整个方案基于三层技术栈协同：

| 层级 | 组件 | 核心机制 |
|------|------|----------|
| 存储层 | cubecow (reflink) | xfs `FICLONE` ioctl 实现 O(1) 文件克隆 |
| VMM 层 | hypervisor/vmm | `MAP_PRIVATE` mmap 实现内存按需加载 |
| 内核层 | Linux page fault handler | 内核级 CoW 缺页处理，无需用户态介入 |

**关键设计决策**：不使用 `userfaultfd`，完全依赖 Linux 内核原生的 `MAP_PRIVATE` file-backed mmap 缺页处理。这避免了用户态 page fault handler 的上下文切换开销和实现复杂度。

---

## 2. 整体数据流

```
┌──────────────────────────────────────────────────────────────────────┐
│                        模板快照创建 (一次性)                          │
│                                                                      │
│  冷启动 VM ──→ wait ready ──→ pause ──→ dump 内存到 memory-ranges     │
│                                              │                       │
│                                              ▼                       │
│                                 cubecow reflink clone (FICLONE)      │
│                                        → 模板内存文件                 │
└──────────────────────────────────────────────────────────────────────┘
                                         │
                                         │ 每次创建 Sandbox
                                         ▼
┌──────────────────────────────────────────────────────────────────────┐
│                      Sandbox 恢复 (每次启动)                          │
│                                                                      │
│  cubecow reflink clone ──→ sandbox 独立内存文件 (O(1))               │
│                                   │                                  │
│                                   ▼                                  │
│              VMM open(文件, O_RDONLY)                                 │
│                                   │                                  │
│                                   ▼                                  │
│      mmap(MAP_PRIVATE | MAP_NORESERVE) → 虚拟地址空间 (零物理内存)    │
│                                   │                                  │
│                                   ▼                                  │
│            恢复 CPU / 设备 / vGIC 状态 → resume vCPU                 │
└──────────────────────────────────────────────────────────────────────┘
                                         │
                                         │ vCPU 执行
                                         ▼
┌──────────────────────────────────────────────────────────────────────┐
│                      运行时缺页处理 (持续)                            │
│                                                                      │
│  vCPU 访问 guest 物理地址                                             │
│        │                                                             │
│        ▼                                                             │
│  host 虚拟页无 PTE → CPU page fault                                  │
│        │                                                             │
│        ├─ 读访问: 内核从快照文件加载页到 page cache → 建立只读映射     │
│        └─ 写访问: 内核 CoW → 分配匿名页 → 拷贝 → 建立私有可写映射    │
│                                                                      │
│  从未访问的页 → 始终不消耗物理内存                                    │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 3. 阶段一：模板快照创建

### 3.1 创建流程入口

快照创建由 CubeShim 的 `Snapshot` 结构驱动：

```rust
// CubeShim/shim/src/snapshot/mod.rs:111
fn do_snapshot(&mut self) -> CResult<()> {
    self.launch_vmm()?;       // 启动 VMM 进程
    self.boot_vm()?;          // 冷启动 VM
    self.wait_vm_ready()?;    // 等待 guest agent 就绪
    self.create_snapshot()?;  // 暂停 VM 并创建快照
    self.store_metadata()     // 保存元数据 (metadata.json)
}
```

### 3.2 VMM 侧快照逻辑

VM 快照在 `Vm::snapshot()` 中完成，需要 VM 处于 Paused 状态：

```rust
// hypervisor/vmm/src/vm.rs:2662
fn snapshot(&mut self) -> std::result::Result<Snapshot, MigratableError> {
    let current_state = self.get_state().unwrap();
    if current_state != VmState::Paused {
        return Err(MigratableError::Snapshot(anyhow!(
            "Trying to snapshot while VM is running"
        )));
    }

    // ...

    // 构建快照状态树
    let mut vm_snapshot = Snapshot::new_from_state(VM_SNAPSHOT_ID, &vm_snapshot_state)?;
    vm_snapshot.add_snapshot(self.cpu_manager.lock().unwrap().snapshot()?);
    vm_snapshot.add_snapshot(self.memory_manager.lock().unwrap().snapshot()?);
    vm_snapshot.add_snapshot(self.device_manager.lock().unwrap().snapshot()?);

    Ok(vm_snapshot)
}
```

快照状态树由 `Snapshot` 结构表示（定义在 `hypervisor/vm-migration/src/lib.rs:187`），采用树形结构组织：

```
vm-snapshot
├── cpu-manager       (CPU 寄存器状态)
├── memory-manager    (内存区域表 + 内存内容)
├── device-manager    (所有设备状态)
└── vgic (aarch64)    (中断控制器状态)
```

### 3.3 内存快照写入

`MemoryManager` 实现了 `Snapshottable` trait，`snapshot()` 方法记录内存区域元数据：

```rust
// hypervisor/vmm/src/memory_manager.rs:2910
fn snapshot(&mut self) -> result::Result<Snapshot, MigratableError> {
    let mut memory_manager_snapshot = Snapshot::new(MEMORY_MANAGER_SNAPSHOT_ID);
    let memory_ranges = self.memory_range_table(true)?;
    self.snapshot_memory_ranges = memory_ranges;  // 保存供后续 send() 使用

    memory_manager_snapshot.add_data_section(SnapshotDataSection::new_from_state(
        MEMORY_MANAGER_SNAPSHOT_ID,
        &self.snapshot_data(),
    )?);
    Ok(memory_manager_snapshot)
}
```

快照元数据结构 `MemoryManagerSnapshotData` 包含：

```rust
// hypervisor/vmm/src/memory_manager.rs:2891
pub struct MemoryManagerSnapshotData {
    memory_ranges: MemoryRangeTable,           // GPA + length 表
    guest_ram_mappings: Vec<GuestRamMapping>,   // zone/slot 映射关系
    start_of_device_area: u64,
    boot_ram: u64,
    current_ram: u64,
    arch_mem_regions: Vec<ArchMemRegion>,
    hotplug_slots: Vec<HotPlugState>,
    next_memory_slot: u32,
    selected_slot: usize,
    next_hotplug_slot: usize,
}
```

快照支持三种类型（定义在 `hypervisor/vm-migration/src/lib.rs:110`）：

```rust
pub enum SnapshotType {
    Full,           // 完整内存快照
    Incremental,    // pagemap_anon 增量快照（仅 CoW 匿名页）
    SoftDirty,      // soft-dirty 增量快照（仅自上次清除以来写入的页）
}
```

### 3.4 快照文件输出

快照目录结构：
```
<snapshot-dir>/
├── config.json           ← VM 配置 (VmConfig)
├── state.json            ← 设备/CPU/内存元数据状态树
├── memory-ranges         ← guest 内存内容 (或在 memory_vol_url 指向的外部文件中)
└── metadata.json         ← 模板兼容性信息 (kernel/image/CPU/memory/disk)
```

快照文件名常量：

```rust
// hypervisor/vmm/src/memory_manager.rs:67
const SNAPSHOT_FILENAME: &str = "memory-ranges";
```

### 3.5 存储层：cubecow reflink 克隆

cubecow 使用 xfs 的 `FICLONE` ioctl 对模板内存文件做 O(1) 的 CoW 文件克隆。`FICLONE` 操作是瞬时的——底层文件系统只复制元数据（block map），两个文件共享物理数据块直到其中一个被修改。

```rust
// cubecow/src/engine/reflink.rs:87
const FICLONE: libc::c_ulong = 0x40049409;

// cubecow/src/engine/reflink.rs:638
fn create_snapshot(
    &self,
    source_name: &str,
    snapshot_name: &str,
    _activate: bool,
) -> CubecowResult<Snapshot> {
    // ...
    // 解析 source: 可以是 volume 或另一个 snapshot
    let (source_path, ultimate_origin) = {
        let idx = self.name_index.read().expect("...");
        match idx.get(source_name) {
            Some(NameKind::Volume) => (
                self.vol_main_file(source_name),
                source_name.to_string(),
            ),
            Some(NameKind::Snapshot { origin_volume }) => (
                self.snap_file(origin_volume, source_name),
                origin_volume.clone(),
            ),
            None => { return Err(CubecowError::NotFound(...)); }
        }
    };

    // 核心操作：FICLONE ioctl 实现 O(1) 文件克隆
    let src_file = File::open(&source_path)?;
    ficlone(&src_file, &dst)?;  // cubecow/src/engine/reflink.rs:708
    // ...
}
```

Cubelet 层通过 `CommitTemplateMemory` 调用 cubecow 创建模板内存克隆：

```go
// Cubelet/storage/cubecow_volume_manager.go:228
func (m *CowVolumeManager) CommitTemplateMemory(
    ctx context.Context, sourceName, templateID string, sizeBytes uint64,
) (*cowVolume, error) {
    snapshotName := cowTemplateMemoryName(templateID)
    devPath, err := m.engine.CreateSnapshot(sourceName, snapshotName, true)
    // ...
}
```

---

## 4. 阶段二：快照恢复（Lazy Load 核心）

### 4.1 Sandbox 启动流程

当创建新 Sandbox 时，CubeShim 判断是否使用快照恢复：

```rust
// CubeShim/shim/src/sandbox/sb.rs:781
async fn start_vm(&mut self) -> CResult<bool> {
    {
        let mut ch = self.ch.as_mut().unwrap().lock().await;
        ch.launch_vmm().await?;
    }
    let mut snapshot = false;

    if self.by_snapshot() {                    // 检查是否启用快照恢复
        match self.restore_vm().await {
            Ok(_) => {
                snapshot = true;
                // ...
            }
            Err(e) => {
                errf!(self.log, "restore vm failed:{}", e);
                // ...
            }
        }
    }

    if !snapshot {
        self.boot_vm().await?;                // 回退到冷启动
    }
    // ...
}
```

快照恢复的判断条件 (`by_snapshot()`)：

```rust
// CubeShim/shim/src/sandbox/sb.rs:764
fn by_snapshot(&self) -> bool {
    let anno = self.spec.annotations().as_ref().unwrap();
    if anno.contains_key(config::ANNO_SNAPSHOT_DISABLE) {
        return false;                          // annotation 显式禁用
    }
    if let Some(proc) = self.spec.process() {
        if proc.selinux_label().is_some() && !proc.selinux_label().clone().unwrap().is_empty() {
            return false;                      // SELinux 场景不支持
        }
    }
    if !enable_snapshot() {
        return false;                          // 全局开关关闭
    }
    !self.conf.app_snapshot_create             // 非创建模式
}
```

### 4.2 restore_vm() 流程

`restore_vm()` 加载快照元数据、验证兼容性、构建 `RestoreConfig`：

```rust
// CubeShim/shim/src/sandbox/sb.rs:838
async fn restore_vm(&mut self) -> CResult<()> {
    // 1. 加载模板快照元数据
    let ss_file = SnapshotInfo::load(
        self.conf.snapshot_base.as_str(),
        self.conf.vm_res.cpu,
        self.conf.vm_res.snap_memory,
    )?;

    // 2. 构建当前环境的 SnapshotInfo 进行兼容性校验
    let mut ss_req = SnapshotInfo::new(self.conf.vm_res.cpu, self.conf.vm_res.snap_memory);
    ss_req.set_image_version()?;
    ss_req.set_kernel_version(self.conf.kernel.as_str())?;
    ss_req.set_disks(&self.conf.disk);
    ss_req.set_pmems(&align_pmem);

    // 3. 校验模板与当前环境的兼容性
    ss_file.eq(&ss_req)
        .map_err(|e| format!("snapshot metadata not match:{}", e))?;

    // 4. 构建 RestoreConfig
    let config = RestoreConfig {
        source_url: PathBuf::from(snapshot),   // 快照目录路径
        fs: Some(fss),
        net: Some(nets),
        disks: Some(disks),
        pmem: Some(pmems),
        vsock: Some(vsock),
        memory_vol_url: restore_memory_vol_url, // cubecow 内存文件 URL
        ..Default::default()
    };

    // 5. 发起恢复请求
    ch.restore_vm(config).await?;
}
```

快照元数据结构（`SnapshotInfo`）记录了兼容性验证所需的信息：

```rust
// CubeShim/shim/src/hypervisor/snapshot.rs:28
pub struct SnapshotInfo {
    pub kernel_version: String,
    pub image_version: String,
    pub ch_version: String,
    pub vm_res: VmRes,                        // cpu, memory, disks, pmems
    pub memory_vol_url: Option<String>,       // cubecow 内存卷 URL
    pub app_snapshot_container_id: Option<String>,
}
```

### 4.3 VMM 恢复入口

VMM 侧恢复入口在 `vm_restore()`：

```rust
// hypervisor/vmm/src/lib.rs:653
fn vm_restore(&mut self, restore_cfg: RestoreConfig) -> result::Result<(), VmError> {
    // 1. 读取 VM 配置和快照状态
    let source_url = restore_cfg.source_url.as_path().to_str().unwrap();
    let vm_config = recv_vm_config(source_url)?;        // 读 config.json
    let snapshot = recv_vm_state(source_url)?;           // 读 state.json

    // 2. 从快照创建 VM（包含内存 lazy load 的核心逻辑）
    let mut vm = Vm::new_from_snapshot(
        &snapshot,
        vm_config.clone(),
        exit_evt,
        reset_evt,
        Some(source_url),
        restore_cfg.prefault,                           // 通常为 false
        &seccomp_action,
        self.hypervisor.clone(),
        activate_evt,
        sandbox_id,
        vcpu_started,
        restore_cfg.memory_vol_url.as_deref(),
    )?;

    // 3. 恢复 CPU、设备、中断控制器状态
    vm.restore(snapshot)?;

    // 4. 恢复 vCPU 执行
    vm.resume()?;
}
```

### 4.4 Vm::new_from_snapshot() — 创建 VM 并恢复内存

```rust
// hypervisor/vmm/src/vm.rs:821
pub fn new_from_snapshot(
    snapshot: &Snapshot,
    vm_config: Arc<Mutex<VmConfig>>,
    // ...
    source_url: Option<&str>,
    prefault: bool,
    memory_vol_url: Option<&str>,
) -> Result<Self> {
    let vm = Self::create_hypervisor_vm(&hypervisor)?;

    // 关键：从快照恢复内存
    let memory_manager = MemoryManager::new_from_snapshot(
        memory_manager_snapshot,
        vm.clone(),
        &vm_config.lock().unwrap().memory.clone(),
        source_url,
        prefault,                     // 是否预加载（通常为 false）
        phys_bits,
        memory_vol_url,               // cubecow 内存卷 URL
    )?;

    Vm::new_from_memory_manager(vm_config, memory_manager, vm, ...)
}
```

### 4.5 MemoryManager::new_from_snapshot() — Lazy Load 核心

这是整个 lazy load 最关键的函数，决定走 fast restore（lazy）还是 slow restore（全量拷贝）：

```rust
// hypervisor/vmm/src/memory_manager.rs:1305
pub fn new_from_snapshot(
    snapshot: &Snapshot,
    vm: Arc<dyn hypervisor::Vm>,
    config: &MemoryConfig,
    source_url: Option<&str>,
    prefault: bool,
    phys_bits: u8,
    memory_vol_url: Option<&str>,
) -> Result<Arc<Mutex<MemoryManager>>, Error> {
    if let Some(source_url) = source_url {
        // 1. 解析快照内存文件路径
        //    优先使用 memory_vol_url（cubecow 管理的 reflink 克隆文件）
        //    否则使用 snapshot_url/memory-ranges
        let memory_file_target =
            MemorySnapshotFile::from_snapshot_url(source_url, memory_vol_url)?;

        // 2. 判断是否支持 fast restore
        let fast_restore = Self::support_fast_restore_check(config);
        let memory_file = if fast_restore {
            info!("restore non-shared map, speed up restore by share map memory file");
            Some(memory_file_target.open_read()?)  // 以只读方式打开快照文件
        } else {
            None
        };

        // 3. 反序列化内存区域元数据
        let mem_snapshot: MemoryManagerSnapshotData = snapshot
            .to_state(MEMORY_MANAGER_SNAPSHOT_ID)?;

        // 4. 创建 MemoryManager（内部会调用 create_ram_region）
        let mm = MemoryManager::new(
            vm, config, Some(prefault), phys_bits,
            Some(&mem_snapshot),
            None,
            memory_file,      // fast restore 时传入文件句柄
        )?;

        // 5. 如果不支持 fast restore，回退到全量拷贝
        if !fast_restore {
            info!("restore shared map, fall back to slow restore");
            mm.lock().unwrap()
                .fill_saved_regions(memory_file_target.path, mem_snapshot.memory_ranges)?;
        }

        Ok(mm)
    } else {
        Err(Error::RestoreMissingSourceUrl)
    }
}
```

**Fast Restore 判断条件**：

```rust
// hypervisor/vmm/src/memory_manager.rs:1362
fn support_fast_restore_check(config: &MemoryConfig) -> bool {
    !config.exist_shared() && !config.has_hotplug_virtio_mem()
}
```

只有满足以下两个条件才走 fast restore（lazy load）：
- 内存不是 shared 模式（`MAP_SHARED`）
- 没有 virtio-mem 热插拔

### 4.6 快照文件路径解析

`MemorySnapshotFile` 负责解析快照内存文件的路径，支持两种来源：

```rust
// hypervisor/vmm/src/memory_manager.rs:69
struct MemorySnapshotFile {
    path: PathBuf,
    external: bool,        // 是否来自外部存储（cubecow）
}

impl MemorySnapshotFile {
    fn from_snapshot_url(
        snapshot_url: &str,
        memory_vol_url: Option<&str>,
    ) -> result::Result<Self, MigratableError> {
        if let Some(memory_vol_url) = memory_vol_url {
            // 优先使用 cubecow 管理的外部内存卷
            Ok(Self {
                path: memory_blob_to_path(memory_vol_url)?,
                external: true,
            })
        } else {
            // 使用快照目录内的 memory-ranges 文件
            let mut path = url_to_path(snapshot_url)?;
            path.push(SNAPSHOT_FILENAME);  // "memory-ranges"
            Ok(Self {
                path,
                external: false,
            })
        }
    }

    fn open_read(&self) -> io::Result<File> {
        OpenOptions::new().read(true).open(&self.path)
    }
}
```

### 4.7 内存区域恢复

`restore_memory_regions_and_zones()` 为每个 guest 内存区域调用 `create_ram_region()`：

```rust
// hypervisor/vmm/src/memory_manager.rs:679
fn restore_memory_regions_and_zones(
    guest_ram_mappings: &[GuestRamMapping],
    saved_regions: &MemoryRangeTable,
    zones_config: &[MemoryZoneConfig],
    prefault: Option<bool>,
    mut existing_memory_files: HashMap<u32, File>,
    saved_file: Option<File>,       // fast restore 时为 Some(快照文件句柄)
    thp: bool,
) -> Result<(Vec<Arc<GuestRegionMmap>>, MemoryZones), Error> {
    for guest_ram_mapping in guest_ram_mappings {
        for zone_config in zones_config {
            if guest_ram_mapping.zone_id == zone_config.id {
                // 查找该区域在快照文件中的偏移量
                let mut file = None;
                let mut offset = 0;
                if let Some(ref f) = saved_file {
                    for range in saved_regions.regions() {
                        if guest_ram_mapping.gpa == range.gpa
                            && guest_ram_mapping.size == range.length
                        {
                            file = Some(f.try_clone()?);  // 克隆文件句柄
                            break;
                        }
                        offset += range.length;  // 累计偏移
                    }
                }

                // 创建 mmap 区域
                let region = MemoryManager::create_ram_region(
                    &zone_config.file,
                    guest_ram_mapping.file_offset,
                    GuestAddress(guest_ram_mapping.gpa),
                    guest_ram_mapping.size as usize,
                    prefault.unwrap_or(zone_config.prefault),
                    zone_config.shared,
                    zone_config.hugepages,
                    zone_config.hugepage_size,
                    zone_config.host_numa_node,
                    existing_memory_files.remove(&guest_ram_mapping.slot),
                    file,          // snap_file: fast restore 时为 Some
                    offset,        // snap_offset: 该区域在文件中的偏移
                    thp,
                )?;
                // ...
            }
        }
    }
}
```

### 4.8 create_ram_region() — mmap 建立

**这是 lazy load 最底层的实现**。当传入 `snap_file` 时，使用 `MAP_PRIVATE` 将快照文件映射为 guest 内存：

```rust
// hypervisor/vmm/src/memory_manager.rs:1474
pub fn create_ram_region(
    backing_file: &Option<PathBuf>,
    file_offset: u64,
    start_addr: GuestAddress,
    size: usize,
    prefault: bool,
    shared: bool,
    hugepages: bool,
    hugepage_size: Option<u64>,
    host_numa_node: Option<u32>,
    existing_memory_file: Option<File>,
    snap_file: Option<File>,        // 快照文件句柄
    snap_offset: u64,               // 该区域在快照文件中的偏移
    thp: bool,
) -> Result<Arc<GuestRegionMmap>, Error> {
    let mut mmap_flags = libc::MAP_NORESERVE;  // 不预留 swap 空间

    // ========== 关键：根据场景选择 mmap 模式 ==========
    let fo = if let Some(f) = snap_file {
        // ★ Fast Restore 路径：MAP_PRIVATE 映射快照文件
        mmap_flags |= libc::MAP_PRIVATE;
        Some(FileOffset::new(f, snap_offset))
    } else if let Some(f) = existing_memory_file {
        // 已有文件（如 shared memory）：MAP_SHARED
        mmap_flags |= libc::MAP_SHARED;
        Some(FileOffset::new(f, file_offset))
    } else if let Some(backing_file) = backing_file {
        // 普通 backing file
        if shared { mmap_flags |= libc::MAP_SHARED; }
        else { mmap_flags |= libc::MAP_PRIVATE; }
        Some(Self::open_backing_file(backing_file, file_offset, size)?)
    } else if shared || hugepages {
        // 匿名 shared/hugepages
        mmap_flags |= libc::MAP_SHARED;
        Some(Self::create_anonymous_file(size, hugepages, hugepage_size)?)
    } else {
        // 匿名私有映射
        mmap_flags |= libc::MAP_PRIVATE | libc::MAP_ANONYMOUS;
        None
    };

    // prefault=true 时添加 MAP_POPULATE，强制预加载所有页面
    // 默认为 false，即页面按需加载
    if prefault {
        mmap_flags |= libc::MAP_POPULATE;
    }

    // ========== 创建 mmap 区域 ==========
    let region = GuestRegionMmap::new(
        MmapRegion::build(
            fo,
            size,
            libc::PROT_READ | libc::PROT_WRITE,
            mmap_flags,
        )?,
        start_addr,
    )?;

    // THP (Transparent Huge Pages) 优化
    if region.file_offset().is_none() && thp {
        unsafe { libc::madvise(region.as_ptr() as _, size, libc::MADV_HUGEPAGE) };
    }

    Ok(Arc::new(region))
}
```

**mmap 标志位组合分析**：

| 标志 | 值 | 作用 |
|------|----|------|
| `MAP_NORESERVE` | 始终设置 | 不预留 swap，允许 memory overcommit |
| `MAP_PRIVATE` | fast restore | CoW 语义：写时分配私有匿名页 |
| `MAP_POPULATE` | prefault=true | 预加载所有页面（**禁用** lazy load） |

默认情况下 `prefault=false`，所以 `MAP_POPULATE` **不会** 被设置。这意味着 mmap 建立后：

- **不分配任何物理内存**
- **不从文件读取任何数据**
- **仅建立虚拟地址空间到文件的映射关系**

### 4.9 Slow Restore 回退路径

当内存为 shared 模式时，无法使用 `MAP_PRIVATE`，回退到全量读取：

```rust
// hypervisor/vmm/src/memory_manager.rs:754
fn fill_saved_regions(
    &mut self,
    file_path: PathBuf,
    saved_regions: MemoryRangeTable,
) -> Result<(), Error> {
    let mut memory_file = OpenOptions::new()
        .read(true)
        .open(file_path)?;

    let guest_memory = self.guest_memory.memory();
    for range in saved_regions.regions() {
        let mut offset: u64 = 0;
        loop {
            // 从文件中顺序读取内存内容到 guest 内存
            let bytes_read = guest_memory.read_volatile_from(
                GuestAddress(range.gpa + offset),
                &mut memory_file,
                (range.length - offset) as usize,
            )?;
            offset += bytes_read as u64;
            if offset == range.length { break; }
        }
    }
    Ok(())
}
```

---

## 5. 阶段三：运行时缺页处理

### 5.1 内核级 Page Fault 流程

CubeSandbox **不使用 userfaultfd**，缺页处理完全在 Linux 内核中完成，无需用户态 handler。流程如下：

```
vCPU 执行 guest 指令
        │
        ▼
访问某 guest 物理地址 (GPA)
        │
        ▼
GPA → HVA (Host Virtual Address) 转换
        │
        ▼
该 HVA 在 host 页表中无 PTE 映射
        │
        ▼
CPU 触发 page fault (同步异常)
        │
        ▼
Linux 内核 page fault handler 处理
        │
        ├─── MAP_PRIVATE file-backed 区域？
        │          │
        │          ▼
        │    ┌─ 读访问 ───────────────────────────────────┐
        │    │  1. 检查 page cache 是否有该页               │
        │    │  2. 若无，从快照文件读取 4KB 到 page cache    │
        │    │  3. 建立 PTE 指向 page cache 中的页 (只读)   │
        │    │  4. 该页与所有共享同一文件偏移的进程共享       │
        │    └───────────────────────────────────────────────┘
        │
        │    ┌─ 写访问 ───────────────────────────────────┐
        │    │  1. 检查 page cache 是否有该页               │
        │    │  2. 若无，从快照文件读取 4KB 到 page cache    │
        │    │  3. 分配新的匿名物理页                       │
        │    │  4. 拷贝 page cache 页内容到新匿名页         │
        │    │  5. 建立 PTE 指向新匿名页 (可写)             │
        │    │  6. 新匿名页为该进程私有，与文件脱钩          │
        │    └───────────────────────────────────────────────┘
        │
        └─── 从未被访问的页 → 不消耗物理内存，不产生 I/O
```

### 5.2 内存效率分析

| 场景 | 物理内存消耗 |
|------|-------------|
| 2GB VM 恢复后从未访问任何页 | ≈ 0 MB (仅 page table) |
| 2GB VM 只读访问 200MB | ≈ 200 MB (page cache, 可回收) |
| 2GB VM 写入 200MB | ≈ 200 MB (匿名页, 不可回收) |
| 2GB VM 读 1GB + 写 200MB | ≈ 200 MB 匿名 + page cache |

关键优势：
- **零拷贝恢复**：mmap 建立时无内存分配和 I/O
- **按需加载**：只有访问到的页才会被加载
- **page cache 共享**：多个 sandbox 从同一模板恢复时，读过的页在 page cache 中共享
- **overcommit 友好**：`MAP_NORESERVE` 允许 host 超额分配内存

---

## 6. 增量快照：识别脏页

恢复运行后需要做下一次快照时，需要精确识别哪些页被 guest 修改过，以最小化快照大小。CubeSandbox 提供两种机制。

### 6.1 pagemap_anon — 识别 CoW 匿名页

通过读取 Linux procfs 精确识别 `MAP_PRIVATE` 映射中被 CoW 产生的匿名页：

```rust
// hypervisor/vmm/src/pagemap_anon.rs:119
pub fn get_anon_pages(host_addr: u64, length: u64) -> Result<Vec<bool>> {
    let num_pages = length.div_ceil(PAGE_SIZE) as usize;
    let start_page = host_addr / PAGE_SIZE;

    // 1. 打开 /proc/self/pagemap 和 /proc/kpageflags
    let mut pagemap_file = File::open("/proc/self/pagemap")?;
    let mut kpageflags_file = File::open("/proc/kpageflags")?;

    // 2. 批量读取 pagemap 条目
    let pagemap_offset = start_page * PAGEMAP_ENTRY_SIZE;
    pagemap_file.seek(SeekFrom::Start(pagemap_offset))?;
    let mut pagemap_buf = vec![0u8; num_pages * PAGEMAP_ENTRY_SIZE as usize];
    pagemap_file.read_exact(&mut pagemap_buf)?;

    let mut result = vec![false; num_pages];
    for (i, item) in result.iter_mut().enumerate().take(num_pages) {
        let entry = u64::from_ne_bytes(/* ... */);

        let present = (entry & PAGEMAP_PRESENT_BIT) != 0;   // bit 63
        let swapped = (entry & PAGEMAP_SWAPPED_BIT) != 0;   // bit 62

        // 被 swap 出的匿名页也必须保存
        if swapped {
            *item = true;
            continue;
        }
        if !present { continue; }

        // 3. 通过 PFN 查 kpageflags，检查 KPF_ANON (bit 12)
        let pfn = entry & PAGEMAP_PFN_MASK;
        let kpageflags_offset = pfn * KPAGEFLAGS_ENTRY_SIZE;
        kpageflags_file.seek(SeekFrom::Start(kpageflags_offset))?;
        let mut kpageflags_buf = [0u8; KPAGEFLAGS_ENTRY_SIZE as usize];
        kpageflags_file.read_exact(&mut kpageflags_buf)?;

        let flags = u64::from_ne_bytes(kpageflags_buf);
        if (flags & KPF_ANON) != 0 {        // KPF_ANON = 1 << 12
            *item = true;                    // 这是 CoW 产生的匿名页
        }
    }
    Ok(result)
}
```

**pagemap_anon 工作原理**：

```
/proc/self/pagemap (每 8 字节一个页条目):
┌───────────────────────────────────┐
│ bit 63: Present                   │
│ bit 62: Swapped                   │
│ bit 55: Soft-dirty                │
│ bit 0-54: PFN (Physical Frame #) │
└───────────────────────────────────┘
                │ PFN
                ▼
/proc/kpageflags (PFN 索引, 每 8 字节):
┌───────────────────────────────────┐
│ bit 12: KPF_ANON (匿名页)         │  ← 判断是否 CoW 产生
│ ...                               │
└───────────────────────────────────┘
```

`filter_memory_ranges_by_pagemap_anon()` 对整个内存区域进行过滤：

```rust
// hypervisor/vmm/src/pagemap_anon.rs:231
pub fn filter_memory_ranges_by_pagemap_anon<B: Bitmap + 'static>(
    guest_memory: &GuestMemoryMmap<B>,
    ranges: &MemoryRangeTable,
) -> Result<(MemoryRangeTable, PagemapAnonStats)> {
    let mut filtered_ranges = MemoryRangeTable::default();
    let mut stats = PagemapAnonStats::default();

    for range in ranges.regions() {
        let gpa = range.gpa;
        let length = range.length;
        stats.total_bytes += length;
        stats.total_pages += length.div_ceil(PAGE_SIZE);

        // 获取该区域的匿名页 bitmap
        // 只将匿名页对应的子区域加入 filtered_ranges
        // ...
    }

    Ok((filtered_ranges, stats))
}
```

增量快照写入只保存 anonymous 页：

```rust
// hypervisor/vmm/src/memory_manager.rs:2298
fn send_pagemap_anon_memory(
    &self,
    destination_url: &str,
    memory_vol_url: &Option<String>,
) -> result::Result<(), MigratableError> {
    let guest_memory = self.guest_memory.memory();

    // 使用 pagemap + kpageflags 过滤，只保留匿名页
    let (filtered_ranges, stats) =
        filter_memory_ranges_by_pagemap_anon(&guest_memory, &self.snapshot_memory_ranges)?;

    info!(
        "PagemapAnon snapshot: total={} bytes ({} pages), anon={} bytes ({} pages), savings={:.1}%",
        stats.total_bytes, stats.total_pages,
        stats.saved_bytes, stats.anon_pages,
        stats.savings_percentage()
    );

    // 打开已有的 base 快照文件（reflink 克隆副本）
    let memory_file = memory_file_target.open_read_write()?;

    // 只将匿名页覆写到 base 文件的对应偏移位置
    for range in filtered_ranges.regions() {
        let file_off = Self::calculate_file_offset_for_gpa(
            range.gpa, range.length, &gpa_to_file_offset,
        )?;
        self.save_range_to_file(&memory_file, range, file_off)?;
    }
}
```

### 6.2 soft-dirty — 更精细的增量追踪

soft-dirty 机制追踪自上次清除以来**被写入**的页，配合 pagemap_anon 取交集得到最小变更集：

```
变更集 = pagemap_anon ∩ soft_dirty
       = (CoW 匿名页) ∩ (自上次快照以来写入的页)
```

**生命周期**：

```rust
// hypervisor/vmm/src/soft_dirty.rs:133
// 探测内核是否支持 CONFIG_MEM_SOFT_DIRTY=y
pub fn probe_soft_dirty_support() -> bool {
    match clear_soft_dirty() {
        Ok(()) => true,
        Err(_) => false,
    }
}

// hypervisor/vmm/src/soft_dirty.rs:150
// 向 /proc/self/clear_refs 写入 "4" 以清除所有 PTE 的 soft-dirty 位
// 并重新启动追踪
pub fn clear_soft_dirty() -> Result<()> {
    let mut f = OpenOptions::new()
        .write(true)
        .open("/proc/self/clear_refs")?;
    f.write_all(CLEAR_REFS_SOFT_DIRTY)?;  // b"4\n"
    Ok(())
}

// hypervisor/vmm/src/soft_dirty.rs:193
// 读取 pagemap bit 55 判断页面是否在窗口期内被写入
pub fn get_soft_dirty_pages(host_addr: u64, length: u64) -> Result<Vec<bool>> {
    // ...
    for (i, item) in result.iter_mut().enumerate() {
        let entry = /* read pagemap entry */;
        let swapped = (entry & PAGEMAP_SWAPPED_BIT) != 0;   // bit 62
        if swapped { *item = true; continue; }

        let present = (entry & PAGEMAP_PRESENT_BIT) != 0;   // bit 63
        if !present { continue; }

        // bit 55: 页面自上次 clear_refs(4) 以来被写入过
        if (entry & PAGEMAP_SOFT_DIRTY_BIT) != 0 {
            *item = true;
        }
    }
    Ok(result)
}
```

soft-dirty 快照的延迟探测/启用策略（避免在启动/恢复时的昂贵 clear_refs 操作）：

```rust
// hypervisor/vmm/src/memory_manager.rs:2406
fn send_soft_dirty_memory(
    &self,
    destination_url: &str,
    memory_vol_url: &Option<String>,
) -> result::Result<(), MigratableError> {
    // 首次调用：tracker 未就绪，写完整 anon-page 快照，然后尝试启用
    if !self.soft_dirty_armed.load(Ordering::Acquire) {
        self.send_pagemap_anon_memory(destination_url, memory_vol_url)?;

        if probe_soft_dirty_support() {
            self.soft_dirty_armed.store(true, Ordering::Release);
        }
        return Ok(());
    }

    // 后续调用：取 anon ∩ soft-dirty 交集
    let (filtered_ranges, stats) = filter_memory_ranges_by_anon_and_soft_dirty(
        &guest_memory, &self.snapshot_memory_ranges,
    )?;

    // 只写变更页到目标文件
    for range in filtered_ranges.regions() {
        let file_off = Self::calculate_file_offset_for_gpa(...)?;
        self.save_range_to_file(&memory_file, range, file_off)?;
    }

    // 重新启用 soft-dirty 追踪 (clear_refs(4))
    // 为下一个快照窗口做准备
}
```

### 6.3 三种快照类型对比

| 快照类型 | 保存范围 | 适用场景 | 数据量 |
|---------|---------|---------|-------|
| `Full` | 所有 guest 内存 | 首次快照、迁移 | 100% |
| `Incremental` (pagemap_anon) | 所有 CoW 匿名页 | 自恢复以来的首次增量 | 通常 5-30% |
| `SoftDirty` | anon ∩ soft-dirty | 连续增量快照 | 通常 1-10% |

---

## 7. 设计权衡：Lazy Load 缺页开销 vs 快速启动

### 7.1 问题：Lazy Load 和快速启动是否矛盾？

一个直觉上的疑问：lazy load 意味着恢复后 vCPU 执行时会频繁触发缺页，这些缺页的开销岂不是抵消了"快速启动"的优势？

答案是**不矛盾**。理解这一点需要把"启动"拆分为两个阶段：

```
|← ① 恢复阶段 (Restore) →|←    ② 首次运行阶段 (First Run)    →|
|    mmap 建立 + 状态恢复   |   vCPU 执行，缺页按需加载页面      |
|    耗时恒定 ~60-70ms     |   页面逐步从快照文件加载到内存       |
```

- **① 恢复阶段**是 Lazy Load 优化的主战场：只做 mmap（不读数据），耗时**恒定 ~60-70ms**，与快照/内存大小完全无关
- **② 首次运行阶段**才会触发缺页，但缺页由内核高效处理，且大部分页面根本不会被访问

### 7.2 性能基准数据

项目性能测试数据（`docs/blog/posts/2026-06-01-cubesandbox-perf-benchmark.md:107`）直接证明了 lazy load 的有效性：

| 脏页大小 | 快照创建时间 | **Sandbox 恢复时间** |
|---------|------------|-------------------|
| 6.8 MB | 41.2 ms | **59.8 ms** |
| 33.9 MB | 59.6 ms | **60.4 ms** |
| 115.2 MB | 90.8 ms | **60.5 ms** |
| 180.6 MB | 115.1 ms | **63.9 ms** |
| 282.5 MB | 155.4 ms | **67.3 ms** |
| 587.9 MB | 263.3 ms | **68.7 ms** |
| 893.0 MB | 371.3 ms | **70.5 ms** |
| 1121.1 MB | 448.1 ms | **66.7 ms** |

快照创建时间随脏页大小线性增长（需要写入更多数据），但 **Sandbox 恢复时间始终稳定在 59-71ms**，不受快照大小影响。如果不用 lazy load（全量拷贝），1GB 内存的恢复时间会线性增长到数百毫秒甚至秒级。

高并发场景下效果更突出（`docs/blog/posts/2026-06-01-cubesandbox-perf-benchmark.md:124`）：

| 并发数 | 总 Sandbox 数 | Wall 平均耗时 | **均摊耗时/sandbox** |
|-------|-------------|-------------|-------------------|
| 1 | 1 | 69.9 ms | 69.9 ms |
| 10 | 10 | 89.7 ms | **9.0 ms** |
| 20 | 20 | 97.3 ms | **4.9 ms** |

20 并发创建时，均摊仅 **4.9ms/sandbox**，这得益于 page cache 共享。

### 7.3 缺页开销为何在实践中可接受

#### 7.3.1 大部分内存不会被立即访问

快照记录的是 VM 的**全部内存状态**——内核代码段、语言运行时、已加载的共享库、堆内存、空闲页等。但恢复后 guest 真正立即需要的只是**当前执行路径上的少量页面**。一个 2GB 的 VM 可能只需 touch 几十 MB 就能开始服务请求。

项目文档中明确表达了这一设计哲学（`docs/blog/posts/2026-05-22-from-serverless-to-agent.md:55`）：

> Snapshot-based startup also implies lazy EPT page-table population: sandbox memory pages that the snapshot didn't touch are mapped on demand via EPT.
> **Defer everything that can be deferred until it's actually needed** — this is a recurring theme in Cube's design.

#### 7.3.2 内核原生 mmap page fault 处理极其高效

CubeSandbox 选择 `MAP_PRIVATE` file-backed mmap 而非 `userfaultfd`，缺页完全在内核态处理，无需用户态上下文切换：

| 缺页处理方式 | 上下文切换 | 单次开销 |
|------------|-----------|---------|
| `userfaultfd` | 内核→用户态→内核（2次切换） | 数十微秒 |
| `MAP_PRIVATE` mmap | 纯内核态处理 | 数微秒 |

内核的 file-backed page fault 处理还自带 **readahead 优化**：触发一个页面的缺页时，内核会预读周围的多个页面（默认 readahead 窗口通常为 128KB-256KB），大幅减少后续缺页次数。

#### 7.3.3 Page Cache 跨 Sandbox 共享

这是 lazy load 在高密度部署场景下的**最大优势**。同一模板创建的所有 sandbox 通过 `MAP_PRIVATE` 映射同一个快照文件，**只读页面在 page cache 中自动共享**：

```
Sandbox A ──┐
Sandbox B ──┤── MAP_PRIVATE ──→ 快照文件 ──→ Page Cache (内核)
Sandbox C ──┘
                                              ↑
                                    所有 sandbox 的只读页
                                    共享同一份 page cache
```

- **第一个 sandbox** 触发缺页时需要从磁盘读取页面到 page cache
- **后续同模板 sandbox** 的相同页面直接命中 page cache（纯内存操作），缺页开销趋近于零
- 内核代码、共享库等只读内容在整个节点上只占一份物理内存

文档（`docs/blog/posts/2026-05-22-from-serverless-to-agent.md:69`）：

> Sandboxes cloned from the same memory snapshot share that snapshot via mmap, naturally sharing all unmodified pages — kernel text, for example, doesn't change once boot completes, so a whole node's worth of sandboxes can share a single copy of the kernel text. That alone saves 30+ MB per sandbox.

#### 7.3.4 Lazy EPT 页表填充

在 KVM 虚拟化场景下，缺页实际上发生在两个层级：

1. **EPT violation**：vCPU 访问 GPA 时，EPT（Extended Page Table）中无映射，触发 VM-Exit 到 KVM
2. **Host page fault**：KVM 为该 GPA 建立 EPT 映射时，对应的 HVA 在 host 页表中无映射，触发 host page fault

Lazy load 使得这两层缺页都按需发生。对于从未访问的页面，既不会建立 EPT 映射，也不会分配物理页——双重节省。

### 7.4 prefault 选项：显式禁用 Lazy Load

对于延迟极度敏感、不能容忍任何运行时缺页的场景，CubeSandbox 提供 `prefault` 选项来显式禁用 lazy load。

`prefault` 参数的流转路径：

```
CLI/API RestoreConfig.prefault (默认 false)
    → hypervisor/vmm/src/config.rs:2099
        → Vm::new_from_snapshot() prefault 参数
            → hypervisor/vmm/src/vm.rs:828
                → MemoryManager::new_from_snapshot() prefault 参数
                    → hypervisor/vmm/src/memory_manager.rs:1310
                        → create_ram_region() prefault 参数
                            → hypervisor/vmm/src/memory_manager.rs:1518
                                → mmap_flags |= MAP_POPULATE
```

当 `prefault=true` 时，`create_ram_region()` 在 mmap 标志中加入 `MAP_POPULATE`（`memory_manager.rs:1518`）：

```rust
if prefault {
    mmap_flags |= libc::MAP_POPULATE;
}
```

`MAP_POPULATE` 强制内核在 mmap 调用时就将所有页面从文件读入内存并建立页表映射，消除后续运行时缺页。

VMM 文档（`hypervisor/docs/memory.md:158`）明确说明了这个权衡：

> By triggering prefault, one can allocate all required physical memory and create its page tables while calling `mmap`. With physical memory allocated, the number of page faults will decrease during running, and performance will also improve.
>
> Note that boot of VM will be **slower** with `prefault` enabled because of allocating physical memory and creating page tables in advance, and physical memory of the specified size will be consumed quickly.

可通过多种方式配置：

| 配置方式 | 参数 | 默认值 |
|---------|------|-------|
| VMM 命令行 | `--memory prefault=on\|off` | off |
| 内存 Zone | `--memory-zone prefault=on\|off` | off |
| 恢复配置 | `--restore prefault=on\|off`（覆盖 memory 设置） | off |
| Rollback API | JSON `"prefault": true\|false` | false |

### 7.5 THP 与 Lazy Load 的交互

值得注意的是，THP（Transparent Huge Pages）优化**不适用于 lazy load 的快照内存区域**。`create_ram_region()` 中的 `MADV_HUGEPAGE` 调用有前置条件（`memory_manager.rs:1529`）：

```rust
// THP 仅用于匿名映射，不用于 file-backed 映射
if region.file_offset().is_none() && thp {
    let ret = unsafe { libc::madvise(region.as_ptr() as _, size, libc::MADV_HUGEPAGE) };
}
```

在 fast restore 路径下，内存区域是 file-backed（`snap_file` 非 None），因此 `region.file_offset()` 返回 `Some(...)`，条件不满足，`MADV_HUGEPAGE` 不会被应用。THP 只对非快照恢复的匿名内存分配（如冷启动）生效。

### 7.6 当前缺少的优化手段

当前代码中**没有**针对快照内存文件的以下优化：

| 优化手段 | 当前状态 | 说明 |
|---------|---------|------|
| `MADV_SEQUENTIAL` / `MADV_WILLNEED` | 未使用 | 未对快照内存做预读提示 |
| `posix_fadvise(FADV_WILLNEED)` | 仅用于磁盘镜像 | `Cubelet/storage/pool.go:303` 对 ext4 元数据块做预读，但不涉及内存快照 |
| 用户态 prefetch / warm-up | 未实现 | 无主动预加载热点页的逻辑 |
| THP on file-backed mmap | 不适用 | 内核限制：`MAP_PRIVATE` file-backed 区域不支持 THP |

磁盘镜像的 `fadvise` 预读逻辑（`Cubelet/storage/pool.go:303`）作为对比参考：

```go
func fadvise(filePath string, size int64, blocks []uint32) {
    file, err := os.OpenFile(filePath, os.O_RDWR, 0755)
    // ...
    if size > 0 {
        _ = unix.Fadvise(int(file.Fd()), 0, size, unix.FADV_WILLNEED)
    }
    for _, offset := range blocks {
        _ = unix.Fadvise(int(file.Fd()), int64(offset)*4096, 4096, unix.FADV_WILLNEED)
    }
}
```

这仅用于预读 ext4 超级块、组描述符和 inode 表（`pool.go:108`），而非快照内存。

### 7.7 方案对比总结

| 方案 | 恢复耗时 | 运行时缺页 | 内存消耗 | 适用场景 |
|------|---------|-----------|---------|---------|
| **冷启动** | 秒级（kernel + init + runtime） | 无 | 100% | 首次部署 |
| **全量恢复** (`prefault=on`) | 线性增长（数百ms ~ 秒级） | 无 | 100% | 延迟极度敏感 |
| **Lazy Load** (默认) | **恒定 ~60-70ms** | 按需缺页 | **仅实际访问量** | 高密度部署（推荐） |

**结论**：Lazy Load 不是"快速启动"的矛盾，而是**精心选择的最优权衡点**——用"运行时按需加载的小额开销"换取"恢复时间恒定 + 内存大幅节省 + page cache 跨 sandbox 共享"。在高密度部署场景（一个节点运行成百上千个 sandbox）下，这个权衡的收益是巨大的。

---

## 8. 深入理解：Page Cache 共享与 CoW 写入机制

### 8.1 核心结论：所有同模板 Sandbox 共享同一文件

CubeSandbox 的一个关键设计：**所有来自同一模板的 sandbox 都 `MAP_PRIVATE` mmap 同一个模板内存文件**，不为每个 sandbox 创建独立的内存文件副本。写隔离完全由 Linux 内核的 `MAP_PRIVATE` CoW 语义保证。

```
模板 tpl-abc123-memory （一个文件，一个 inode）
        │
        ├── Sandbox A: open(O_RDONLY) → mmap(MAP_PRIVATE)
        ├── Sandbox B: open(O_RDONLY) → mmap(MAP_PRIVATE)
        ├── Sandbox C: open(O_RDONLY) → mmap(MAP_PRIVATE)
        └── ...
              │
              ▼
        Page Cache（内核，按 inode+offset 索引）
              │
              ├── 只读页：所有 sandbox 共享同一物理页
              └── 写入页：触发 CoW，每个 sandbox 获得私有匿名页
```

### 8.2 memory_vol_url 的解析：指向模板级文件

追踪 `memory_vol_url` 从 Cubelet 到 VMM 的完整流转，可以确认它指向的是**模板级别**的文件，而非 sandbox 级别：

**第一步：Cubelet 从快照目录解析模板内存卷名**

```go
// Cubelet/storage/local.go:921
func (l *local) resolveSnapshotMemoryVolFromCatalog(
    ctx context.Context, annotations map[string]string,
) (string, string, error) {
    // 通过 logicalID（模板ID/快照ID）查找 catalog
    logicalID := strings.TrimSpace(
        annotations[constants.MasterAnnotationRuntimeSnapshotID],
    )
    // ...
    entry, err := GetLocalSnapshot(ctx, logicalID)
    // ...
    volumeName := strings.TrimSpace(entry.MemoryVol)  // 模板级卷名
    return volumeName, volumeKind, nil
}
```

卷名生成规则（`cubecow_snapshot_artifacts.go:467`）：

```go
func cowTemplateMemoryName(templateID string) string {
    return fmt.Sprintf("tpl-%s-memory", templateID)  // 模板级，非 sandbox 级
}
```

**第二步：解析卷名为设备路径**

```go
// Cubelet/storage/local.go:865
func (l *local) prefetchRestoreMemoryVolURL(
    ctx context.Context, opts *workflow.CreateContext,
) (string, error) {
    // ...
    volumeName, volumeKind, err := l.resolveSnapshotMemoryVolFromCatalog(ctx, annotations)
    // ...
    // ResolveDevPath 返回已有卷的路径，不创建新副本
    devPath, err := l.cowManager.ResolveDevPath(ctx, volumeName, normalizedKind)
    return cowFileURLFromPath(devPath), nil  // 如 "file:///path/to/tpl-abc-memory"
}
```

**第三步：Cubebox 插件设置 annotation**

```go
// Cubelet/plugins/cbri/cubeboxcbri/cubebox.go:208
memoryVolURL := snapshotRestoreMemoryVolURLFromStorageInfo(flowOpts)
if memoryVolURL != "" {
    annotations[constants.AnnotationVMSnapshotMemoryVolURL] = memoryVolURL
}
```

**第四步：CubeShim 读取 annotation 传递给 VMM**

```rust
// CubeShim/shim/src/sandbox/config.rs:118
let snapshot_memory_vol_url = anno
    .get(ANNO_SNAPSHOT_MEMORY_VOL_URL)
    .map(|x| x.trim().to_string())
    .filter(|x| !x.is_empty());

// CubeShim/shim/src/sandbox/sb.rs:864,884
let restore_memory_vol_url = self.conf.snapshot_memory_vol_url.clone();
let config = RestoreConfig {
    memory_vol_url: restore_memory_vol_url,  // 模板级 URL
    // ...
};
```

**第五步：VMM 以只读方式打开并 mmap**

```rust
// hypervisor/vmm/src/memory_manager.rs:1320-1326
let memory_file = if fast_restore {
    Some(memory_file_target.open_read()?)  // O_RDONLY
} else {
    None
};

// hypervisor/vmm/src/memory_manager.rs:1493-1495
let fo = if let Some(f) = snap_file {
    mmap_flags |= libc::MAP_PRIVATE;      // CoW 语义
    Some(FileOffset::new(f, snap_offset))
};
```

整条链路都指向同一个 `tpl-<templateID>-memory` 文件。**每个 sandbox 的 VMM 进程独立 `open()` + `mmap()` 这同一个文件**，但因为 `MAP_PRIVATE`，内核为每个进程提供独立的写入视图。

### 8.3 Linux 内核 Page Cache 共享原理

Linux page cache 以 `(inode, offset)` 为 key 管理缓存页。当多个进程 `MAP_PRIVATE` mmap 同一个文件时：

```
进程 A (Sandbox A VMM)                进程 B (Sandbox B VMM)
┌─────────────────────┐              ┌─────────────────────┐
│ 虚拟地址空间         │              │ 虚拟地址空间         │
│                     │              │                     │
│ 0x7f000000 ─────────┤              │ 0x7f800000 ─────────┤
│   page 0  ──┐      │              │   page 0  ──┐      │
│   page 1  ──┼──────┼──────────────┼── page 1  ──┤      │
│   page 2  ──┤      │              │   page 2 (W)│──→ 私有匿名页
│   ...       │      │              │   ...       │      │
└─────────────┘      │              └─────────────┘      │
        │            │                      │            │
        ▼            │                      ▼            │
┌─────────────────────────────────────────────────────────┐
│                    Page Cache (内核)                      │
│                                                          │
│  inode=tpl-abc-memory:                                   │
│    offset 0    → 物理页 P0  ← Sandbox A,B 共享          │
│    offset 4096 → 物理页 P1  ← Sandbox A,B 共享          │
│    offset 8192 → 物理页 P2  ← 仅 Sandbox A 使用         │
│                              (B 已 CoW 到私有匿名页)     │
│    ...                                                   │
└─────────────────────────────────────────────────────────┘
        │
        ▼
┌──────────────────┐
│ 快照文件 (磁盘)   │
│ tpl-abc-memory   │
└──────────────────┘
```

关键点：
- **同一 inode** 的相同 offset 在 page cache 中只有一份物理页
- 所有 `MAP_PRIVATE` 映射了该文件的进程，在**只读访问**时共享同一个 page cache 物理页
- page cache 是全局的，由内核的 LRU 算法管理生命周期

### 8.4 读访问缺页：Page Cache 查找与共享

当 sandbox 的 vCPU 首次**读**某个 guest 内存页时：

```
vCPU 读指令 → GPA → HVA (mmap 区域内)
                      │
                      ▼
              host PTE 不存在
                      │
                      ▼
              CPU 触发 page fault
                      │
                      ▼
              内核 page fault handler
                      │
                      ▼
          查找 page cache: (inode, offset)
                      │
         ┌────────────┴────────────┐
         ▼                         ▼
    cache 命中                 cache 未命中
    (其他 sandbox             (首次访问此页)
     已加载过此页)                  │
         │                         ▼
         │                从磁盘读取 4KB 到
         │                新分配的 page cache 页
         │                         │
         ▼                         ▼
    建立 PTE: HVA → page cache 物理页 (只读, 共享)
                      │
                      ▼
              vCPU 继续执行（page fault 透明处理）
```

**cache 命中路径**是最关键的优化：当节点上已有同模板 sandbox 加载过某页时，后续 sandbox 的缺页只需建立一个 PTE 映射，**无磁盘 I/O**，延迟在微秒级。

内核还会自动执行 **readahead**：触发一个页面缺页时，内核会预读取周围连续的多个页面（默认 readahead 窗口通常 128KB-256KB），大幅减少后续顺序访问时的缺页次数。

### 8.5 写访问缺页：CoW（Copy-on-Write）详细流程

当 sandbox 的 vCPU 首次**写**某个 guest 内存页时，内核执行 CoW，为该进程分配私有匿名页：

```
vCPU 写指令 → GPA → HVA
                      │
                      ▼
          ┌───────────────────────┐
          │ HVA 的 PTE 状态？      │
          └───────────┬───────────┘
                      │
         ┌────────────┴────────────────┐
         ▼                              ▼
    PTE 不存在                    PTE 存在但只读
  (页面从未被访问过)            (之前读过，指向 page cache)
         │                              │
         ▼                              ▼
  从 page cache 获取页           已有 page cache 页
  (若 cache miss 则从磁盘读)
         │                              │
         └──────────┬───────────────────┘
                    ▼
         ┌─────────────────────┐
         │  CoW 操作 (内核执行)  │
         │                     │
         │  1. 分配新匿名物理页 │
         │  2. 拷贝 page cache  │
         │     页内容到新匿名页 │
         │  3. 更新 PTE:        │
         │     HVA → 新匿名页  │
         │     权限: 可读可写   │
         │  4. page cache 页    │
         │     不受影响,继续     │
         │     服务其他进程     │
         └─────────────────────┘
                    │
                    ▼
         vCPU 写入新匿名页（私有，不影响其他 sandbox）
```

CoW 之后的状态变化：

```
写入前：
  Sandbox A page 5 PTE → Page Cache 页 (只读, 共享)
  Sandbox B page 5 PTE → Page Cache 页 (只读, 共享)

Sandbox B 写入 page 5 后：
  Sandbox A page 5 PTE → Page Cache 页 (只读, 共享)  ← 不受影响
  Sandbox B page 5 PTE → 匿名页 B5 (可写, 私有)      ← 新分配
                          内容 = Page Cache 页内容 + B 的修改
```

关键特性：
- **原子性**：CoW 是 page fault handler 中的原子操作，对 vCPU 完全透明
- **惰性**：只有实际被写入的页才会触发 CoW，未写入的页始终共享 page cache
- **隔离性**：CoW 后的匿名页完全私有，对其的后续读写不再触发 page fault
- **不可逆**：一旦 CoW 产生匿名页，该页不会回退到 page cache 共享状态

### 8.6 写入后的页面生命周期

```
          ┌──────────────────────────────────────┐
          │        页面状态机 (单个页面)           │
          └──────────────────────────────────────┘

     ┌─────────┐    首次读      ┌──────────────────┐
     │ 未映射   │ ──────────→   │ 只读, 指向        │
     │ (无PTE)  │               │ Page Cache 页     │
     └─────────┘               │ (与其他 sandbox   │
          │                    │  共享)             │
          │ 首次写              └──────────────────┘
          │ (直接 CoW)                   │
          │                              │ 写入
          ▼                              ▼
     ┌──────────────────────────────────────┐
     │ 可写, 指向私有匿名页                   │
     │ (该 sandbox 独占)                     │
     │                                      │
     │ - 后续读写无 page fault               │
     │ - pagemap 中标记为 KPF_ANON (bit 12)  │
     │ - 可被 swap out 到交换空间             │
     │ - 增量快照时被 pagemap_anon 识别并保存  │
     └──────────────────────────────────────┘
```

### 8.7 多 Sandbox 场景下的物理内存占用分析

假设模板快照大小为 2GB，节点上运行 100 个同模板 sandbox：

| 内存类别 | 大小估算 | 说明 |
|---------|---------|------|
| Page Cache（共享） | ~200-500 MB | 所有 sandbox 共享的只读页面（内核、运行时、共享库等） |
| 匿名页（每 sandbox） | ~50-200 MB | 每个 sandbox 被写入的私有页面 |
| **总物理内存** | **~5-20 GB** | 远小于 100 × 2GB = 200GB |
| **理论最大** | 200 GB | 所有页面全部 CoW（不可能的极端情况） |

**节省比例**：在典型工作负载下，100 个 sandbox 的实际物理内存占用仅为理论最大值的 **3-10%**。核心原因：

1. **内核代码段**（~30MB）：只读，所有 sandbox 共享 1 份 → 节省 ~3GB
2. **语言运行时**（~50-100MB）：大部分只读，共享 → 节省 ~5-10GB
3. **共享库**（~20-50MB）：只读，共享
4. **空闲页/未使用页**：从未访问，零开销
5. **每个 sandbox 独立修改的数据**：堆、栈、运行时状态 → 这部分是 CoW 匿名页，不可共享

### 8.8 Page Cache 与增量快照的关系

pagemap_anon 增量快照机制精确利用了 CoW 的结果——只有被 CoW 产生的匿名页才需要保存：

```
Page Cache 页 (file-backed, 共享)
    │
    │ 无需保存到增量快照
    │ 因为这些页面的内容就是模板快照文件中的内容
    │ 恢复时可以直接从文件重新 mmap
    │
匿名页 (CoW 产生, 私有)
    │
    │ 必须保存到增量快照
    │ pagemap_anon 通过 /proc/self/pagemap (present) +
    │ /proc/kpageflags (KPF_ANON bit 12) 精确识别
    │ 这些页面包含了 guest 修改后的数据
```

这形成了一个**自洽的闭环**：

```
模板文件 ──MAP_PRIVATE mmap──→ Page Cache (共享只读页)
    ▲                                │
    │                                │ guest 写入触发 CoW
    │                                ▼
    │                          匿名页 (私有)
    │                                │
    │                                │ pagemap_anon 识别
    │                                ▼
    │                         增量快照 (只写匿名页)
    │                                │
    │                                │ 覆写到 reflink clone
    └── 新模板文件 (base + 变更) ◄────┘
```

### 8.9 为什么不用 reflink 做 per-sandbox 内存文件克隆

一个自然的问题：既然 cubecow 的 reflink (`FICLONE`) 是 O(1) 的，为什么不给每个 sandbox 创建自己的 reflink 内存文件副本？

| | 当前方案：共享 mmap | 替代方案：per-sandbox reflink clone |
|---|---|---|
| **Page Cache** | 所有 sandbox 共享（同一 inode） | 每个 sandbox 独立（不同 inode，不同 page cache 条目） |
| **内存效率** | 极高——只读页面全局只有 1 份物理页 | 较差——每个 sandbox 读取的页面在 page cache 中各有 1 份 |
| **100 sandbox 场景** | ~200MB page cache + N × 匿名页 | 100 × ~200MB page cache + N × 匿名页 |
| **文件系统开销** | 无额外文件创建 | 每次创建 sandbox 需要 FICLONE（虽然 O(1) 但有 inode 开销） |
| **增量快照** | 需要额外机制写回脏页 | 可直接写回到自己的文件 |

当前方案选择**共享 mmap + MAP_PRIVATE CoW**，牺牲了增量快照写回的便利性，换取了 page cache 级别的物理内存共享。在高密度场景下（单节点数百 sandbox），这个选择带来的内存节省是巨大的。

---

## 10. 关键数据结构汇总

| 结构体 | 文件位置 | 用途 |
|--------|----------|------|
| `MemoryManager` | `hypervisor/vmm/src/memory_manager.rs:272` | Guest 内存管理、mmap、快照操作 |
| `MemoryManagerSnapshotData` | `hypervisor/vmm/src/memory_manager.rs:2891` | 序列化的快照状态（memory_ranges, guest_ram_mappings） |
| `MemorySnapshotFile` | `hypervisor/vmm/src/memory_manager.rs:69` | 解析和打开快照内存文件 |
| `RestoreConfig` | `hypervisor/vmm/src/config.rs:2096` | 恢复参数（source_url, prefault, memory_vol_url） |
| `SnapshotConfig` | `hypervisor/vm-migration/src/lib.rs:161` | 快照创建参数（destination_url, snapshot_type） |
| `SnapshotType` | `hypervisor/vm-migration/src/lib.rs:110` | Full / Incremental / SoftDirty 枚举 |
| `Snapshot` | `hypervisor/vm-migration/src/lib.rs:187` | 树形结构 VM 状态快照 |
| `PagemapAnonStats` | `hypervisor/vmm/src/pagemap_anon.rs:86` | pagemap_anon 过滤统计 |
| `SoftDirtyStats` | `hypervisor/vmm/src/soft_dirty.rs:101` | soft-dirty 过滤统计 |
| `SnapshotInfo` | `CubeShim/shim/src/hypervisor/snapshot.rs:28` | 快照元数据（版本、资源配置） |
| `SandBox` | `CubeShim/shim/src/sandbox/sb.rs:59` | Sandbox 生命周期管理 |
| `CowVolumeManager` | `Cubelet/storage/cubecow_volume_manager.go:90` | cubecow reflink 卷/快照管理 |
| `ReflinkEngine` | `cubecow/src/engine/reflink.rs:113` | xfs reflink FICLONE 存储后端 |

---

## 11. 关键文件索引

| 文件路径 | 关键行号 | 功能 |
|---------|---------|------|
| `hypervisor/vmm/src/memory_manager.rs` | L1305 | `new_from_snapshot()` — lazy load 决策入口 |
| `hypervisor/vmm/src/memory_manager.rs` | L1362 | `support_fast_restore_check()` — fast/slow 判断 |
| `hypervisor/vmm/src/memory_manager.rs` | L1474 | `create_ram_region()` — mmap 建立（MAP_PRIVATE） |
| `hypervisor/vmm/src/memory_manager.rs` | L679 | `restore_memory_regions_and_zones()` — 区域恢复 |
| `hypervisor/vmm/src/memory_manager.rs` | L754 | `fill_saved_regions()` — slow restore 全量拷贝 |
| `hypervisor/vmm/src/memory_manager.rs` | L2298 | `send_pagemap_anon_memory()` — 增量快照 |
| `hypervisor/vmm/src/memory_manager.rs` | L2406 | `send_soft_dirty_memory()` — soft-dirty 快照 |
| `hypervisor/vmm/src/memory_manager.rs` | L2910 | `snapshot()` — 快照创建 |
| `hypervisor/vmm/src/pagemap_anon.rs` | L119 | `get_anon_pages()` — 识别 CoW 匿名页 |
| `hypervisor/vmm/src/pagemap_anon.rs` | L231 | `filter_memory_ranges_by_pagemap_anon()` — 过滤 |
| `hypervisor/vmm/src/soft_dirty.rs` | L133 | `probe_soft_dirty_support()` — 内核特性探测 |
| `hypervisor/vmm/src/soft_dirty.rs` | L150 | `clear_soft_dirty()` — 清除 PTE soft-dirty 位 |
| `hypervisor/vmm/src/soft_dirty.rs` | L193 | `get_soft_dirty_pages()` — 读取 soft-dirty bitmap |
| `hypervisor/vmm/src/vm.rs` | L821 | `Vm::new_from_snapshot()` — VM 恢复入口 |
| `hypervisor/vmm/src/vm.rs` | L2662 | `Vm::snapshot()` — VM 快照 |
| `hypervisor/vmm/src/vm.rs` | L2735 | `Vm::restore()` — CPU/设备状态恢复 |
| `hypervisor/vmm/src/lib.rs` | L653 | `vm_restore()` — VMM 恢复编排 |
| `hypervisor/vm-migration/src/lib.rs` | L110 | `SnapshotType` 枚举定义 |
| `CubeShim/shim/src/snapshot/mod.rs` | L111 | `do_snapshot()` — 模板快照创建流程 |
| `CubeShim/shim/src/sandbox/sb.rs` | L764 | `by_snapshot()` — 快照恢复判断 |
| `CubeShim/shim/src/sandbox/sb.rs` | L838 | `restore_vm()` — Sandbox 恢复流程 |
| `CubeShim/shim/src/hypervisor/snapshot.rs` | L28 | `SnapshotInfo` — 元数据结构 |
| `cubecow/src/engine/reflink.rs` | L87 | `FICLONE` ioctl 常量 |
| `cubecow/src/engine/reflink.rs` | L638 | `create_snapshot()` — reflink 文件克隆 |
| `Cubelet/storage/cubecow_volume_manager.go` | L228 | `CommitTemplateMemory()` — 模板内存提交 |
| `Cubelet/storage/cubecow_snapshot_artifacts.go` | L129 | `CommitTemplateMemoryFromBase()` — 基础克隆 |
