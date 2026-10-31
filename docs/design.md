# sbx 架构与设计

[首页](index.html) · [场景教程](walkthrough.md) · [设计](design.md) · [命令](commands.md) · [术语](glossary.md)

本文描述当前实现，机器输入与执行记录统一使用 v2。sbx 是一次性 CLI 驱动的受控 Agent 执行内核，支持 Claude Code 和 Codex。Git 交互与通用任务共用执行服务，但工作区选择和完成语义不同。

## 1. 从使用方式理解架构

| 入口 | 输入与工作区 | 结果与使用场景 |
|---|---|---|
| `sbx run fix-login` | 当前 Git 仓库，独立 worktree 与分支；省略名称为 main，直接使用仓库根 | 交互结对、代码修改，操作者 review 后合并 |
| `sbx run fix-login -p "…" --jsonl` | 同一 Git 工作区规则，headless | 脚本观察执行结果；退出成功不自动代表业务验收成功 |
| `sbx task run --spec task.json` | 显式 Source：文件、Git 副本或空目录 | 报告、代码任务、只读工具查询、声明式业务写入，按判据验证后交付 |
| `sbx run --spec task.json` | 与 task run 相同 | 同一个通用提交入口的便捷形式 |

通用任务不是把 Git 命令再包一层。它允许没有仓库、没有文件输出，完成证据可以来自产物、测试、人工审核或外部业务状态。

## 2. 系统分层

```text
调用方：人 / 脚本 / 外部调度器
  │ Git CLI 或 v2 TaskSpec
  ▼
CLI 适配层 ── 配置、输入解码、信号、JSONL、命令结果
  ▼
execution.Service ── 生命周期编排、任务锁、阶段事实与恢复门禁
  ├─ Source → ExecutionWorkspace → Provider（Claude / Codex）
  ├─ ToolGateway → 受控只读连接器
  ├─ 外部 operations → 停止沙箱后由宿主提交 → Receipt
  └─ ArtifactSet → Evaluator → Sink → DeliveryReceipt
       │             │          │
       └──── FileStore / snapshots / 安全审计 ────┘
可信上限：个人注册表 ∩ 已安装的管理员组织策略
运行资源：独占 Docker daemon → Task internal network → agent / sidecars
事件出口：公共 JSONL；受管模式另写私有 team-event outbox → 显式 flush
```

CLI 不自行决定产物已完成或外部写可以重试。执行服务掌握阶段顺序；适配器提供输入、运行、验证和交付能力。管理员策略与操作者授权决定任务实际上能选择什么。

## 3. 核心抽象及实现

| 抽象 | 职责 | 当前实现与限制 |
|---|---|---|
| TaskSpec | 声明任务需求，不携带授权 | `sbx.execution/v2`，source/agent/tools/operations/outputs/criteria/sink/limits |
| Source | 准备输入并记录版本、摘要和授权绑定 | files、Git revision/显式工作树选择；省略 Source 为 empty |
| ExecutionWorkspace | Agent 的可写工作副本 | 通用隔离目录；Git 便捷入口为 repo/main 或 worktree |
| Provider | 认证、启动、会话恢复与状态适配 | Claude/Codex；同一 Task 不借 provider 参数切换身份 |
| ToolGateway | 逐次核验运行期工具调用 | 受版本限制的只读 records.get / MCP HTTP，宿主持有凭据 |
| 外部操作连接器 | 提交与核对业务写入 | 预先声明 record_http operations；宿主在 Agent 成功且沙箱停止后提交 |
| ArtifactSet | 冻结产物与完整工作区清单 | 输出选择、patch、摘要、显式 prune；不是可写工作目录 |
| Evaluator | 独立判断完成要求 | 文件、测试、人工、external_state；测试使用独立副本 |
| Sink | 发布稳定产物 | local_directory、git_local；没有隐式 push/merge |
| FileStore | 保存执行事实并阻断危险重放 | 版本化记录、校验摘要、原子保存、任务/记录锁 |
| RuntimeManager | 停止、核对、清理专属资源 | stop/clean/recover；保留产物、工作副本、回执与历史 |
| EventSink | 输出执行事件 | stdout JSONL；与产物 Sink 职责不同 |

代码入口：`core/internal/cli`、`execution`、`agent`、`source`、`artifact`、`completion`、`sink`、`external`、`toolgate`、`governance`、`isolation`。

## 4. 一次通用任务如何完成

