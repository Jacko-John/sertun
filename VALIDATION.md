# 验证记录

日期：2026-10-19。功能、构建和性能分开记录。业务通道复用还没实现。

## 本地检查

在 macOS arm64、Go 1.27.2 上跑过：

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- 两份示例配置的 `-check`
- Linux、Windows、macOS 的 amd64 / arm64 纯 Go 构建

行为测试覆盖直连和本地 HTTP CONNECT 的双向转发、并发配对、半关闭、身份与服务拒绝、重连、取消、配对超时、资源上限和一次性请求。单向复制测试补了部分读写、EOF 与错误、CONNECT 缓冲保留，以及未知包装的复制语义。

[三系统 CI](https://github.com/Jacko-John/sertun/actions/workflows/ci.yml) 在 Ubuntu、Windows、macOS 上跑 vet、race 测试和构建。发布前要求对应提交的检查通过。六个目标平台的交叉构建不等于六个平台都实际运行过，也不构成各系统的性能证据。

## Linux 容器检查

2026-10-19 在 OrbStack 29.8.2 的 linux/arm64 容器里跑了一遍，镜像 `golang:1.27.2-alpine`，源码只读挂载。这只验证 Linux 行为，不代表裸机或目标发行版的性能。

- `gofmt -l`、`go vet ./...`、`go test ./...`、`go test -race ./...` 通过。
- `TestReverseTCP` 连续 20 次通过，覆盖直连和 HTTP CONNECT；`internal/server` 在 race 下连续 10 次通过。
- `BenchmarkTCPCopy` 单轮 1 秒：原生 TCP 对 sertun 14872.78 MB/s，标准库 14220.06 MB/s；含待读字节的包装 sertun 14232.38 MB/s，标准库 6854.05 MB/s。两组都是 0 allocs/op。容器 CPU 由 OrbStack 虚拟化，数值不能和 macOS 报告直接比较。
- 同一测试在 `strace -f -e trace=splice` 下出现 22 次 `splice` 调用，其中一次请求长度为 1 MiB，确认 Linux 原生路径实际走了 splice。
- Linux 二进制构建和 `version` 输出正常。

容器和拉取的镜像在测试后删除，OrbStack 已停止。宿主机上原有的 `hagb/docker-atrust` 镜像和已退出容器没有改动。

## v0.1.0 发布包

六个目标都用 Go 1.27.2、`CGO_ENABLED=0` 构建，产物里的系统、架构和构建信息已经核对。每个归档只包含对应可执行文件、两份示例、使用说明和依赖许可声明；tar 归档所有者统一为 `root:root`，没有个人构建路径或工作区元数据。六份 SHA-256 全部通过。

macOS arm64 包解压后，`version` 输出 `sertun v0.1.0 (go1.27.2 darwin/arm64)`，两份示例配置都通过 `-check`。其他架构只做了构建和包内容检查，实际运行以三系统 CI 为准。

## v0.1.1 发布包

六个目标均使用 Go 1.27.2、`CGO_ENABLED=0` 构建，`file` 和 `go version -m` 核对了系统和架构。归档内容与 v0.1.0 相同：对应可执行文件、两份示例、使用说明和依赖许可声明；tar 所有者统一为 `root:root`，没有个人构建路径或工作区元数据。六份 SHA-256 全部通过，包大小约 2.0–2.3 MiB。

macOS arm64 包解压后，`version` 输出 `sertun v0.1.1 (go1.27.2 darwin/arm64)`，两份示例配置都通过 `-check`。其他架构只做了构建和包内容检查，实际运行以三系统 CI 为准。v0.1.1 是维护版本，没有新增完整链路性能测量，性能结论仍引用上面的 v0.1.0 数据。

## 完整链路性能

环境是 Apple M5 Pro、18 个逻辑 CPU、24 GiB 内存、macOS 27.0.1 arm64。sertun 用 Go 1.27.2；frp v0.71.1 和 Mihomo v1.19.32 是 Go 1.26.8 官方二进制，编译器差异没有控制。

直连和 Mihomo 测量用的是同一个 sertun 产品二进制，SHA-256 为 `6f3c6437a2ba474b3cf7a04850d5cf4a17e7494c2266eaf39090988272582506`。测量对应发布前的源码快照；原始报告保留了当时的 Git 元数据和二进制摘要，没有改写成发布产物的版本或哈希。

每个配置三轮，每个吞吐阶段 6 秒，100 个 64 字节短连接，8 条并发流。所有回显校验通过。报告保留全部轮次、中位数、范围、短连接分位数，以及隧道和代理的资源数据：

| 结果 | 范围 | 原始数据 |
| --- | --- | --- |
| [直连对照](bench/results/2026-10-19-direct-single.md) | 三种配置，共 9 组 | [JSON](bench/results/2026-10-19-direct-single.json) |
| [Mihomo 对照](bench/results/2026-10-19-mihomo.md) | DIRECT / AnyTLS / HY2，三种配置，共 27 组 | [JSON](bench/results/2026-10-19-mihomo.json) |

frp 两种配置都关掉了额外 TLS、业务加密和压缩。早期配置写的是 `poolCount=0`，但 v0.71.1 会把它补成 1，所以这些结果的有效工作连接池是 1，不能当成完全禁用预建连接。生成器现在显式写 1，汇总数值没变。

数据说明：sertun 的空闲隧道 RSS 更低，AnyTLS 多连接和 frp 独立连接接近；frp 复用在部分单连接负载和 HY2 多连接里吞吐更高。HY2 并发的具体原因还没定位。

所有端点都在本机，AnyTLS / HY2 两端也是。负载发生器和回显服务会占本机资源，但不计入隧道或代理的 CPU / RSS。CPU 成本按每轮有效回显数据归一化后再汇总。隧道和代理的 RSS 峰值之和不代表它们在同一时刻的总峰值。完整方法见 [benchmark 说明](bench/README.md)。

## 复制微基准

macOS arm64、Go 1.27.2，两条回环 TCP 连接，只测一个转发阶段，写入块 64 KiB。每项跑 2 秒、三轮，吞吐是 Go benchmark 的十进制 MB/s：

| 场景 | 标准库中位数 | streamcopy 中位数 |
| --- | ---: | ---: |
| 原生 TCP | 4732.42 | 5858.66 |
| 含待读字节的 TCP 包装 | 4716.90 | 5844.12 |

包装对照模拟旧 CONNECT 包装隐藏复制方法的情况，不包含完整代理链路。池预热后的内存基准用来看分配；池未命中仍可能分配，不能据此宣称全程零分配。纯内存长流没有显示出吞吐优势。

[原始输出](bench/results/2026-10-19-streamcopy.txt)保留了最终实现的三轮结果和源码摘要。这些数字不代表 Linux splice、Windows 或完整隧道的性能。

分级缓冲每个方向从 32 KiB 开始，累计传输 1 MiB 后切到 256 KiB，每个升级方向多占 224 KiB 工作缓冲。复制结束后缓冲回池，闲置池条目可以由 GC 回收。

## 历史结果

较早的测量保留下来用于追溯，不和当前结果混在一起汇总：

- [初始四链路基线](bench/results/2026-10-18-macos-arm64.md)，36 组，产品代码对应 `373b28b`。
- [直连复制优化对照](bench/results/2026-10-18-direct-copy.md)，16 组，优化前后用同一个 Go 编译器，和两种 frp 配置交错运行。

## 尚未验证

真实公网节点、丢包与高 RTT 条件，代理中断后的恢复和外层半关闭兼容性，Linux / Windows 宿主机上的完整链路性能（Linux 容器内的复制微基准和 race 测试已补），以及计划中的业务通道顺序复用。交付范围见 [演进规划](ROADMAP.md)，测试增量按 [测试原则](docs/rules/TESTING.md) 来。

## 维护记录

2026-10-19 对代码注释、控制面结构和 benchmark 工具做了一轮整理，并重写了文档。数据面（`streamcopy`、`relay`）没有改，产品转发、协议和关闭语义保持不变。整理后重新跑了 `gofmt -l`、`go test ./...`、`go test -race -count=1 ./...`、`go vet ./...`、`staticcheck ./...`、六个目标的交叉构建和两份示例配置的 `-check`，全部通过。`internal/server` 在 race 下重复 10 次通过；新增了部分绑定失败、运行期监听器失败后的端口回收测试，以及复制对非法写入计数的契约测试。`wire.Read` 跑了 5 秒 fuzz，约 150 万次执行，没有崩溃或残留 corpus。

`streamcopy` 微基准复查仍是 0 alloc/op；单次数字受机器负载影响，不作为新的前后对照结论。benchmark 工具用 `direct` 场景做了一次冒烟运行（1 轮、200ms、少量短连接、2 条并发流），报告生成、场景选择、TCP/UDP 端口预留和内存字段都正常；这不是新的性能对照，结论仍以上面的 v0.1.0 测量为准。

`dist/v0.1.0/` 的发布包没有重建，仍对应原来的 v0.1.0 构建提交。
