# Sandbox Network Architecture

## Overview

CubeSandbox 的网络架构采用 **virtio-net + TAP + eBPF** 的组合，无 veth、无 bridge、无 iptables。所有数据包转发、NAT、网络策略均由 eBPF TC 程序完成。

## 数据路径

```
Guest VM                          Host
┌─────────────────┐               ┌──────────────────────────────────────┐
│                 │               │                                      │
│  eth0           │   virtqueue   │  TAP (z192.168.0.40)                 │
│  (virtio-net)   │◄────────────►│    │                                  │
│                 │               │    │ TC ingress: from_cube (eBPF)     │
│                 │               │    │   - ARP proxy                    │
│                 │               │    │   - SNAT (sandbox IP → node IP)  │
│                 │               │    │   - network policy check         │
│                 │               │    │   - bpf_redirect()               │
│                 │               │    │                                  │
│                 │               │    ├──► cube-dev (dummy gateway)      │
│                 │               │    │     TC egress: from_envoy (eBPF) │
│                 │               │    │     - sandbox 间本地通信          │
│                 │               │    │                                  │
│                 │               │    └──► eth0 (物理网卡)               │
│                 │               │          TC ingress: from_world (eBPF)│
│                 │               │          - 反向 DNAT                  │
│                 │               │          - bpf_redirect() → TAP      │
└─────────────────┘               └──────────────────────────────────────┘
```

## 1. Guest 侧：virtio-net 设备

Guest VM 内的网络设备是标准的 **virtio-net PCI 设备**，以 host TAP 为后端。

### 1.1 Hypervisor 创建 virtio-net

**`hypervisor/vmm/src/device_manager.rs:2413-2434`**

```rust
let virtio_net = if let Some(ref tap_if_name) = net_cfg.tap {
    Arc::new(Mutex::new(
        virtio_devices::Net::new(         // 创建 virtio-net 设备
            id.clone(),
            Some(tap_if_name),            // host TAP 名，如 "z192.168.0.40"
            None,
            None,
            Some(net_cfg.mac),            // guest MAC 地址
            &mut net_cfg.host_mac,
            net_cfg.mtu,
            self.force_iommu | net_cfg.iommu,
            net_cfg.num_queues,           // 队列数
            net_cfg.queue_size,           // 队列深度
            self.seccomp_action.clone(),
            net_cfg.rate_limiter_config,  // 可选 QoS 限速
            self.exit_evt.try_clone().map_err(DeviceManagerError::EventFd)?,
            state,
            Some(self.sandbox_id.clone()),
        )
        .map_err(DeviceManagerError::CreateVirtioNet)?,
    ))
};

// 注册为 VirtioDevice trait object
(
    Arc::clone(&virtio_net) as Arc<Mutex<dyn virtio_devices::VirtioDevice>>,
    virtio_net as Arc<Mutex<dyn Migratable>>,
)
```

### 1.2 CubeShim 配置网络参数

**`CubeShim/shim/src/hypervisor/config.rs:236-266`**

CubeShim 将 Cubelet 传来的网络信息（通过 `cube.net` annotation）转为 hypervisor 的 `NetConfig`：

```rust
pub fn add_nets(&mut self, net: &Net) -> CResult<&mut Self> {
    let nets = self.nets.as_mut().unwrap();
    for n in net.interfaces.iter() {
        let mut nc: NetConfig = NetConfig {
            id: Some(format!("{}-{}", utils::NET_DEVICE_ID_PRE, nets.len())),
            tap: n.name.clone(),    // host TAP 设备名
            ..Default::default()
        };
        nc.mac = MacAddr::from_str(&n.mac)
            .map_err(|_| format!("New mac addr failed:{}", &n.mac))?;
        // 可选 QoS rate limiter（带宽/PPS 限速）
        if let Some(q) = &n.qos {
            nc.rate_limiter_config = Some(RateLimiterConfig { ... });
        }
        nets.push(nc);
    }
    Ok(self)
}
```

### 1.3 Guest 内核参数

**`CubeShim/shim/src/hypervisor/config.rs:82`**

```rust
"net.ifnames=0".to_string(),  // 使用传统命名，网卡显示为 eth0
```

### 1.4 网络接口数据结构

**`CubeShim/shim/src/sandbox/net.rs:107-127`**

