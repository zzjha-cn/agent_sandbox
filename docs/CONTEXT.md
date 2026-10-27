# sbx — AI 编码 Agent 沙箱运行时

> 当前行为按 2026-10-27 代码更新：Claude Code / Codex、执行服务和公开 v1 协议已实现。入口见 [命令手册](commands.md)、[自动化手册](automation.md)、[测试指南](testing.md)，实现说明见 [architecture.md](architecture.md)，整体设计见 [design.md](design.md)。旧里程碑和 ADR 是历史决策，不替代当前命令帮助。

## 定位

`sbx` 是一个本地的 AI 编码 Agent（Claude Code / Codex）沙箱运行时：把本地代码挂载进容器，让 Agent 在容器里以跳过权限确认的模式运行。

- **主场景**：无人值守的自主执行（长时间运行、多任务并行，例如在会话中跑 `/auto-it` 循环）。
- **兼容场景**：交互式结对编程。
- **不做**：远程 / 云端 / 多租户共享运行环境。团队成员各自在本机使用。
- **原则**：沙箱只是容器，不依赖宿主机操作系统特性；支持 macOS/Linux 部署；平台网络、权限和 rootless/VM 的行为仍需在目标环境验收。

## 术语表

| 术语 | 定义 |
|---|---|
| **Workspace** | 一个 git 仓库，即宿主机上的本地代码目录。 |
| **Task** | Workspace 上的一个工作单元，拥有独立的 git worktree 和分支。省略任务名使用 main，直接挂主工作目录；命名 Task 同时支持交互与 headless。 |
| **Sandbox** | Task 的运行时容器，生命周期跟随 Task：可停止 / 恢复，Task 收尾时销毁。 |
| **Profile** | 语言环境镜像（Dockerfile 或镜像名），只负责语言运行时和系统依赖。内置 `web-go`、`py-rust`。 |
| **Agent 层** | `sbx` 自动叠加在 Profile 之上的固定层：claude / codex CLI、tmux、git、非 root 用户、proxy 环境变量、入口脚本、状态 hooks。 |
| **派生镜像** | Profile + Agent 层构建出的镜像，命名为 `sbx/<profile>:<hash>`。 |
| **Egress Proxy** | 独立的 Squid 容器，是 Sandbox 唯一的出网通道，负责白名单放行和访问日志。有两种部署：**shared**（默认，全局一个，通过代理认证区分 Task）和 **dedicated**（每个 Task 一个 sidecar）。 |
| **网络模式** | `open`（**默认**，全部放行）或 `allowlist`（只放行白名单）。两种模式都经过 Egress Proxy；策略拦截层在两种模式下都生效。默认值 2026-10-08 从 allowlist 改过来（ADR 0005 修订）。 |
| **白名单层** | 内置层（LLM API 和 Agent 认证）+ 语言栈预设层（包源、镜像）+ 项目层。三层取并集，再减去**策略拦截层**（默认包含云端 MCP，ADR 0015）。 |
| **sbx-home** | 所有 Task 共享的 volume，存放 Agent 登录态、会话历史和缓存。 |
| **依赖遮盖** | 用每个 Task 独立的 named volume 盖住 worktree 内的依赖目录（如 `node_modules`、`.venv`、`target`），避免 Linux 二进制写回宿主机。 |
| **信任确认（trust）** | 宿主机侧 `~/.sbx/trust/` 记录你确认过的 `.sbx/` 内容哈希。内容变化后拒绝启动 Task，直到执行 `sbx trust`。 |
| **Task 状态** | `running`（Agent 工作中）/ `idle`（等待输入）/ `exited(agent)`（容器还在，但 Agent 已退出）/ `stopped`（容器已停止）/ `exited(code\|oom)`（headless 结束或被 OOM 杀掉）。 |

## 目录与配置布局

```
~/.sbx/
  config.toml                 # 个人全局配置：上游代理、API key 来源、默认 Agent、资源上限、max_running、on_idle/on_exit
  workspaces/<repo>.toml      # 个人对某个项目的覆盖（不进仓库）
  worktrees/<repo>/<task>/    # Task 的 worktree
  trust/                      # 信任哈希（不挂载进任何容器）

<repo>/.sbx/
  sandbox.toml                # 项目配置（提交进仓库，禁止出现密钥和个人路径）
  Dockerfile                  # 可选：项目自定义 Profile
```

配置合并顺序：内置默认 → `~/.sbx/config.toml` → `<repo>/.sbx/sandbox.toml` → `~/.sbx/workspaces/<repo>.toml`。列表取并集，其他值后者覆盖前者。

## 当前命令入口

完整 flags 与组合限制以 [commands.md](commands.md) 和当前二进制 `--help` 为准。

