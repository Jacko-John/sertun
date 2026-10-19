# 发布流程

发布用的是已经实现并验证过的代码，后续规划不算当前版本的功能。提交、推送和发布都要有任务里的明确授权。

## 发布前

1. 看一遍产品、示例、规划和公开文档，确认版本范围一致；不能带本地路径、真实凭据、私人地址或临时调试文件。
2. 跑 `go test ./...`、`go test -race ./...`、`go vet ./...`，再检查示例配置。
3. 构建六目标发布包，核对归档内容、版本输出和 SHA-256，并在本机跑一遍可执行文件的配置校验。在发布说明或验证记录里写明各目标二进制大小；空闲 RSS 引用已有 benchmark 报告，不单独造数。
4. 对应提交在 Linux、Windows、macOS CI 全部通过后，再给同一个提交打版本标签、上传资产。

纯文档整理不需要补性能测量。性能声明引用已有原始数据和适用范围。

## 构建产物

在仓库根目录运行，需要 Go、POSIX shell、`tar`、`zip`，以及 `sha256sum` 或 `shasum`：

```sh
make dist VERSION=v0.1.1
```

也可以直接跑 `sh scripts/dist.sh v0.1.1`。输出在 `dist/v0.1.1/`，包括 Linux / macOS 的 tar.gz、Windows 的 zip，共六个包和 `SHA256SUMS`。

每个包里只有对应平台的可执行文件、两份示例配置、简短使用说明和依赖许可声明。构建使用 `CGO_ENABLED=0`、`-trimpath`、`-buildvcs=false` 和精简链接参数，不嵌入本机源码路径或工作区状态。构建环境和 Go 版本仍会影响产物，不承诺不同工具链得到相同哈希。

校验下载文件：

```sh
cd dist/v0.1.1
shasum -a 256 -c SHA256SUMS
# Linux 也可以用 sha256sum -c SHA256SUMS
```

Windows 用 `Get-FileHash <压缩包> -Algorithm SHA256`，再和发布的校验文件比对。

## 文档放哪

发布说明放 `docs/releases/`，只写这个版本的能力、下载目标和必要限制。性能证据放 `bench/results/`，开发规则放 `docs/rules/`。本地构建输出和临时配置由 `.gitignore` 排除，源码树里不提交二进制。
