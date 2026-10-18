# sertun

轻量的 TCP 反向端口映射工具，适合通过现有 AnyTLS、Hysteria 2 等代理链路暴露 SSH、Web 等服务。

一个可执行文件兼任客户端与服务端，负责服务注册、连接配对和双向字节转发。支持直接 TCP、HTTP CONNECT，以及已有的 TUN / 路由；传输加密由外层代理承担。

## 下载

从 [GitHub Releases](https://github.com/Jacko-John/sertun/releases) 下载对应系统和架构的压缩包。v0.1.0 提供：

| 系统 | 架构 | 文件 |
| --- | --- | --- |
| Linux | amd64 / arm64 | `sertun_0.1.0_linux_<arch>.tar.gz` |
| macOS | amd64 / arm64 | `sertun_0.1.0_darwin_<arch>.tar.gz` |
| Windows | amd64 / arm64 | `sertun_0.1.0_windows_<arch>.zip` |

包内包含可执行文件、示例配置和[依赖许可声明](THIRD_PARTY_NOTICES.txt)，运行时不需要安装 Go。发布页的 `SHA256SUMS` 用于校验下载文件。

## 快速开始

以下部署让 sertun 服务端与外层代理出口位于同一台机器。

1. 解压发布包，参考 [服务端配置](examples/server.toml) 和 [客户端配置](examples/client.toml)，分别创建 `server.toml`、`client.toml`。
2. 运行 `sertun token`，将生成的 token 填入两端配置。两端的客户端 ID、服务名称和服务列表必须一致。
3. 在代理出口机器启动服务端：

   ```sh
   sertun server -c server.toml
   ```

4. 在内网机器启动外层代理，确认客户端配置中的 `proxy` 指向其本地 HTTP 代理端口，再启动客户端：

   ```sh
   sertun client -c client.toml
   ```

示例将服务端的 `60022` 映射到客户端的 `127.0.0.1:22`，将 `60800` 映射到 `127.0.0.1:8000`。访问服务端的对应端口即可使用这些服务。

命令中的 `sertun` 请替换为实际可执行文件路径，例如 Linux / macOS 的 `./sertun`，或 Windows 的 `.\sertun.exe`。

### 地址与加密边界

- `proxy` 是客户端本机的 HTTP CONNECT 代理地址，例如 `http://127.0.0.1:7890`，支持 Basic 认证。
- 使用代理时，`server` 是从代理出口访问的地址。与出口同机部署时可用 `127.0.0.1:7000`，外层代理需要允许访问这个目标。
- `proxy = ""` 使用直接连接，此时 `server` 必须从客户端可达。已有 TUN / 路由也使用这种配置。
- 隧道接入口默认监听回环地址。跨机器部署需使用可信或经过加密的网络路径，sertun 自身不加密 token 或业务数据。
- 配置代理后不会在故障时自动切换直连，也不读取 `HTTP_PROXY` 等环境变量。

```sh
sertun server -c server.toml -check
sertun client -c client.toml -check
sertun client -c client.toml -debug
sertun version
```

`-check` 校验配置而不监听端口，不验证网络连通性。完整默认值、连接限制与故障行为见 [配置说明](docs/CONFIGURATION.md)。

## 与 frp 的性能对照

2026-10-19，在 Apple M5 Pro、24 GiB RAM、macOS 27.0.1 arm64 上，比较当前复制实现与 frp v0.71.1；代理场景使用 Mihomo v1.19.32。每项三轮，每个吞吐阶段 6 秒，另有 100 个短连接请求。下表为三轮中位数，8 连接列为合计吞吐，单位 MiB/s。

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

sertun 的直连空闲隧道 RSS 约为 23.5 MiB，frp 两种配置约为 46 MiB，均为客户端与服务端之和，未包含外层代理。AnyTLS 的 8 连接吞吐比 frp 复用高 47.3%，与 frp 独立连接接近；计入 Mihomo 后，三者的 CPU 成本分别为 6.74、7.67、21.55 核秒/GiB。

frp 复用在本次直连、AnyTLS 单连接和 HY2 并发负载中有吞吐优势，HY2 的 8 连接吞吐约为 sertun 的 2.35 倍。选择时需要同时考虑吞吐、CPU 和内存。

所有端点均在本机，包括 AnyTLS / HY2 两端；这些结果反映本机处理成本。sertun 使用 Go 1.27.2，frp / Mihomo 官方二进制使用 Go 1.26.8，编译器差异未控制。frp 对照关闭额外 TLS、业务加密和压缩，工作连接池的有效配置为 1，仅复用开关不同。

完整范围、逐轮数据和资源指标见 [直连报告](bench/results/2026-10-19-direct-single.md)、[Mihomo 报告](bench/results/2026-10-19-mihomo.md)及 [复现说明](bench/README.md)。结果不能外推到公网、丢包链路或其他系统。

## 构建与开发

需要 Go 1.27.2 或更新版本：

```sh
go build -o bin/sertun ./cmd/sertun
go test ./...
go test -race ./...
go vet ./...
```

Windows 构建输出可改为 `bin/sertun.exe`。Linux / macOS 可用 `make build VERSION=v0.1.0` 构建精简二进制；`make dist VERSION=v0.1.0` 生成六个目标的发布包和校验文件，详见 [发布流程](docs/RELEASING.md)。

当前仅支持 TCP，每次访问使用独立业务连接。控制会话断开后，相关业务连接结束；重连可接收新访问，无法恢复已断开的 TCP 会话。真实公网节点、代理故障恢复和外层半关闭兼容性仍需补充验证，见 [验证记录](VALIDATION.md)。

[演进规划](ROADMAP.md)包含待实现的 60 秒空闲通道复用；[架构说明](docs/ARCHITECTURE.md)描述当前行为。[代码规范](docs/rules/CODING.md)、[测试原则](docs/rules/TESTING.md)和 [工作流程](docs/rules/WORKFLOW.md)维护开发约定，测试采用 MVP 加合理增量。
