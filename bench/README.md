# 性能对照与复现

benchmark 以真实独立进程比较 sertun 与 frp。生产转发路径不依赖测试工具；负载发生器和回显服务运行在基准进程中，不计入隧道与代理资源。

## 场景与配置

| 场景 | 客户端到服务端的路径 |
| --- | --- |
| `direct` | 直接 TCP |
| `mihomo-direct` | HTTP CONNECT → Mihomo → DIRECT |
| `mihomo-anytls` | HTTP CONNECT → Mihomo 客户端 → 本地 AnyTLS → Mihomo 服务端 → DIRECT |
| `mihomo-hy2` | HTTP CONNECT → Mihomo 客户端 → 本地 Hysteria 2 → Mihomo 服务端 → DIRECT |

所有端点绑定回环地址；AnyTLS / HY2 运行真实协议，使用运行时生成的临时证书和凭据。结果反映本机处理成本，不代表公网环境。

- `sertun`：默认的独立业务连接。
- `frp-plain`：TCP，关闭额外 TLS、业务加密、压缩和 TCP 复用，工作连接池为 1。
- `frp-mux`：与 frp-plain 相同，启用 TCP 复用。

历史配置写入 `poolCount=0`，frp v0.71.1 的默认值处理会将其补成 1；现显式配置为 1。历史报告的有效连接池也为 1。依据：[配置默认值](https://github.com/fatedier/frp/blob/v0.71.1/pkg/config/v1/client.go#L153)、[零值处理](https://github.com/fatedier/frp/blob/v0.71.1/pkg/util/util/types.go#L17)。

固定对照版本为 [frp v0.71.1](https://github.com/fatedier/frp/releases/tag/v0.71.1)和 [Mihomo v1.19.32](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.32)。首次运行从官方 Release 下载并核对发布资产的 SHA-256；可执行文件缓存到系统用户缓存目录，运行时配置和下载压缩包自动清理。

## 负载与指标

每个配置启动新进程，确认就绪，完成 10 个预热请求，静置 1 秒并观察空闲资源 1 秒，再运行短连接、单连接和并发负载。不同轮次轮换配置顺序。

- 短连接延迟从连接映射端口到收到完整的 64 字节回显，包含反向配对与首个往返。分位数使用 nearest-rank，再按轮次取中位数。
- 吞吐按收到的回显载荷计数一次，单位 MiB/s；bulk 写入块为 64 KiB，连接在计时前建立，逐字节检查数据。
- CPU 以一个逻辑核心为 100%；CPU 核秒/GiB 按每轮有效吞吐归一化后，再取中位数与范围。
- RSS 每 200ms 采样，负载阶段取采样峰值。隧道为客户端与服务端之和，代理独立统计；多个进程求和会重复计入共享页，不等于 heap 或 PSS。
- 隧道与代理的阶段峰值之和不一定同时发生，不能视为精确的同时总峰值。

所有完成轮次均保留，失败会中止运行。负载发生器与回显服务也会竞争本机 CPU，吞吐包含整条本地链路的调度与内核成本。

资源采样依赖 `ps`，工具支持 macOS 和 Linux；实际运行平台以报告为准。macOS CPU 累计时间精度为 0.01 秒，Linux 的 `ps` 通常只有 1 秒，短时 CPU 比较应谨慎。

## 运行

需要 Go、Git、`ps`，以及首次下载时的网络访问。在仓库根目录运行：

```sh
go run ./cmd/sertun-bench -trials 3 -duration 6s -short-count 100 -parallel 8 \
  -output bench/results/local.json
```

`-scenarios` 可选逗号分隔的上述场景。结果文件已存在时拒绝覆盖；各负载串行执行，运行期间避免其他构建、下载或压测。

优化前后对照可使用 `-baseline-ref`，工具从指定提交导出源码，以同一 Go 编译器和构建参数重建，不改变当前 checkout：

```sh
go run ./cmd/sertun-bench -scenarios direct -trials 4 -duration 4s \
  -baseline-ref 34d5a97 -output bench/results/direct-local.json
```

复制微基准仅测单个转发阶段，不与完整隧道吞吐混用。内存微基准预热缓冲池，用于观察分配，不代表进程 RSS：

```sh
go test ./internal/streamcopy -run '^$' -bench BenchmarkTCPCopy -benchmem -benchtime=2s -count=3
go test ./internal/streamcopy -run '^$' -bench BenchmarkCopyBuffers -benchmem -benchtime=1s -count=3
```

JSON 记录环境、工具版本和摘要、编译器、负载参数、全部轮次与逐请求延迟，同名 Markdown 提供汇总。官方二进制和本机 sertun 的编译器可能不同；报告保留这个差异，不将其全部归因于实现。

## 结果索引

| 结果 | 范围 | 原始数据 |
| --- | --- | --- |
| [当前直连](results/2026-10-19-direct-single.md) | 三种配置，各三轮，6 秒 | [JSON](results/2026-10-19-direct-single.json) |
| [当前 Mihomo](results/2026-10-19-mihomo.md) | 三种路径、三种配置，各三轮，6 秒 | [JSON](results/2026-10-19-mihomo.json) |
| [复制微基准](results/2026-10-19-streamcopy.txt) | 最终实现各三轮 | 原始文本 |
| [历史初始基线](results/2026-10-18-macos-arm64.md) | 四种路径，共 36 组，3 秒 | [JSON](results/2026-10-18-macos-arm64.json) |
| [历史直连优化](results/2026-10-18-direct-copy.md) | 四种配置，共 16 组，4 秒 | [JSON](results/2026-10-18-direct-copy.json) |

结果对应测量时的源码和二进制摘要；不能将不同次测量的绝对值当作严格的优化前后增益。当前实现与资源边界见 [验证记录](../VALIDATION.md)。

真实节点需要单独记录 RTT、丢包、带宽、协议、版本和服务器条件，使用独立结果文件。公开报告只保留复现所需的信息，不包含订阅、凭据、私人地址、主机名或个人路径。
