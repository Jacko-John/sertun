# 验证记录

代码验证日期：2026-10-18。

以下证据覆盖 [项目演进规划](ROADMAP.md) 的 P1、本轮 P2 增量和 P3 本地基线。验证方式与结果表达遵循 [测试原则](docs/rules/TESTING.md)。

## 已执行

- 在 macOS arm64、Go 1.27.2 上完成构建和测试。
- `go test ./...`：通过。
- `go test -race ./...`：通过，未报告数据竞争。
- `go vet ./...`：通过。
- 两份示例配置通过命令行 `-check` 校验。
- 纯 Go 交叉构建通过：Linux、Windows、macOS，分别覆盖 amd64 与 arm64。

测试覆盖的是 MVP 所需行为：直接连接和本地 HTTP CONNECT 测试代理下的真实 TCP 转发、并发配对、半关闭、身份与服务配置拒绝、断线重连、取消与关闭、待配对超时、连接上限和一次性请求。小型单元测试补充 CONNECT 的认证、取消、响应缓冲与长度边界，以及控制帧边界和配置字段校验。

## 多系统 CI

提交 `373b28baf85993bf516880de29a864e73514974f` 的 [GitHub Actions 运行](https://github.com/Jacko-John/sertun/actions/runs/39061607734)已核实为成功。Ubuntu、Windows、macOS 三个 runner 均执行并通过 `go vet ./...`、`go test -race ./...`、`go build ./cmd/sertun`。这提供三个系统上的功能测试证据，不代表六种架构均已实际运行或经过性能测试。

## 本地性能与实际代理链路

在 Apple M5 Pro、24 GiB RAM、macOS 27.0.1 arm64 上，对 sertun、frp v0.71.1 独立连接和复用模式完成 4 种路径 × 3 种配置 × 3 轮，共 36 组测量：直接 TCP、Mihomo DIRECT、本地 Mihomo AnyTLS、本地 Mihomo HY2。Mihomo 固定为 v1.19.32，两种加密协议均运行真实客户端与服务端。

每组独立进程，10 个预热请求、1s 静置、1s 空闲观察、100 个 64 字节短连接请求、3s 单连接和 3s 的 8 连接回显吞吐。全部回显检查通过；记录隧道与代理的空闲 / 负载 RSS、CPU、吞吐和连接到完整首个回显的延迟。结果按全部轮次取中位数，保留范围与逐请求数据。

报告见 [完整结果](bench/results/2026-10-18-macos-arm64.md)、[原始 JSON](bench/results/2026-10-18-macos-arm64.json)，方法见 [benchmark 说明](bench/README.md)。生产转发代码仍对应 `373b28b`；测量时工作区的修改为 benchmark 与文档，JSON 中单独记录了产品源码未修改。sertun 由 Go 1.27.2 构建，frp / Mihomo 官方二进制由 Go 1.26.8 构建，编译器差异未控制。

benchmark 的小型测试验证 CPU 时间解析、分位数与跨轮汇总不丢弃极值，以及真实回环请求；不设置机器相关的性能门槛。完整矩阵是外部二进制、协议配置与进程生命周期的运行验证。

## 直连单连接优化

macOS arm64 上完成四轮交错对照：当前工作区候选、Git `34d5a97` 基线、frp 独立连接与复用，共 16 组。两个 sertun 均用 Go 1.27.2 和相同构建参数编译，每阶段 4s，100 个短连接，并记录 8 连接负载作为资源增长检查。数据见 [报告](bench/results/2026-10-18-direct-copy.md)与 [JSON](bench/results/2026-10-18-direct-copy.json)。测量时 `product_source_dirty=true` 表示候选优化尚在工作区；基线由 Git 提交独立导出，二进制摘要分别记录。

单连接吞吐中位数为优化前 444.1、优化后 553.7 MiB/s；单位数据的隧道 CPU 成本为 4.90、3.74 核秒/GiB。单连接负载 RSS 中位数从 32.78 降至 25.91 MiB；8 连接负载 RSS 从 33.16 增至 36.91 MiB。工作缓冲更大与池化降低短连接分配同时存在，不能把一次 RSS 差异解释为所有并发规模的内存下降。

已有端到端测试增加 1 MiB 升级阈值前、阈值处、阈值后的 EOF 请求与响应，覆盖直接连接、CONNECT 和半关闭；沿用现有取消、关闭和连接限制用例。统计测试扩展为包含基线变体，并验证逐轮归一化后再取中位数。macOS 的 `BenchmarkTCPCopy` 使用真实回环 socket 比较标准复制和生产策略，其结果只描述一个复制阶段。

本轮本地 `go test ./...`、`go test -race ./...`、`go vet ./...`、复制微基准均通过。优化后的产品纯 Go 交叉构建再次通过 Linux / Windows / macOS 的 amd64、arm64 六个目标；这不构成其他系统采用该缓冲策略的性能证据。

## 尚未执行

- Linux、Windows 的性能测量，以及其余架构的实际运行验证；benchmark 资源采样当前支持 macOS 和 Linux，只在前者完整运行过。
- 真实公网 AnyTLS / Hysteria 2 节点的部署验证、RTT / 丢包 / 带宽受限场景。
- 实际外层协议下的服务端先发数据、半关闭、代理中断与恢复专项验证。
- 长时间稳态内存、高并发容量，以及 sertun 与 frp 使用相同编译器的实现对照。

交叉构建、回环网络测试和真实代理部署分别提供不同证据，不互相替代。后续按实际部署或发现的问题更新本记录，不增加无明确目的的重复测试。
