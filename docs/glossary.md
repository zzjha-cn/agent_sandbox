# sbx 术语表

[首页](index.html) · [场景教程](walkthrough.md) · [设计](design.md) · [命令](commands.md) · [术语](glossary.md)

以下定义对应当前实现；同名词在 Git 便捷入口与通用执行模型中的差异单独说明。命令语法见 [命令手册](commands.md)。

## 任务与执行

| 术语 | 定义与边界 |
|---|---|
| **Task** | 逻辑任务、资源和恢复事实的归属单位。通用任务由 SBX_HOME/task_id 定位，不要求 Git。 |
| **TaskSpec** | v2 JSON 任务声明；选择已注册的资源、工具和完成要求，不提供授权。 |
| **task_id** | 任务稳定标识；不能换 ID 绕过尚未核对的外部写。 |
| **Attempt / attempt_id** | Task 的一次执行及其标识。run 新增 attempt，verify/deliver 延续既有 attempt。 |
| **RepositoryContext** | Git 便捷入口从当前仓库推导的上下文；代码中仍名为 workspace.Workspace。 |
| **Workspace / ExecutionWorkspace** | 前者在旧 Git CLI 中指仓库上下文，后者指 Agent 的可写执行目录；阅读时需结合入口。 |
| **main Task** | 省略 Git 任务名的特例，直接使用仓库根，无独立分支。 |
| **worktree** | Git 便捷任务的独立工作树；通用 Git Source 副本不要求包含 .git。 |
| **Provider** | Claude/Codex 的启动、认证、会话和状态适配。不是工具连接器。 |
| **headless / detach** | headless 是无交互任务执行；detach 是启动后不等待终态。启动返回不等于完成。 |
| **Execution Service** | 一次性 CLI 内的生命周期编排服务，不是常驻 worker。 |
| **阶段 / phase** | admission、prepare、execute、collect、evaluate、deliver、finalize 等持久化边界。 |

## 输入、能力与成果

| 术语 | 定义与边界 |
|---|---|
| **Source** | 准备输入、保存选择范围、内容摘要、revision 和授权绑定的适配器。 |
| **empty** | 省略 Source 时准备的空工作区；不会执行 git init。 |
| **基线 / baseline** | 执行前输入清单和摘要，支持差异验证与输入一致性检查。 |
| **Grant / 可信注册表** | 操作者的资源授权及引用映射；注册于 execution-policy.toml，任务只能缩小选择范围。 |
| **ToolGateway / MCP** | 前者逐次核验工具权限、代用凭据；后者是向 provider 暴露受控工具的协议。当前动态工具只读。 |
| **Connector** | 对具体外部服务的调用适配，例如 record_http；不是任意插件执行框架。 |
| **operations** | 任务预先声明的外部写操作，由宿主在 Agent 成功且沙箱停止后提交。 |
| **ArtifactSet / snapshot** | 冻结的产物及清单、摘要与来源；区别于仍可变的工作副本。 |
| **outputs / patch** | 前者选择文件产物，后者是可选差异文件。允许修改范围与交付范围不是同一概念。 |
| **Criterion / Evaluator** | 前者声明完成判据，后者独立验证；支持文件、测试、人工和外部业务状态。 |
| **needs_review** | 必须由操作者查看证据后显式 approve/reject 的验证状态。 |
| **Sink** | 消费稳定产物并交付到授权目标的适配器；目前为本地目录或本地 Git。 |
| **DeliveryKey / DeliveryReceipt** | 交付幂等身份及可核对回执；相同 key 不能改绑目标或产物。 |
| **git_local** | 本地 Git 分支交付；不隐式 push、merge 或修改目标 checkout。 |

## 持久化与恢复

| 术语 | 定义与边界 |
|---|---|
| **Execution Record / FileStore** | 独立于 Agent 状态目录的持久化事实、attempt 历史及恢复门禁；不是日志文本。 |
| **intent** | 已落盘的操作提交意图；进程中断后不能据此认定从未提交。 |
| **unknown** | 外部结果尚未确认；先查询核对，不能自动重发。 |
| **recovery / next_actions** | 根据已记录事实给出的恢复状态与建议；不构成授权。 |
| **reconcile / apply** | reconcile 查询原回执与业务状态；apply 是显式提交已证明未提交且仍有授权的操作。 |
| **stop / recover** | stop 取消当前活动；recover 独立停止并核对孤立执行，保留业务不确定性。 |
| **clean / prune** | task clean 回收专属运行资源；task prune 清理符合保留条件的指定快照。两者都不等于删除任务历史。 |
| **runtime_retired** | 已确认专属资源全部清理的标记；保留原策略身份，允许新 runtime 重新准入。 |
| **活动 token** | 绑定本次 run/continuation 的随机停止身份，防止旧停止请求误停下一活动。 |

## 部署与边界

| 术语 | 定义与边界 |
|---|---|
| **SBX_HOME** | 本地配置、执行记录及资源关联目录，默认 ~/.sbx；新通用资源绑定 home 摘要。 |
| **Docker daemon / namespace / owner** | 运行资源的 daemon、身份命名空间和操作者归属。标签防止误操作，不对抗 Docker API 管理员。 |
| **Sidecar** | Task 网络上的代理、服务或转发容器；不是通用网络网关。 |
| **Squid / allowlist / dedicated** | HTTP(S) 代理、域名允许列表与任务专属代理模式；个人默认 open，不默认限制所有出站流量。 |
| **prep** | 在没有 Agent 的构建阶段固化私有依赖，再向任务播种。 |
| **services / forward / port** | 任务临时依赖、到单个 TCP 目标的连接、把任务端口暴露到宿主回环地址。三者方向和风险不同。 |
| **组织策略 / policy digest** | 管理员安装的权限上限及内容摘要；与个人授权取交集，不能补出缺失 grant。 |
| **EventSink / JSONL** | 执行事件出口及逐行 JSON 编码；与文件产物 Sink 不同。 |
| **outbox / collector** | 本地私有事件待投递队列与远端接收服务；本地入队不等于远端已收到。 |
| **at-least-once / 幂等** | 至少一次投递可能重复；通过稳定身份、回执和业务核对避免重复效果，不宣称通用 exactly-once。 |
| **schema** | 机器契约的结构描述；Go 运行时校验与策略授权仍为权威。 |
| **Worker / Master / tenant** | 常驻执行者、控制面与多租户等未来部署概念；当前没有因此实现调度平台或多租户隔离。 |
