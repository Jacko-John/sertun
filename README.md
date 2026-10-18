# sertun

轻量的 TCP 反向端口映射工具。通过本地 HTTP CONNECT 代理使用 AnyTLS、Hysteria 2 等外层协议，也支持直接连接和已有的 TUN / 路由。

sertun 负责服务注册、连接配对与字节转发。外层代理独立运行，负责传输加密。第一版仅支持 TCP，工具自身不叠加多路复用。

## 与 frp 的性能对照

适合已经运行 AnyTLS / HY2、只需要反向映射 SSH 或 Web 端口的部署。在本次 macOS arm64 基准中，sertun 的客户端与服务端空闲 RSS 合计为 **24.5 至 24.8 MiB**，比两种 frp 配置的各场景中位数低约 **46% 至 47%**。sertun 用一个 **4.88 MiB** 的二进制兼任客户端与服务端；本次对照的 frpc 为 14.45 MiB，frps 为 17.93 MiB。

环境：Apple M5 Pro、24 GiB RAM、macOS 27.0.1。sertun 基于 `373b28b`，frp v0.71.1，Mihomo v1.19.32。每组 3 轮，每轮新建进程，分别运行 3s 单连接、3s 的 8 连接负载和 100 个短连接。下表为各轮结果的中位数，吞吐按收到的回显载荷计数一次，单位 MiB/s。

| 链路 | 配置 | 空闲隧道 RSS（MiB） | 单连接（MiB/s） | 8 连接（MiB/s） | 短连接 p95（ms） |
| --- | --- | ---: | ---: | ---: | ---: |
| 直连 | sertun | 24.47 | 427.8 | 653.6 | 0.89 |
| 直连 | frp 独立连接 | 45.98 | 418.6 | 499.8 | 0.73 |
| 直连 | frp 复用 | 46.11 | 663.9 | 477.1 | 0.83 |
| Mihomo DIRECT | sertun | 24.52 | 305.6 | 472.2 | 1.25 |
| Mihomo DIRECT | frp 独立连接 | 46.16 | 315.0 | 333.1 | 0.87 |
| Mihomo DIRECT | frp 复用 | 46.45 | 608.6 | 281.1 | 0.80 |
| Mihomo AnyTLS | sertun | 24.58 | 230.3 | 298.2 | 1.25 |
| Mihomo AnyTLS | frp 独立连接 | 46.39 | 221.8 | 297.3 | 1.05 |
| Mihomo AnyTLS | frp 复用 | 46.19 | 425.3 | 192.7 | 0.97 |
| Mihomo HY2 | sertun | 24.75 | 83.5 | 38.4 | 1.42 |
| Mihomo HY2 | frp 独立连接 | 46.38 | 95.4 | 38.3 | 0.88 |
| Mihomo HY2 | frp 复用 | 46.05 | 88.5 | 88.7 | 0.90 |

sertun 在这次直连和 AnyTLS 的 8 连接负载中高于 frp 复用；frp 复用的单连接吞吐在四种链路中都更高，HY2 的 8 连接吞吐约为 sertun 的 2.3 倍。frp 的短连接 p95 也更低。选型时应同时考虑这些差异。

RSS 仅统计隧道客户端与服务端，Mihomo 开销单独记录，不能将上述降幅理解为整套代理的内存降幅。所有端点均在本机回环网络，包括真实运行的 AnyTLS / HY2 加密链路；结果不能推断公网、丢包链路或其他系统的性能。sertun 使用 Go 1.27.2，frp / Mihomo 官方二进制使用 Go 1.26.8，本次没有控制编译器版本。

[完整结果与波动范围](bench/results/2026-10-18-macos-arm64.md)包含 CPU 与外层代理；[原始 JSON](bench/results/2026-10-18-macos-arm64.json)保留全部 36 组结果、负载 RSS 与逐请求延迟。复现方法、指标定义与限制见 [benchmark 说明](bench/README.md)。

```sh
go run ./cmd/sertun-bench -trials 3 -duration 3s -short-count 100 -parallel 8 \
  -output bench/results/local.json
```

## 构建

需要 Go 1.27.2 或更新版本，运行时不需要 Go。

```sh
go build -o bin/sertun ./cmd/sertun
```

Linux / macOS 可使用 `make build` 构建去除调试符号的纯 Go 二进制；`make dist` 构建 Linux、Windows、macOS 的 amd64、arm64 版本。Windows 可用 `go build -o bin/sertun.exe ./cmd/sertun`。

## 使用

1. 参考 [服务端配置](examples/server.toml) 和 [客户端配置](examples/client.toml) 分别创建 `server.toml`、`client.toml`。
2. 运行 `sertun token`，把生成的 token 填入两端配置。两端的客户端 ID 和服务名也必须一致。
3. 在代理出口所在的机器启动服务端：

   ```sh
   sertun server -c server.toml
   ```

