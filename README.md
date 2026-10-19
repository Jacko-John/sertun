# sertun

sertun 是一个轻量的 TCP 反向端口映射工具。客户端和服务端是同一个可执行文件，适合放在 AnyTLS、Hysteria 2 这类外层代理后面，转发 SSH、Web 等服务。

客户端启动后向服务端注册。有人访问映射端口时，服务端通知客户端回连，两条连接配对完成后直接转发原始字节。直连、HTTP CONNECT 和现有的 TUN / 路由都能用。传输加密交给外层代理，sertun 自己不做。

## 下载

发布包在 [GitHub Releases](https://github.com/Jacko-John/sertun/releases)。v0.1.1 提供六个目标：

| 系统 | 架构 | 文件 |
| --- | --- | --- |
| Linux | amd64 / arm64 | `sertun_0.1.1_linux_<arch>.tar.gz` |
| macOS | amd64 / arm64 | `sertun_0.1.1_darwin_<arch>.tar.gz` |
| Windows | amd64 / arm64 | `sertun_0.1.1_windows_<arch>.zip` |

包里有可执行文件、两份示例配置和[依赖许可声明](THIRD_PARTY_NOTICES.txt)，运行时不需要安装 Go。发布页的 `SHA256SUMS` 可以用来校验下载文件。

## 快速开始

下面的部署把服务端放在代理出口那台机器上。

1. 解压发布包，照着 [服务端示例](examples/server.toml) 和 [客户端示例](examples/client.toml) 各写一份 `server.toml`、`client.toml`。
2. 运行 `sertun token` 生成 token，两端填同一个值。客户端 ID 和服务列表也要一致。
3. 在代理出口机器启动服务端：

   ```sh
   sertun server -c server.toml
   ```

4. 在内网机器启动外层代理，让客户端配置里的 `proxy` 指向它的本地 HTTP 代理端口，然后启动客户端：

   ```sh
   sertun client -c client.toml
   ```

示例把服务端的 `60022` 转到客户端的 `127.0.0.1:22`，把 `60800` 转到 `127.0.0.1:8000`。连服务端对应的端口就能用这些服务。

命令里的 `sertun` 换成实际路径：Linux 和 macOS 是 `./sertun`，Windows 是 `.\sertun.exe`。

### 地址与加密

- `proxy` 是客户端本机的 HTTP CONNECT 代理，例如 `http://127.0.0.1:7890`，支持 URL 里的 Basic 认证。
- 配了代理时，`server` 由代理出口访问。服务端和出口在同一台机器上就写 `127.0.0.1:7000`，代理要允许连接这个地址。
- `proxy = ""` 走直连，`server` 必须从客户端可达。已经有 TUN 或路由时也这样配。
- 隧道接入口默认只监听回环地址。跨机器部署要放在可信网络里，或者靠外层代理提供加密。sertun 不加密 token 和业务数据。
- 配了代理就不会在故障时改走直连，也不会读 `HTTP_PROXY` 之类的环境变量。

```sh
sertun server -c server.toml -check
sertun client -c client.toml -check
sertun client -c client.toml -debug
sertun version
```

`-check` 只校验配置，不监听端口，也不测网络通不通。默认值、连接上限和出错时的行为见 [配置说明](docs/CONFIGURATION.md)。

## 与 frp 的对比

2026-10-19 在 Apple M5 Pro、24 GiB 内存、macOS 27.0.1 arm64 上做了一组本地对照。sertun 用发布前的源码快照，frp 是 v0.71.1，代理用 Mihomo v1.19.32。每个配置跑三轮，每个吞吐阶段 6 秒，另外发 100 个短连接。下表是三轮中位数，八连接列是八条流的总吞吐，单位 MiB/s。

| 路径 | 配置 | 单连接 | 8 连接 |
| --- | --- | ---: | ---: |
| 直连 | sertun | 505.9 | 499.3 |
| 直连 | frp 独立连接 | 411.4 | 504.4 |
| 直连 | frp 复用 | 626.5 | 438.7 |
| Mihomo DIRECT | sertun | 305.0 | 354.4 |
| Mihomo DIRECT | frp 独立连接 | 285.3 | 430.6 |
| Mihomo DIRECT | frp 复用 | 630.0 | 274.6 |
| AnyTLS | sertun | 219.1 | 291.6 |
| AnyTLS | frp 独立连接 | 214.3 | 298.3 |
| AnyTLS | frp 复用 | 397.1 | 197.9 |
| HY2 | sertun | 97.3 | 37.2 |
| HY2 | frp 独立连接 | 96.5 | 37.8 |
| HY2 | frp 复用 | 85.6 | 87.4 |

sertun 的直连空闲隧道 RSS 约 23.5 MiB，frp 两种配置约 46 MiB，都是客户端加服务端，不含外层代理。AnyTLS 的八连接吞吐比 frp 复用高 47.3%，和 frp 独立连接差不多；算上 Mihomo 的 CPU 后，三者分别是 6.74、7.67、21.55 核秒/GiB。

frp 复用在直连单连接、AnyTLS 单连接和 HY2 并发这三个场景更快，HY2 八连接大约是 sertun 的 2.35 倍。吞吐、CPU 和内存的取舍并不总是一致，选型时要一起看。

所有端点都在本机，AnyTLS / HY2 两端也是。这些数字只反映本机处理成本，不能直接套到公网、丢包链路或其他系统。sertun 用 Go 1.27.2 构建，frp 和 Mihomo 的官方二进制是 Go 1.26.8，编译器差异没有控制。frp 对照关掉了额外 TLS、业务加密和压缩，工作连接池设成 1，两种配置只有复用开关不同。

逐轮数据、资源指标和复现方法见 [直连报告](bench/results/2026-10-19-direct-single.md)、[Mihomo 报告](bench/results/2026-10-19-mihomo.md)和 [benchmark 说明](bench/README.md)。

## 构建

需要 Go 1.27.2 或更新版本：

```sh
go build -o bin/sertun ./cmd/sertun
go test ./...
go test -race ./...
go vet ./...
```

Windows 构建时把输出改成 `bin/sertun.exe`。Linux 和 macOS 可以用 `make build VERSION=v0.1.1` 构建精简二进制，用 `make dist VERSION=v0.1.1` 生成六个平台的发布包和校验文件，细节见 [发布流程](docs/RELEASING.md)。

## 当前限制

目前只转发 TCP，每次访问单独建一条业务连接。控制会话断了，相关业务连接一起结束；客户端重连后能接新访问，但已经断掉的 TCP 会话不会恢复。真实公网节点、代理故障恢复和外层半关闭还没验证，范围见 [验证记录](VALIDATION.md)。

下一步是[演进规划](ROADMAP.md)里的业务通道顺序复用，空闲 60 秒后关闭。已经实现的行为在 [架构说明](docs/ARCHITECTURE.md)。开发约定见 [代码规范](docs/rules/CODING.md)、[测试原则](docs/rules/TESTING.md)和 [工作流程](docs/rules/WORKFLOW.md)。
