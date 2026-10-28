# 术语表

> 状态：**随讨论演进**（起草 2026-10-22，按 2026-10-28 讨论更新）。标记 ✅ 的是代码里已经成立的定义，🚧 的是尚未实现或仍有待定细节的词；职责方向已确认不等于能力已实现。
>
> 新词在这里定下来之前不要进别的文档——[maturity.md](maturity.md) 和 [boundary.md](boundary.md) 的论证都依赖术语是稳定的。

## 已成立的词 ✅

| 词 | 定义 | 锚点 |
|---|---|---|
| **Workspace（旧仓库上下文）** | 旧 Git CLI 中一个仓库在本机的沙箱上下文，id 由仓库路径派生；概念上称 RepositoryContext，与通用执行工作区区分 | `commands.md`、[G0](g0-contract.md) |
| **Task** | 受控执行和资源归属单位。旧 Git CLI 对应 worktree/分支/容器/sidecar/internal 网络；通用任务对应 `tasks` 命名空间中的隔离目录及运行资源，不要求 Git | `internal/task`、[通用任务](general-tasks.md) |
| **Sidecar** | Task 所在 internal 网络上的普通对端，**不是网关**。包括 squid（HTTP/HTTPS 代理）、services（临时依赖）、forwarder（单 TCP 目标），以及 port 暴露端口的转发容器 | `boundary.md` §0 |
| **prep** | 把「取」变成「存」：在 build 期（有凭据、无 agent）把外部依赖固化进镜像或 volume | `boundary.md` §2 |
| **委托人（principal）** | 对 agent 行为负责的主体。今天的前提是 **机器的主人 = 委托人** | `maturity.md` §0 |
| **daemon** | 一个 Docker daemon。**sbx 的隔离单位**——不变量守的是 Task 与外界，不守同一 daemon 上 Task 之间 | `commands.md` 的 daemon 归属说明 |
| **Provider** | Claude/Codex 的启动、认证和会话适配；Task 创建后不能借 --agent 切换 | `internal/agent/provider.go` |
| **Execution Service** | 一次性 CLI 中的生命周期服务，统一编排准入、准备、运行/停止、产物收集、独立验证、本地交付、阶段记录与恢复；运行期工具授权待 G5/G6 | `internal/execution` |
| **任务信封（envelope）** | 公开 sbx.execution/v1 输入：schema_version/task_id/prompt 及可选 base/fresh/timeout_seconds；仓库与策略来自当前目录和本地配置 | `automation.md` |
| **Attempt** | 同一逻辑 Task 的一次执行，每次生成独立 attempt_id；重试不代表幂等重放 | `automation.md` |
| **Runner** | 可被外部脚本/调度器调用的本地执行入口；底层调用执行服务，不要求常驻 worker | `automation.md` |
| **执行记录（Execution Record）** | 独立于 Task StateDir 的版本化阶段事实与 attempt 历史；用于中断查询和恢复门禁，不是 stdout 事件日志或业务产物 | `internal/execution/record.go`、[G1](g1-implementation.md) |
| **RepositoryContext** | G0 对当前 `workspace.Workspace` 仓库上下文的概念名；保留现有 Go 名称和路径派生 id | [G0](g0-contract.md) |

## 提案中的词 🚧

| 词 | 候选定义 | 未决问题 |
|---|---|---|
| **Worker** | 一台机器 + 一个 daemon + 一个常驻 sbx 进程 | 常驻进程与今天的一次性 CLI 是同一个二进制的两种模式，还是两个东西？ |
| **Node（节点）** | 拓扑中的一个可调度位置 | 与 Worker 是否严格 1:1？若 1:1，这个词就是冗余的，应当删掉一个 |
| **Master / 控制面** | 派发任务、收集事件的上游 | **是否属于 sbx 的交付范围**？还是只交付协议 + worker 侧实现？ |
| **租户（tenant）** | 一组共享同一份凭据与可见性的委托人 | 一个 Worker 是否恒为单租户？ |


## 执行模型的词（含已实现与后续范围）

参照系是**批作业 + 物化产物传递**（CI pipeline / Argo Workflow），**不是流处理**。理由：任务有界、agent 不确定且不可重放、工作区是双向的。详见讨论结论。

| 词 | 候选定义 | 状态 |
|---|---|---|
| **通用 runner 类型** | 当前公开入口只承诺 agent 执行；未来非 agent 程序是否需独立能力模型由真实场景决定 | 未实现；不与已成立的 Runner/provider 混用 |
| **TaskSpec** | 通用任务声明，描述输入、agent、输出、完成要求和交付；与可信注册表分开 | ✅ `sbx task` 已接入阶段性 v2；工具字段及 G7 完整发布待后续 |
| **Source / ExecutionWorkspace** | Source 准备输入，ExecutionWorkspace 承载执行；与 RepositoryContext 区分 | ✅ files / Git revision / empty 副本；旧 Git CLI 保留 worktree |
| **ArtifactSet** | 按明确输出范围收集的稳定产物快照及清单，带摘要与来源；区别于失败回滚用的状态快照 | ✅ 完整工作区/选定输出快照、patch、内容摘要与显式回收 |
| **产物 Sink / DeliveryReceipt** | Sink 将通过验证的产物交付到目标并返回回执；独立于内部 EventSink | ✅ 目录/Git 本地交付、持久化意图、查询与独立重试；v1 仍无 artifact delivery，远端发布待后续 |
| **完成判据 / Evaluator** | 任务声明完成要求，独立验证器给出结论和证据 | ✅ 文件/范围/格式/结构、受限容器测试和人工判断；外部业务状态验证待 G6 |
| **Tool Gateway / GrantRef** | 工具调用入口按可信授权引用约束操作、目标和数据范围，代用凭据并记录副作用回执；项目或模型不能自行授予权限 | 方向已确认，授权生命周期与部署方式待定稿 |

上述通用执行概念的实施顺序与退出条件见 [执行内核 Roadmap](execution-roadmap.md)。

| 词 | 候选定义 | 状态 |
|---|---|---|
| **builder** | 沙箱初始化行为 | ❌ **不入场**：`prep` 已是唯一实现，改名无收益 |
| **edge** | **前一个 task 的 sink 成为后一个 task 的 source**——编排期的数据依赖，**不是运行期的网络通道**。两个 task 不同时活着，不需要在 internal 网络上打洞 | 形态已定，优先级未定 |

**不属于 edge 的：**到 master 的连接。它属于 worker 进程，容器不应知道 master 存在（ADR 0020（本地 ADR） 推论 2）。

**sbx 里唯一真正的「流」是事件流**（上报）：单向；当前 v1 经本地 stdout 交付，交付失败需结合进程码判断，消费者可位于本地或上游调度端。流处理的直觉用在控制面成立，用在数据面不成立。

## 撞名告警

「**Task**」在今天的 sbx 里已有严格定义（见上）。集群语境里说「daemon 中存在多个 task」如果指的是同一个东西，就**不要另造词**；如果指的是「master 派下来的工作项」（可能一个工作项 → 一个 Task，也可能 → 多个），那它是另一个概念，必须另起名（建议 **Job**），否则后面每一句话都要靠上下文消歧。
