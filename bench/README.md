# 性能对照与复现

benchmark 使用真实独立进程比较 sertun 与 frp，结果包含原始轮次和汇总表。负载发生器与回显服务运行在基准进程中，不计入隧道进程的 CPU / RSS；它们在所有配置中保持一致。

## 场景

| 名称 | 隧道客户端到服务端的路径 |
| --- | --- |
| `direct` | 直接 TCP |
| `mihomo-direct` | HTTP CONNECT → Mihomo → DIRECT |
| `mihomo-anytls` | HTTP CONNECT → Mihomo 客户端 → 本地 AnyTLS → Mihomo 服务端 → DIRECT |
| `mihomo-hy2` | HTTP CONNECT → Mihomo 客户端 → 本地 Hysteria 2 → Mihomo 服务端 → DIRECT |

所有端点绑定回环地址，运行时生成短期证书与凭据。Mihomo DIRECT 单独标记，避免与 AnyTLS / HY2 混淆。加密协议的两端也在同机，因此结果反映本机处理成本，不能外推到公网 RTT、丢包或远端 CPU。

## 对照配置

- **sertun**：独立业务连接，现有默认实现。
- **frp-plain**：TCP、关闭额外 TLS、业务加密、压缩和复用，连接池为零。
- **frp-mux**：与 frp-plain 相同，但启用 frp 的 TCP 复用。

关闭 TLS 的配置仅用于本机或已经有外层加密的对照路径。保留 frp-mux 是为了同时展示实际可选的连接复用取舍。

每轮为各配置启动新进程，预热并确认回显链路可用后再测量；不同轮次交错运行配置，降低固定顺序带来的偏差。生产转发代码不因 benchmark 改变。

## 指标

- 空闲隧道进程 RSS：客户端与服务端之和。
- 外层代理 RSS：Mihomo 两端或 DIRECT 代理之和，独立记录。
- 短连接 p50 / p95：从连接映射端口开始，到收到完整回显为止，覆盖反向配对与首个往返。
- 单连接与小并发 TCP 回显吞吐：按实际收到的有效载荷计数，单位 MiB/s。写入与回显均发生在链路内。
- 负载阶段的隧道与外层代理 CPU、RSS：CPU 以一个逻辑核心为 100%，RSS 为采样值，不代表操作系统分配内存总量。
- 单位有效数据的 CPU 成本：CPU 核秒 / GiB，越低越好。每轮先以 CPU 百分比和实际吞吐换算，再汇总中位数，不能用两项汇总值的比值代替。

每个请求检查回显，任何阶段失败都会终止本次运行，不生成成功报告。所有完成轮次保留，汇总采用中位数，同时保留范围供检查；短连接分位数使用 nearest-rank，汇总表取各轮分位数的中位数。

读取进程资源依赖系统 `ps`，第一版支持 macOS 和 Linux，已实际运行的平台以结果文件为准。RSS 每 200ms 采样，阶段内取峰值，再按轮次汇总；它包括程序映射和共享页，客户端与服务端求和会重复计入共享页，不等于 Go heap 或 PSS。CPU 使用累计时间之差，macOS 精度为 0.01s，Linux 的 `ps` 通常只有 1s，短时 CPU 比较应谨慎解读。吞吐负载下各配置完成的数据量不同，CPU 百分比应结合吞吐阅读。

## 运行

