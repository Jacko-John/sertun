# 发布流程

发布使用已有实现与验证结果；后续规划不作为当前版本功能。对外提交、推送和发布按任务中的明确授权执行。

## 发布检查

1. 审阅产品、示例、规划和公开文档，确认版本范围一致；不包含本地路径、真实凭据、私人地址或临时调试产物。
2. 执行 `go test ./...`、`go test -race ./...`、`go vet ./...`，检查示例配置。
3. 构建六目标发布包，检查归档内容、版本输出与 SHA-256，并在本机运行可执行文件的配置校验。
4. 对应提交的 Linux、Windows、macOS CI 全部通过后，发布同一提交的版本标签与资产。

不为纯文档整理追加性能测量；性能声明引用已有原始数据与适用范围。

## 构建产物

在仓库根目录运行，需要 Go、POSIX shell、`tar`、`zip`，以及 `sha256sum` 或 `shasum`：

```sh
make dist VERSION=v0.1.0
```

也可直接执行 `sh scripts/dist.sh v0.1.0`。输出位于 `dist/v0.1.0/`，包括 Linux / macOS 的 tar.gz、Windows 的 zip，共六个压缩包和 `SHA256SUMS`。

包内仅有对应平台的可执行文件、两份示例配置、简短使用说明与依赖许可声明。构建使用 `CGO_ENABLED=0`、`-trimpath`、`-buildvcs=false` 和精简链接参数，不嵌入本机源码路径或工作区状态。构建环境与 Go 版本仍决定产物，不承诺跨工具链得到相同哈希。

校验下载文件：

```sh
cd dist/v0.1.0
shasum -a 256 -c SHA256SUMS
# Linux 也可使用：sha256sum -c SHA256SUMS
```

Windows 可用 `Get-FileHash <压缩包> -Algorithm SHA256` 与发布的校验文件比对。

## 发布内容

发布说明维护在 `docs/releases/`，只描述该版本能力、下载目标与必要限制。性能证据保存在 `bench/results/`，开发规则保存在 `docs/rules/`。本地构建输出和临时配置由 `.gitignore` 排除，不提交二进制到源码树。