```rust
pub struct Interface {
    pub name: Option<String>,       // host TAP 名（如 "z192.168.0.40"）
    pub guest_name: String,         // guest 内设备名（"eth0"）
    pub mac: String,                // MAC 地址
    pub mtu: u32,                   // MTU
    pub ips: Vec<MVMIp>,            // IP 地址列表
    pub qos: Option<NetQos>,        // 可选 QoS 配置
}
```

默认网络设备 ID 为 `tap-0`（`CH_NET_ID: &str = "tap-0"`），即每个 sandbox 默认一张网卡。

## 2. Host 侧：TAP 设备

每个 sandbox 对应一个 host TAP 设备，由 **network-agent** 负责创建和池化管理。

### 2.1 TAP 创建

**`network-agent/internal/service/netdevice.go:316-381`**

```go
const (
    tapNamePrefix    = "z"              // TAP 命名前缀
    cubeDevName      = "cube-dev"       // 网关设备名
    virtioNetHdrSize = 12               // virtio-net header 大小
)

func newTap(ip net.IP, mvmMacAddr string, mtu, cubeDevIdx int) (*tapDevice, error) {
    name := tapName(ip.String())         // 如 "z192.168.0.40"
    tapConfig := &netlink.Tuntap{
        LinkAttrs: netlink.LinkAttrs{Name: name, Flags: net.FlagUp},
        Mode:   netlink.TUNTAP_MODE_TAP,
        Flags:  unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_VNET_HDR | unix.IFF_ONE_QUEUE,
        Queues: 1,
    }

    netlink.LinkAdd(tapConfig)                                    // 1. 创建 TAP
    unix.Syscall(unix.SYS_IOCTL, fd, unix.TUNSETVNETHDRSZ, ...)  // 2. 设置 vnet header
    netlink.LinkSetUp(tapConfig)                                  // 3. 启用
    cubevs.AttachFilter(uint32(tap.Index))                        // 4. 挂载 eBPF 程序
    netlink.LinkSetMTU(tapConfig, mtu)                            // 5. 设置 MTU
    addARPEntry(ip, mvmMacAddr, cubeDevIdx)                       // 6. 添加 ARP 到 cube-dev
}
```

### 2.2 TAP fd 传递

TAP 文件描述符通过 **Unix socket + SCM_RIGHTS** 从 network-agent 传递给 Cubelet/hypervisor。

**`Cubelet/network/plugin_tap.go:1042-1093`**

```go
func requestNetworkAgentTapFile(socketPath, sandboxID, tapName string, timeout time.Duration) (*os.File, error) {
    conn, _ := net.DialUnix("unix", nil, addr)              // 连接 network-agent
    conn.Write(reqBody)                                       // 发送请求（sandboxID + tapName）
    n, oobn, _, _, _ := conn.ReadMsgUnix(buf, oob)           // 读取响应 + OOB 数据
    msgs, _ := syscall.ParseSocketControlMessage(oob[:oobn])
    for _, msg := range msgs {
        fds, _ := syscall.ParseUnixRights(&msg)              // 解析 SCM_RIGHTS 获取 fd
        return os.NewFile(uintptr(fds[0]), "/dev/net/tun"), nil
    }
}
```

Socket 路径：`/tmp/cube/network-agent-tap.sock`

### 2.3 网关设备 cube-dev

**`network-agent/internal/service/netdevice.go:192-230`**

`cube-dev` 是一个 Linux **dummy** 类型设备，作为所有 sandbox 的 L3 网关：

```go
func getOrCreateCubeDev(ip net.IP, mask, mtu int, macAddr string) (*cubeDev, error) {
    link, _ := netlinkLinkByName(cubeDevName)
    dummy, ok := link.(*netlink.Dummy)     // 类型是 dummy
    if !ok {
        return nil, fmt.Errorf("%s is not dummy", cubeDevName)
    }
    // 配置网关 IP 和 MAC
}
```

## 3. 转发层：eBPF TC 程序

所有数据包转发由三个 eBPF TC 程序完成，通过 `bpf_redirect()` 实现零拷贝重定向。

### 3.1 程序定义

**`CubeNet/cubevs/cubevs.go:78-80`**

```go
programNameFromEnvoy = "from_envoy"   // cube-dev TC egress
programNameFromCube  = "from_cube"    // TAP TC ingress
programNameFromWorld = "from_world"   // eth0 TC ingress
```

### 3.2 全局挂载（Init 时执行一次）