1. **声明与准入**：严格解码；检查 provider 能力、资源引用、有效授权、daemon 归属和未决恢复状态。admit 只是检查，run 在锁内再次准入。
2. **准备**：Source 复制输入、保留基线；创建或恢复隔离运行环境。Source 变化不能静默覆盖已有任务。
3. **执行**：运行 Agent，处理超时/取消；只读工具每次调用重新检查范围、时限和撤销状态。
4. **确认终态并停止写入者**：Agent 退出码与容器状态分别记录。停止容器不能证明远端业务已撤销。
5. **外部写与收集**：存在 operations 时先持久化意图，再由宿主提交；存在 outputs 时冻结工作区与输出快照。纯外部任务可以没有文件产物。
6. **验证与审核**：独立检查文件、测试与业务状态；主观要求进入 needs_review，不能伪造自动通过。
7. **交付与收尾**：按当前授权把稳定快照交付到 Sink，保存回执和各维度结果。终端输出成功不代替业务回执。

验证失败不重新运行 Agent；人工审核和交付恢复都消费原快照。完整命令见 [命令参考](commands.md)。

## 5. 两种状态必须分开

| 运行资源 | 持久化成果 |
|---|---|
| agent/proxy/服务容器、任务网络、专属依赖卷 | 输入基线、工作副本、执行记录、日志归档、产物、验证证据与操作/交付回执 |
| 可以停止、清理、在当前授权下重建 | 必须保留以支持核对与恢复，不能通过删除记录解除业务门禁 |

Task 是逻辑身份，Attempt 是一次执行。`task clean` 不删除逻辑历史；再次 run 创建新 attempt。`task verify/deliver/reconcile` 延续已有事实，不重放 prompt。Git 的 done/drop 是另一组资源与分支管理语义，不能当作通用 task clean 的别名。

## 6. 副作用与恢复

外部请求先保存 intent。收到丢失、错误或无法绑定的回执时保持 unknown；unknown 先 lookup/read 核对，不能当作“没有提交”。reconcile 不自动写入；只有证明尚未提交且当前写权限有效时，操作者才显式 apply。不同连接器的补偿语义不抽象成通用 Undo。

stop 通过私有活动 token 取消同步 run/continuation；recover 在两把独占锁内核对孤立运行资源并关闭中断 attempt，不从 run.exit 或容器停止推断成功。clean 在全部专属资源删除后标记 runtime_retired，之后按当前策略创建新容器。home 摘要标签阻止另一 SBX_HOME 接管同名资源。旧资源缺少标签时不自动领养。

输入基线缺失、记录损坏、未决产物清理等情况仍可能需要人工核对。恢复命令成功后仍需读取 recovery，不能仅看进程码。

## 7. 信任与网络边界

- 同一操作者独占 Docker daemon；不互信操作者使用独立 daemon/VM。Docker API 权限本身不受容器标签隔离。
- Agent 位于 Task internal 网络。HTTP(S) 经 Squid；Squid 是代理对端，不是任意 IP 流量的透明网关。forward/port 是独立 TCP 路径。
- 个人网络默认 open；allowlist 是显式配置。通用只读工具要求 dedicated proxy；受管模式限制为同步 TaskSpec。
- `execution-policy.toml` 注册宿主资源、验证规则、工具和目标；任务不能通过提交路径自行获得权限。
- 固定 `/etc/sbx/organization.toml` 是管理员安装的上限，只能收紧个人授权。更新、禁用、过期或失效取消当前受管活动；重新执行必须通过当前策略。
- 对共享身份、缓存、宿主可见配置和内网服务的具体风险，见 [安全边界](boundary.md)。

## 8. 协议与组织事件

机器输入/事件是 `sbx.execution/v2`，执行记录是 `sbx.execution-record/v2`。schema 做结构发现和离线校验，运行时由 Go 解码、语义校验和授权执行；schema 不授予权限。旧 v1 记录保留原样，需原二进制核对后另行迁移环境。

执行状态、验证状态、产物交付状态、事件发送状态分别表达不同事实。脚本应同时检查退出码和最终记录，不能只匹配 completed 文本。

组织事件先持久化到私有 outbox，显式 `events flush` 使用成员 token 上传。稳定事件 ID 支持至少一次投递；collector 必须认证、去重后确认持久化。当前不交付中心 collector 或策略分发服务。

## 9. 交付范围

当前提供本机 CLI、Docker 运行时、两种 provider、受控连接器、本地 Sink、组织上限及通用任务管理。受管交互/detach、远端 Sink、常驻 worker、队列调度、多租户控制面和自动灾难恢复均不在已实现范围。受控模型/API 测试不代替部署环境中的真实业务验收。
