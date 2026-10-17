# sertun

轻量的 TCP 反向端口映射工具。通过本地 HTTP CONNECT 代理使用 AnyTLS、Hysteria 2 等外层协议，也支持直接连接和已有的 TUN / 路由。

sertun 负责服务注册、连接配对与字节转发。外层代理独立运行，负责传输加密。第一版仅支持 TCP，工具自身不叠加多路复用。

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

仓库提供三个系统的 CI 配置。交叉构建只证明能够编译；各系统实际运行与真实代理部署仍需单独验证。当前验证记录见 [VALIDATION.md](VALIDATION.md)。