**`CubeNet/cubevs/miscs.go:92-130`**

```go
func Init(params Params) error {
    loadObject(params, loadLocalgw, "loadLocalgw")      // 加载 from_envoy
    loadObject(params, loadMvmtap, "loadMvmtap")        // 加载 from_cube
    loadObject(params, loadNodenic, "loadNodenic")       // 加载 from_world

    // cube-dev 出口 → from_envoy（sandbox 间本地通信）
    attachTCFilter(programNameFromEnvoy, params.Cubegw0Ifindex, TCEgress)

    // eth0 入口 → from_world（外部回包反向 NAT）
    attachTCFilter(programNameFromWorld, params.NodeIfindex, TCIngress)

    // lo 入口 → from_world（本地回环）
    attachTCFilter(programNameFromWorld, 1, TCIngress)
}
```

### 3.3 Per-TAP 挂载（每个 sandbox 创建时）

**`CubeNet/cubevs/miscs.go:132-151`**

```go
func AttachFilter(ifindex uint32) error {
    prog, _ := ebpf.LoadPinnedProgram(pinPath(programNameFromCube), nil)
    createQdisc(ifindex)                                                // 创建 clsact qdisc
    attachFilter(ifindex, uint32(prog.FD()), programNameFromCube, TCIngress)  // 挂载 from_cube
    return initNetPolicy(ifindex)                                       // 初始化网络策略
}
```

### 3.4 BPF 程序功能

#### from_cube（出站）

**`CubeNet/src/mvmtap.bpf.c:436-440`**

```c
/* This filter will be attached to the ingress path of Sandbox TAP devices.
 * It performs a SNAT/VXLAN-ENCAP and redirects the packets to target devices.
 */
SEC("tc")
int from_cube(struct __sk_buff *skb)
```

功能：ARP 代理、SNAT（sandbox IP → node IP）、网络策略检查、`bpf_redirect()` 到物理网卡或 cube-dev。

#### from_world（入站）

**`CubeNet/src/nodenic.bpf.c:243-247`**

```c
/* This filter will be attached to the ingress path of host NIC.
 * It performs NAT and then redirect the traffics to Sandbox TAP devices.
 */
SEC("tc")
int from_world(struct __sk_buff *skb)
```

功能：反向 DNAT（node IP → sandbox IP）、`bpf_redirect()` 到对应 TAP 设备。

#### from_envoy（本地通信）

**`CubeNet/src/localgw.bpf.c:14-18`**

```c
/* This filter will be attached to the egress path of cube-dev device.
 * It performs a DNAT and then redirect the traffics to Sandbox TAP devices.
 */
SEC("tc")
int from_envoy(struct __sk_buff *skb)
```

功能：DNAT 到目标 sandbox IP、`bpf_redirect()` 到对应 TAP 设备。

## 4. 组件交互总结

| 组件 | 代码位置 | 职责 |
|------|----------|------|
| **hypervisor (virtio-net)** | `hypervisor/vmm/src/device_manager.rs` | 创建 virtio-net PCI 设备，绑定 TAP 后端 |
| **CubeShim** | `CubeShim/shim/src/hypervisor/config.rs` | 将网络配置（TAP 名、MAC、QoS）传给 hypervisor |
| **network-agent** | `network-agent/internal/service/netdevice.go` | 创建/池化 TAP 设备，管理 cube-dev 网关，传递 TAP fd |
| **Cubelet** | `Cubelet/network/plugin_tap.go` | 通过 Unix socket 从 network-agent 获取 TAP fd |
| **CubeVS (eBPF)** | `CubeNet/cubevs/miscs.go` + `CubeNet/src/*.bpf.c` | SNAT/DNAT、网络策略、`bpf_redirect()` 转发 |

## 5. 性能影响因素

相比 ECS 直接使用物理网卡，sandbox 网络路径增加了以下开销：

1. **virtio-net virtqueue 处理**：每个网络包需要经过 virtqueue 的 avail/used ring 交互，产生 VM exit
2. **TAP 设备内核拷贝**：数据在 hypervisor 用户态和内核 TAP 之间拷贝
3. **eBPF SNAT/DNAT 处理**：每个包经过 eBPF 程序做地址/端口转换和策略检查
4. **中断注入开销**：virtio-net 中断需要通过 hypervisor 注入到 guest，涉及额外的 VM exit/entry