4. 在内网机器启动外层代理，确认它提供的本地 HTTP 代理地址，再启动客户端：

   ```sh
   sertun client -c client.toml
   ```

示例将远端 `60022` 映射到客户端的 `127.0.0.1:22`，将远端 `60800` 映射到客户端的 `127.0.0.1:8000`。访问远端对应端口即可使用本地服务。

`bin/sertun` 是构建输出路径；命令中的 `sertun` 表示你实际部署的可执行文件，可替换成其完整路径。

### 代理地址与目标地址

客户端的 `proxy` 是本机 HTTP 代理，例如 `http://127.0.0.1:7890`，可携带用户名和密码。第一版接入普通 HTTP CONNECT，不接入 HTTPS 代理或 SOCKS5。

**使用代理时，客户端的 `server` 是从代理出口访问的目标地址。** 如果 sertun 服务端与代理出口在同一机器，通常可使用 `127.0.0.1:7000`，但外层代理必须允许 CONNECT 到该地址和端口。

推荐让服务端的隧道接入口监听回环地址，只对外开放需要暴露的业务端口。跨机器连接隧道接入口需要可信网络路径，sertun 本身不提供加密。

设置 `proxy = ""` 使用直接连接，此时 `server` 必须从客户端可达。配置了代理时，代理故障不会自动切换为直接连接。sertun 不读取 `HTTP_PROXY` 等环境变量。

### 常用命令

```sh
sertun server -c server.toml -check
sertun client -c client.toml -check
sertun client -c client.toml -debug
sertun token
sertun version
```

`-check` 只解析和校验配置，不开启端口，也不验证网络连通性。配置中的未知字段会报错。

## 配置与运行行为

服务端的每个 `[[clients]]` 定义一个客户端及其 token，嵌套的 `[[clients.services]]` 定义允许暴露的服务与监听地址。客户端的 `[[services]]` 定义同名服务的本地目标。服务列表需完全一致；同一客户端 ID 仅允许一个在线控制会话。

以下配置可省略：

| 位置 | 配置项 | 默认值 | 用途 |
| --- | --- | --- | --- |
| 服务端 | `listen` | `127.0.0.1:7000` | 隧道接入口 |
| 两端 | `timeout` | `"10s"` | 连接和握手超时；服务端等待业务回连的超时 |
| 两端 | `heartbeat_timeout` | `"60s"` | 控制连接无消息时的超时 |
| 服务端 | `max_handshakes` | `64` | 同时进行的隧道接入握手上限 |
| 服务端的每个客户端 | `max_connections` | `128` | 等待中与转发中的业务连接总上限 |
| 服务端的每个客户端 | `max_pending` | `32`，不超过连接上限 | 待配对连接上限 |
| 客户端 | `proxy` | 空 | HTTP CONNECT 代理，空值表示直接连接 |
| 客户端 | `max_connections` | `128` | 本地业务连接上限 |
| 客户端 | `heartbeat_interval` | `"20s"` | 保活间隔 |
| 客户端 | `reconnect_min` | `"1s"` | 重连初始基础退避 |
| 客户端 | `reconnect_max` | `"30s"` | 重连最大基础退避 |

重连带少量随机扰动。心跳超时应大于保活间隔，并留出余量。认证错误和服务列表不匹配会退出，网络故障和暂时存在重复会话则自动重试。

控制会话中断时，该会话的业务连接会关闭，客户端重新注册后可建立新连接。已断开的 TCP 会话不会自动恢复。客户端离线、超过连接上限或配对失败时，服务端关闭对应的访问连接。

转发支持本地 TCP 半关闭；最终行为还取决于外层代理是否保留该语义。HTTP CONNECT 测试代理与真实 AnyTLS / HY2 的运行验证分别记录，不能互相替代。

## 开发与测试

项目的阶段目标、进展与后续取舍见 [项目演进规划](ROADMAP.md)，已采用的设计见 [当前架构](docs/ARCHITECTURE.md)。

开发规则独立维护：[代码规范](docs/rules/CODING.md)、[测试原则](docs/rules/TESTING.md)、[工作流程](docs/rules/WORKFLOW.md)。[AGENTS.md](AGENTS.md) 是后续协作者的规则入口。

```sh
go test ./...
go test -race ./...
go vet ./...
```

测试采用 MVP 加合理增量，具体行为基线和按改动选择检查的方式由测试原则维护。

Linux、Windows、macOS 的功能测试已在 CI 实际运行并通过；本地 Mihomo DIRECT、AnyTLS、HY2 的回显与性能测量已在 macOS arm64 完成。真实公网节点、故障恢复与外层半关闭仍需独立验证。具体证据与边界见 [VALIDATION.md](VALIDATION.md)。