| 命令 | 作用 |
|---|---|
| `sbx run [task]` | 创建或恢复 Task，在容器内的 tmux 里启动 Agent TUI |
| `sbx run <task> -p "..."` | headless 模式运行一个 prompt，输出写进 Task 日志 |
| `sbx attach <task>` | 接入 Task 的 tmux 会话 |
| `sbx net denied [task]` | 按域名列出被代理拒绝的请求 |
| `sbx net allow <host>...` | 把域名加进白名单并热加载 |
| `sbx ls` | 列出 Task：状态、分支、提交数和 diff 统计、最后活动时间、网络被拒次数 |
| `sbx port <task> <port>` | 把容器端口映射到宿主机的随机空闲端口 |
| `sbx net denied [task]` / `sbx net allow <host>` | 查看被拒请求；把域名加进白名单 |
| `sbx login [claude\|codex]` | 默认 Claude；Codex 可设备码登录或显式导入 auth.json |
| `sbx run --agent codex <task>` | 本次选择 Codex，不修改配置，恢复要求原 provider |
| `sbx version --json` | 获取 schema 与 capabilities |
| `sbx admit --envelope <file\|->` | 只读检查，不预留资源 |
| `sbx run --envelope <file\|->` | v1 headless、墙钟超时和分类退出码 |
| `sbx isolation status [--json]` / `sbx isolation adopt` | 当前 daemon 归属与本人独占旧卷接管 |
| `sbx trust` | 确认 `.sbx/` 的当前内容 |
| `sbx upgrade` | 升级 Claude 并重建 Agent 层；新建容器生效 |
| `sbx done <task>` | 销毁容器、worktree 和依赖 volume，保留分支 |
| `sbx drop <task>` | 同上，并删除分支（需要确认） |

## 执行与用户边界

执行服务编排 create/resume、headless wait/stop、超时/取消、结构化事件和结果，CLI 保留配置、trust 和终端适配。`[isolation]` 只能由个人全局配置设置，namespace 绑定 daemon ID。旧卷需显式 adopt；共享身份与 owner 标记不由 Task 清理删除。不同 SBX_HOME 不能提供用户隔离。

## 决策索引

> ADR 放在 `docs/private/adr/`，不纳入版本控制（见 `.gitignore` 的 `private`），下面保留决策编号供本地追溯，不依赖未分发的 ADR 文件。

| # | 决策 |
|---|---|
| 0001（本地 ADR） | 用容器级隔离，不绑定特定 Docker 实现 |
| 0002（本地 ADR） | 每个 Task 一个 Sandbox，Workspace → Task → Sandbox |
| 0003（本地 ADR） | 用 worktree，主 `.git` 可写挂载（R1 已由 0019 缓解） |
| 0004（本地 ADR） | Agent 跑在容器内的 tmux 里，另有 headless 模式 |
| 0005（本地 ADR） | 用 Squid 做 Egress Proxy，当前 open 为默认，可切换 allowlist |
| 0006（本地 ADR） | 共享 sbx-home 存放登录态，宿主机配置只读挂入 |
| 0007（本地 ADR） | Profile 加自动叠加的 Agent 层，内置两个 Profile |
| 0008（本地 ADR） | 依赖目录用 volume 遮盖，缓存全局共享 |
| 0009（本地 ADR） | 配置分四层 |
| 0010（本地 ADR） | 用信任确认防止配置被篡改 |
| 0011（本地 ADR） | 状态、通知、回收，不做常驻守护进程 |
| 0012（本地 ADR） | 资源上限、并发上限、端口按需映射 |
| 0013（本地 ADR） | 用 Go 单二进制，通过 docker CLI 编排；Agent 免确认模式运行 |
| 0014（本地 ADR） | Egress Proxy 默认共享，可以配置为独占 |
| 0015（本地 ADR） | 默认拦截 claude.ai 云端 MCP 连接器 |
| 0016（本地 ADR） | 项目自动记忆：导入沙箱，手动导回 |
| 0017（本地 ADR） | 用具名「私有源」统一处理内网 / 私有化接入（已由 0018 的 prep/sidecar 分流取代，见 [boundary.md](boundary.md)） |
| 0018（本地 ADR） | 前置依赖用 prep 固化，活的依赖用 sidecar 开窄洞（设计见 [boundary.md](boundary.md)） |
| 0019（本地 ADR） | 用嵌套只读挂载封住 `.git` 的代码执行面（R1），修订 0003 |

## M0 验证结论（2026-10-04）

全部通过（当时 Codex 延后；当前支持状态以正文为准），详见 [spikes/M0-SUMMARY.md](../spikes/M0-SUMMARY.md)。资源默认值已定：Docker VM 10GB，每个 Task 2C/3g，`max_running = 3`（ADR 0012 修订）。
