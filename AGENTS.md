# 仓库入口与执行约定

## 必读与规则索引

开始任务先读 [项目演进规划](ROADMAP.md) 和 [工作流程](docs/rules/WORKFLOW.md)。以下是本仓库的开发规则，按任务涉及的范围阅读并遵循；用户明确指令优先。

| 修改范围 | 对应文档 |
| --- | --- |
| Go 代码、依赖、网络与资源生命周期 | [代码规范](docs/rules/CODING.md) |
| 测试设计、检查选择、性能与平台验证 | [测试原则](docs/rules/TESTING.md) |
| 规划、实现、修复、文档维护与交付 | [工作流程](docs/rules/WORKFLOW.md) |
| 架构、模块和现有行为 | [当前架构](docs/ARCHITECTURE.md) |

规则细节在对应文档维护，本文件保留入口与环境约定。

## 环境

- Go 构建、依赖、格式化和测试使用 Go 工具链。
- Python 与依赖操作始终使用 `uv`。
- JavaScript / TypeScript 执行与依赖操作始终使用 `bun` / `bunx`。
- 搜索使用 `rg`；需要覆盖隐藏或忽略文件时使用 `-uu`。
- 磁盘占用使用 `gdu`，不使用 `du`；`gdu -n -p <dir>` 输出排序摘要，`-s` 查看总量。