固定对照版本：[frp v0.71.1](https://github.com/fatedier/frp/releases/tag/v0.71.1)、[Mihomo v1.19.32](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.32)。工具从官方 GitHub Release 下载，校验发布资产的 SHA-256，二进制保存在系统用户缓存目录的 `sertun-bench` 下；下载压缩包和运行时配置会自动清理。运行需要 Go、Git、`ps` 及首次下载的网络访问，不需要管理员权限；请在仓库根目录执行。

```sh
go run ./cmd/sertun-bench -output bench/results/local.json
```

可用 `-scenarios direct,mihomo-direct` 先跑最小对照，或通过 `-trials`、`-duration`、`-short-count`、`-parallel` 调整负载。单轮短时间运行适合检查工具能否工作；公开对比使用多轮结果，并保持比较双方的参数一致。

每组先完成回显就绪检查与 10 个预热请求，静置 1s 后观察空闲资源 1s，再运行短连接、单连接、并发负载。bulk 使用 64KiB 写入块，连接在计时前建立，每个收到的字节均检查；负载发生器和回显服务也消耗本机 CPU，因此回环吞吐包含整条本地链路的调度与内核成本。

结果文件已存在时会拒绝覆盖；再次运行应指定新文件名。压测按场景和配置串行执行，建议保持机器空闲，避免其他构建、测试或下载与测量重叠。完整默认矩阵有 36 组，约需数分钟。

优化前后可在同一轮矩阵中比较。`-baseline-ref` 从仓库提交导出源码，用当前 Go 编译器和相同构建参数生成 `sertun-baseline`，与当前 `sertun`、frp 两种配置交错运行；不会修改当前 checkout。四种配置使用至少四轮，可使每种配置轮到每个执行位置。

```sh
go run ./cmd/sertun-bench -scenarios direct -trials 4 -duration 4s \
  -baseline-ref 34d5a97 -output bench/results/direct-local.json
```

macOS 的 TCP 复制微基准可单独运行，用于隔离缓冲策略和采样 CPU。它只包含一个转发阶段，不能将其吞吐用作整个隧道的性能宣传：

```sh
go test ./internal/relay -run '^$' -bench BenchmarkTCPCopy -benchtime=2s -count=3
```

生成的 JSON 包含环境、版本、二进制摘要与大小、实际构建使用的 Go 版本、参数、逐请求延迟和全部轮次；同名 Markdown 用于审阅。官方发布版与本机编译的 sertun 可能使用不同编译器，这属于指定二进制的部署对照，不能把差异全部归因于代码设计。结果不包含真实节点凭据、主机名或个人路径。

## 已记录基线

- [2026-10-18，macOS arm64](results/2026-10-18-macos-arm64.md)：四种链路、三种配置、三轮，共 36 组；[原始 JSON](results/2026-10-18-macos-arm64.json)。复现时使用上面命令并添加 `-duration 3s`。环境中的 `memory` 在 macOS 为 `hw.memsize` 的字节数，Linux 为 `/proc/meminfo` 的 `MemTotal`（含单位）。
- [2026-10-18，直连复制优化](results/2026-10-18-direct-copy.md)：优化前后两个 sertun 使用 Go 1.27.2 重建，与 frp 两种配置交错四轮，共 16 组；[原始 JSON](results/2026-10-18-direct-copy.json)。每个 bulk 阶段 4s，100 个短连接、8 条并发流；CPU 成本按各轮的有效回显载荷归一化。

本轮优化前的 CPU 采样包含单条与两条直连流的诊断负载，主要样本位于 socket 系统调用、网络轮询与运行时调度。Go 在 macOS 上的 TCP 复制回退到 32 KiB 用户态缓冲；仅向 `io.CopyBuffer` 提供较大缓冲还不够，TCP 的 `WriterTo` / `ReaderFrom` 会优先执行并忽略该缓冲，见 [Go 文档](https://pkg.go.dev/io#CopyBuffer)。生产策略只对 macOS 的原生 TCP 对隐藏这些方法，分级复用 32 / 256 KiB 缓冲；其他连接类型保留原有复制方法。

大流量方向每端的工作缓冲比原来多 224 KiB，两个方向均升级时每端多 448 KiB；`sync.Pool` 中的闲置缓冲可由 GC 回收。短流不会直接分配大缓冲。进程 RSS 包括 GC、映射和缓存，不能从这些工作缓冲大小直接推算，应同时检查报告内的空闲与负载采样。

## 真实节点的后续验证

当前命令只创建本地隔离链路。真实节点需单独记录服务端位置与硬件、RTT、丢包、可用带宽、节点协议和版本，在同一时段使用相同负载比较，并将结果另存。代理入口应只引用本地配置，不向公开报告写入订阅、凭据或私人地址。公网结果不能与本地结果混成同一组中位数。
