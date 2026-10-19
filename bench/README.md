# 性能对照与复现

benchmark 用真实独立进程比较 sertun 和 frp。产品的转发路径不依赖这个工具；负载发生器和回显服务跑在基准进程里，不计入隧道和代理的资源。

## 场景与配置

| 场景 | 客户端到服务端的路径 |
| --- | --- |
| `direct` | 直接 TCP |
| `mihomo-direct` | HTTP CONNECT → Mihomo → DIRECT |
| `mihomo-anytls` | HTTP CONNECT → Mihomo 客户端 → 本地 AnyTLS → Mihomo 服务端 → DIRECT |
| `mihomo-hy2` | HTTP CONNECT → Mihomo 客户端 → 本地 Hysteria 2 → Mihomo 服务端 → DIRECT |

所有端点都绑回环地址。AnyTLS / HY2 跑真实协议，证书和凭据在运行时生成。这些结果只反映本机处理成本，不代表公网环境。

参与对照的配置：

- `sertun`：默认的独立业务连接。
- `frp-plain`：TCP，关掉额外 TLS、业务加密、压缩和 TCP 复用，工作连接池为 1。
- `frp-mux`：和 frp-plain 一样，只是打开 TCP 复用。

早期配置写的是 `poolCount=0`，frp v0.71.1 会把它补成 1；现在显式配置为 1。历史报告的有效连接池也是 1。依据：[配置默认值](https://github.com/fatedier/frp/blob/v0.71.1/pkg/config/v1/client.go#L153)、[零值处理](https://github.com/fatedier/frp/blob/v0.71.1/pkg/util/util/types.go#L17)。

固定对照版本是 [frp v0.71.1](https://github.com/fatedier/frp/releases/tag/v0.71.1) 和 [Mihomo v1.19.32](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.32)。第一次运行会从官方 Release 下载并核对 SHA-256；可执行文件缓存在系统用户缓存目录，运行时的配置和下载压缩包会自动清理。只跑 `direct` 时不下载 Mihomo。

## 负载与指标

每个配置都新起进程，确认就绪，发 10 个预热请求，静置 1 秒并观察 1 秒空闲资源，再跑短连接、单连接和并发负载。轮次之间会轮换配置顺序。

- 短连接延迟从连接映射端口算到收到完整的 64 字节回显，包含反向配对和第一个往返。分位数用 nearest-rank，再按轮次取中位数。
- 吞吐按收到的回显载荷计一次，单位 MiB/s。bulk 写入块 64 KiB，连接在计时前建立，收到的每个字节都会校验。
- CPU 以一个逻辑核心为 100%。CPU 核秒/GiB 先按每轮有效吞吐归一化，再取中位数和范围。
- RSS 每 200ms 采样一次，负载阶段取采样峰值。隧道是客户端加服务端，代理单独统计。多个进程求和会重复计算共享页，不等于 heap 或 PSS。
- 隧道和代理的阶段峰值不一定同时发生，不能当成精确的同时总峰值。

完成的轮次都会保留，失败会中止整次运行。负载发生器和回显服务也抢本机 CPU，所以吞吐里包含整条本地链路的调度和内核成本。

资源采样依赖 `ps`，只支持 macOS 和 Linux；实际运行平台写在报告里。macOS 的 CPU 累计时间精度是 0.01 秒，Linux 的 `ps` 通常只有 1 秒，短时间 CPU 比较要谨慎。

## 运行

需要 Go、Git、`ps`，第一次下载还要能访问 GitHub。在仓库根目录运行：

```sh
go run ./cmd/sertun-bench -trials 3 -duration 6s -short-count 100 -parallel 8 \
  -output bench/results/local.json
```

`-scenarios` 接受逗号分隔的场景名。结果文件已存在时会拒绝覆盖；各负载串行执行，运行期间别同时构建、下载或压测。

要比较优化前后，用 `-baseline-ref` 指定提交。工具会从该提交导出源码，用同一个 Go 编译器和构建参数重建，不会改动当前 checkout：

```sh
go run ./cmd/sertun-bench -scenarios direct -trials 4 -duration 4s \
  -baseline-ref 34d5a97 -output bench/results/direct-local.json
```

复制微基准只测一个转发阶段，不要和完整隧道吞吐混着看。内存微基准预热缓冲池，用来看分配，不代表进程 RSS：

```sh
go test ./internal/streamcopy -run '^$' -bench BenchmarkTCPCopy -benchmem -benchtime=2s -count=3
go test ./internal/streamcopy -run '^$' -bench BenchmarkCopyBuffers -benchmem -benchtime=1s -count=3
```

JSON 里记录环境、工具版本和摘要、编译器、负载参数、全部轮次和逐请求延迟；同名 Markdown 给出汇总。官方二进制和本机 sertun 的编译器可能不同，报告保留这个差异，不把它全部归因于实现。

## 结果索引

| 结果 | 范围 | 原始数据 |
| --- | --- | --- |
| [当前直连](results/2026-10-19-direct-single.md) | 三种配置，各三轮，6 秒 | [JSON](results/2026-10-19-direct-single.json) |
| [当前 Mihomo](results/2026-10-19-mihomo.md) | 三种路径、三种配置，各三轮，6 秒 | [JSON](results/2026-10-19-mihomo.json) |
| [复制微基准](results/2026-10-19-streamcopy.txt) | 最终实现各三轮 | 原始文本 |
| [历史初始基线](results/2026-10-18-macos-arm64.md) | 四种路径，共 36 组，3 秒 | [JSON](results/2026-10-18-macos-arm64.json) |
| [历史直连优化](results/2026-10-18-direct-copy.md) | 四种配置，共 16 组，4 秒 | [JSON](results/2026-10-18-direct-copy.json) |

结果对应测量时的源码和二进制摘要，不能拿不同次测量的绝对值当严格的优化前后增益。当前实现和资源边界见[验证记录](../VALIDATION.md)。

真实节点要单独记录 RTT、丢包、带宽、协议、版本和服务端条件，结果放独立文件。公开报告只保留复现需要的信息，不放订阅、凭据、私人地址、主机名或个人路径。
