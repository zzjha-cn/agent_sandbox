<h1 align="center">sbx</h1>

<p align="center">面向 Claude Code 和 Codex 的受控任务执行运行时。</p>

<p align="center">
  <a href="https://zzjha-cn.github.io/agent_sandbox/"><b>项目主页与 Wiki</b></a> ·
  <a href="README.md">English</a> · <b>简体中文</b> ·
  <a href="docs/walkthrough.md">场景教程</a> · <a href="docs/commands.md">命令手册</a>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white">
  <img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  <img alt="Providers" src="https://img.shields.io/badge/providers-Claude%20%2B%20Codex-brightgreen">
</p>

sbx 在 Docker 沙箱中运行 Agent，并管理任务周围的流程：准备输入、控制访问、记录执行、验证结果和交付产物。你可以交互式修改代码，也可以从脚本或其他系统提交结构化任务。

| 入口 | 适用场景 | 工作区与完成语义 |
|---|---|---|
| `sbx run <task>` | 在 Git 仓库中交互式编码 | 独立 worktree 和 `sbx/<task>` 分支；由你 review 并合并 |
| `sbx run <task> -p "…" --jsonl` | 脚本化、无交互的 Git 任务 | Git worktree 和机器事件；Agent 成功不自动等于业务完成 |
| `sbx task run --spec task.json` | 报告、代码任务、工具查询或声明式外部操作 | 文件、Git 或空输入；显式产物、判据和可选本地交付 |
| `sbx run --spec task.json` | 通用任务的便捷入口 | 与 `sbx task run --spec` 使用相同提交路径 |

Git 便捷流程和通用任务共用执行服务，但工作区与结果语义不同。`sbx run` 省略任务名时选择 **main Task**，直接使用仓库根目录。

## 快速开始：运行一个 Git 任务

需要 Go 1.27、Git、运行中的 Docker daemon，以及 Claude Code 或 Codex 的有效登录态，或受支持模式下的 API key。daemon 应由一个可信操作者独占；互不信任的操作者需要独立 daemon 或 VM。

```sh
git clone https://github.com/zzjha-cn/agent_sandbox.git
cd agent_sandbox/core
make build
# 将 core/bin 加入 PATH，也可以一直使用二进制的绝对路径。
export PATH="$PWD/bin:$PATH"
sbx --version
```

检查 daemon 归属，再登录计划使用的 provider：

```sh
sbx isolation status --json
# 仅在确认旧 daemon 属于自己且尚未登记时接管：
# sbx isolation adopt
sbx login codex --status
# 如有需要，导入本机 Codex auth.json，或走登录流程：
# sbx login codex --import ~/.codex
# sbx login codex
```

空 daemon 会在 sbx 首次创建资源时登记归属。不要删除归属标记来绕过身份不匹配。凭据保存在该 daemon 的共享 `sbx-home` 卷中；`sbx login` 省略 provider 时选择 Claude。

```sh
cd ~/code/my-project
sbx trust --show                 # 查看项目配置（如果存在）
# 项目配置需要确认时，审阅后执行 sbx trust。
sbx run --agent codex fix-login
```

按 `Ctrl-b`，松开后按 `d` 可离开交互会话，任务继续在容器中运行。恢复时选择相同 provider；检查工作区后，由你自己合并分支：

```sh
sbx ls
sbx attach fix-login
sbx path fix-login
sbx stop fix-login
sbx run --agent codex fix-login  # 使用相同 provider 恢复
sbx done fix-login               # 回收任务运行资源和工作树，保留分支
git merge sbx/fix-login
```

`sbx drop` 还会删除任务分支，具有单独的确认步骤。使用前请看[场景教程](docs/walkthrough.md)和[命令手册](docs/commands.md)。

## 通用任务：从输入到验证和交付

通用任务可以在 Git 仓库外运行。操作者先在 `$SBX_HOME/execution-policy.toml` 中注册获准使用的 Source、判据、工具和 Sink；TaskSpec 选择这些引用并可收窄范围，不能自行授予权限。

[通用任务手册](docs/general-tasks.md#1-可信注册表与任务声明)提供了完整的报告示例，包含文件 Source、验证、人工审核和本地目录 Sink。替换示例路径并保存 `report-task.json` 后：

```sh
sbx version --json
sbx schema task > task.schema.json
sbx admit --spec report-task.json
sbx task run --spec report-task.json
sbx task status monthly-report --json
sbx task logs monthly-report --tail 30
# 当记录显示需要人工审核时：
sbx task approve --spec report-task.json --reviewer operator-name
```

准入检查是只读操作，不预留资源；run 会再次检查。Agent 在隔离副本中工作。声明 outputs 时，sbx 会先停止运行环境，再冻结产物快照、独立验证，并交付到已授权的本地目录或 Git 目标。本地 Git 交付不会自动 push 或 merge；没有文件输出的任务也可用外部操作回执和业务状态作为证据。

机器调用时，stdout 输出版本化 JSONL 事件，诊断信息写入 stderr。应结合进程退出码和持久化的执行、验证、交付状态判断结果。Schema 描述数据结构，运行时仍会执行语义检查和授权。详见[自动化协议](docs/automation.md)与 [schema 目录](docs/schemas/README.md)。

## 工具、外部写入与恢复

- **只读工具**：受支持的固定版本 Claude/Codex 可通过宿主网关调用 `records.get`。每次调用检查当前 grant、目标、字段和限制，凭据留在宿主；需要同步 headless 和 dedicated proxy。见[动态工具](docs/dynamic-tools.md)。
- **声明式写入**：TaskSpec `operations` 预先声明记录更新。Agent 成功且沙箱停止后，宿主先持久化意图再提交。回执丢失或无法确认时保持 `unknown`，先用 `sbx task reconcile --spec task.json` 查询核对。`apply` 是显式操作，仅用于已证明未提交且仍有授权的情况。见[外部写入说明](docs/commands.md#外部写入与核对)。
- **任务管理**：`sbx task list`、`status`、`logs` 用于查看通用任务；`task stop` 取消活动，`task recover` 核对孤立执行，`task clean` 回收任务专属运行资源，同时保留工作副本、快照、日志和回执。`task prune` 单独回收符合条件的产物快照。参数可用 `sbx task <command> --help` 查询。

新 attempt、容器停止或运行资源被清理，都不能证明外部写入没有发生。应保留原记录，先核对 unknown，再决定是否继续提交。

## 配置与边界

个人配置位于 `~/.sbx/config.toml`；设置 `SBX_HOME` 时使用对应目录。Git 任务还会读取项目和工作区配置层。列表通常合并、标量按层覆盖；组织权限上限单独生效，只能收紧结果。

```toml
# ~/.sbx/config.toml
default_agent = "codex"       # claude 或 codex
profile = "web-go"

[network]
mode = "allowlist"            # 默认是 open
# upstream = "http://host.docker.internal:7890"
```

`sbx config show` 展示有效值与来源；`sbx trust` 用于审阅项目配置。Task 网络是 internal，HTTP(S) 使用 Squid。显式 service、forward 和 port sidecar 有各自的连接路径与权限，因此代理日志不代表所有可能连接的完整记录。网络模式默认是 **open**；需要更窄范围时，应配置 allowlist 和相应授权。见[网络与宿主边界](docs/boundary.md)。

Git worktree 和依赖卷有助于隔离日常工作，但仓库 Git 数据及选定的共享状态仍会按配置暴露。Task 标签和容器隔离不能约束拥有 Docker API 权限的操作者。挂载、Git 保护、转发服务和凭据的具体边界见[设计文档](docs/design.md#7-信任与网络边界)。

管理员可以安装固定的 root 所有策略 `/etc/sbx/organization.toml`，限制网络、Source、Sink 和工具范围。受管执行使用同步 TaskSpec；策略更新或撤销会取消受监督活动，后续操作重新检查当前授权。团队事件先写入私有 outbox，通过 `sbx events status` 查看、`sbx events flush` 显式导出。中心 collector 和策略分发由部署方提供。见[组织策略命令](docs/commands.md#组织策略与事件上报)。

## 文档

| 文档 | 内容 |
|---|---|
| [项目主页与 Wiki](https://zzjha-cn.github.io/agent_sandbox/) | 架构图，以及个人、团队和基础设施使用场景 |
| [场景教程](docs/walkthrough.md) | 准备、Git 与通用任务、工具、审核、交付和恢复 |
| [设计文档](docs/design.md) | 执行流程、抽象职责、信任与部署边界 |
| [命令手册](docs/commands.md) · [CLI 参数参考](docs/cli-reference.md) | 按场景查命令及完整 CLI 帮助 |
| [术语表](docs/glossary.md) | Task、attempt、grant、回执、恢复等术语 |
| [通用任务](docs/general-tasks.md) · [动态工具](docs/dynamic-tools.md) | TaskSpec、资源注册和只读工具配置 |
| [自动化协议](docs/automation.md) · [Schemas](docs/schemas/README.md) | 机器协议、状态、退出码与数据契约 |
| [安全边界](docs/boundary.md) · [发布指南](docs/PUBLISHING.md) | 网络/宿主风险与公开 Wiki 导出 |

## 当前范围与开发

当前范围包括本地 Docker 执行、Claude/Codex、Git 会话、同步通用任务、本地交付、受控只读工具、声明式记录写入、恢复和组织权限上限。远端产物发布、中心 collector、受管交互/后台执行、常驻 worker 和多租户控制面尚未提供。真实 provider 和业务连接器需要在部署环境单独验收；受控测试服务不代表兼容任意生产 API。

```sh
cd core
make test          # 单元测试
make test-docker   # Docker 集成测试
make lint          # golangci-lint；未安装时执行 vet 和格式检查
make e2e           # Claude 端到端流程，需要登录
```

在 `core` 中执行 `make release` 可构建发布归档。静态站点仅导出公开文件的方法见[发布指南](docs/PUBLISHING.md)。如果 GitHub Pages 已配置为直接发布 `docs/`，无需手动执行导出命令；推送前应确认该目录中提交的内容都适合公开。

## 许可

[Apache-2.0](LICENSE)。
