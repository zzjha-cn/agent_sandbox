# 项目记忆与任务交接设计

[首页](index.html) · [当前架构](design.md) · [场景教程](walkthrough.md) · [命令参考](commands.md)

> 状态：M1 本地存储/审核/注入与 M2 交接/隔离提取已实现并通过受控测试；真实 Docker/模型验收仍待完成。M3 团队共享仍是提案。当前准确的命令和配置见 [项目记忆使用手册](project-memory.md)；下文未明确标注已落地的接口仍为目标契约。

## 0. 已采纳的模式与 M1/M2 实施边界

- **native**：个人默认，Codex 使用原生后台记忆，Claude 沿用原生记忆及既有同步。sbx 设置启动开关并保留会话关联，不接管原生生成文件的内部格式。
- **managed**：关闭 provider 原生自动记忆，由 sbx 选择已审核项目经验并保存、注入固定上下文。与组织受管模式相互独立。
- **off**：关闭原生自动记忆，不注入项目经验；不擦除旧会话内容。切换模式需要新会话，正在运行的会话需要先停止。

M1 使用宿主全局/workspace 配置和 Git flags，不新增 v2 TaskSpec 字段；通用任务仅使用宿主全局配置。组织执行默认 off、禁止 native；组织审核身份尚未接入，因此组织模式下项目记忆修改命令暂不开放。原生记忆导入将按候选审核处理，尚未实现。

为简化崩溃一致性，M1 每个项目使用单个原子 JSON 保存完整修订，而非下面目标布局中的多文件索引；项目名作为稳定本地 ID。独立 task-memory 保存启动前快照，供 memory status 查询，尚未加入执行记录字段。数据 schema 与使用说明已发布。

M2 的实际入口是 `memory handoff <task>`、`extract <task>`、`job/jobs`、`processing <task>`、`recover <job>` 和 `retry <job>`，均有明确的 Git/general 命名空间。事实从持久化记录投影；目标/进度备注由操作者明确附加，模型说明与建议保存在提取作业。提取采用无工具的宿主 Responses/Messages API，复用可信配置的 provider API key，要求显式 model；不复用订阅会话，也不自动扫描日志或原生记忆。材料/输出/调用数与超时均有预算，输出只有候选权限。

自动提取默认关闭，显式启用后在 managed 同步 headless attempt 关闭、释放执行锁之后有界等待。独立 processing/job 记录失败，不改变业务终态。冻结作业保存在 `$SBX_HOME/memory-extractions/<id>/job.json`，交接、人工备注与 processing 在 task-memory 下；尚无提取守护进程、团队审核或原生导入桥。以下更广的 Source/产物摘要选择、复核到期和共享布局仍是后续目标。

## 1. 背景与目标

目前 sbx 为 Codex 保存 task 对应的会话 ID，并在后续启动时使用该 ID 恢复会话。Codex 历史保存在共享 `sbx-home` 卷；Git 任务 done 后，任务状态与会话关联被删除，共享卷中的历史可能仍然存在。现有 `sbx memory pull` 是 Claude 项目自动记忆的宿主机同步入口，没有提供跨 provider 的项目知识管理。

本设计补齐两个能力：任务中断后有可检查的交接记录；新任务能够使用其他任务积累并经审核的项目经验。项目记忆由 sbx 管理，对 Codex、Claude 提供相同的选择、审核、版本与清理语义。

目标：

- 经验具有明确的项目范围、适用条件、来源和验证依据。
- 新任务只读取获准使用的知识，并记录实际使用的版本。
- 失败、取消、清理、崩溃恢复不丢失已持久化的知识，也不触发业务重放。
- 个人使用先在本地闭环；团队共享通过显式导出、审核、导入逐步扩展。

第一版不包含向量数据库、跨机器实时同步、中心记忆服务、自动修改项目规范或无审核的自动入库。记忆不保存凭据，也不提供外部业务操作授权。

## 2. 信息分层与权威边界

| 信息 | 内容 | 范围与主要用途 | 管理者 |
|---|---|---|---|
| 会话历史 | 对话、工具调用、上下文 | 同一会话继续执行 | Provider；sbx 保存关联 |
| 任务交接记录 | 目标、进度、未完成事项、验证结果、恢复入口 | task / attempt 的接手与恢复 | sbx |
| 项目记忆 | 测试方法、环境条件、稳定约束、已确认的故障经验 | 后续任务复用 | sbx 项目记忆服务 |
| 项目规范 | AGENTS.md、设计文档、团队规则 | 明确约束与确认决策 | 仓库及其评审流程 |

“还剩两个测试失败”属于交接；“集成测试依赖 PostgreSQL”可以成为经验候选。任务成功不代表其总结都正确；失败任务也可能产生有依据的经验。

记忆注入为带来源的参考资料，不能覆盖操作者指令、组织策略、工具授权或当前项目规范。发现冲突时标记并提示核对，不自动覆盖任何一方。需要把经验提升为规范时，另行提交仓库文档变更。

## 3. 抽象与接入位置

```text
CLI / TaskSpec
    ↓ 当前准入与项目授权
execution.Service
    ├─ Source → ExecutionWorkspace
    ├─ ProjectResolver → MemorySelector → MemorySnapshot
    ├─ Provider ← 本次记忆上下文
    ├─ 现有执行 → 外部操作 / ArtifactSet → Evaluator → Sink
    └─ HandoffWriter → CandidateExtractor → 候选持久化
                                              ↓ 显式审核
                                       ProjectMemoryStore
```

| 组件 | 职责 | 禁止承担的职责 |
|---|---|---|
| ProjectResolver | 把任务绑定到获准的项目身份 | 根据 prompt 或远程 URL 自动授权 |
| ProjectMemoryStore | 条目、版本、审核历史、项目锁与原子保存 | 启动 Agent、提交外部业务写入 |
| MemorySelector | 按项目、路径、标签和预算选择条目 | 任意跨项目搜索、修改条目 |
| MemorySnapshot | 固定本次注入的正文、版本、来源及摘要 | 作为项目记忆的可写主库 |
| HandoffWriter | 汇总执行事实与尚待处理的事项 | 用模型总结代替运行结果 |
| CandidateExtractor | 从授权材料生成结构化候选 | 自行批准、提升组织权限 |
| Provider 适配 | 将统一上下文交给具体 Agent | 决定项目知识的可信等级 |

建议新增 `core/internal/projectmemory`，保留现有 `core/internal/memory` 的 Claude 同步职责。执行服务负责阶段编排，CLI 负责命令适配；不把新能力实现成仅在 CLI 返回前运行的脚本。

## 4. 项目身份与存储

项目使用 sbx 生成的稳定 ID，通过受信任的本地注册表绑定。Git 主仓库与其 worktree 解析到同一绑定；仓库迁移或重新 clone 后需要显式绑定。remote URL 仅作为提示，不作为身份或访问权限证明。

通用任务通过获准的项目引用绑定；没有绑定时不读取或写入项目记忆，仍可产生任务交接记录。团队共享时，本地项目与导入来源项目也必须显式映射。

```text
$SBX_HOME/
  project-memory/
    projects/<project-id>/
      project.json
      entries/<entry-id>/<revision>.json
      candidates/<candidate-id>.json
      reviews/<review-id>.json
      index.json
      lock
  task-memory/<task-key>/<attempt-id>/
    context.json
    context.md
    handoff.json
    extraction.json
```

新目录与当前 Claude 同步使用的 `$SBX_HOME/memory/<key>.json` 分离。任务侧目录由宿主管理，独立于可删除的 Git task 状态；通用任务记录保存相应引用，Git 便捷入口使用相同的辅助记录契约。

Agent 仅接收本次上下文，不挂载整个知识库。优先使用宿主生成的只读文件和启动提示引用；具体 provider 适配必须验证新会话与 resume 都能收到上下文，不覆盖仓库原有指令文件。共享 daemon / 共享身份卷的既有信任边界仍然成立，这一设计不提供互不信任租户之间的隔离。

## 5. 数据契约

### 5.1 项目记忆条目

建议独立 schema 标识为 `sbx.project-memory/v1`。以下为拟议示例：

```json
{
  "schema_version": "sbx.project-memory/v1",
  "id": "mem-001",
  "revision": 1,
  "project_id": "project-001",
  "kind": "testing",
  "title": "执行集成测试前检查 Docker",
  "content": "执行 core 的 Docker 集成测试前检查 daemon 可用；单元测试通过不能证明集成测试通过。",
  "applies_to": {"paths": ["core/**"], "tags": ["docker", "integration-test"]},
  "source": {"task_key": "task-001", "attempt_id": "attempt-001", "revision": "commit-or-input-digest"},
  "evidence": [{"kind": "test_result", "ref": "evidence-001", "digest": "sha256:..."}],
  "status": "approved",
  "review": {"reviewer": "operator", "reviewed_at": "2026-11-02T00:00:00Z", "reason": "已核对实际测试记录"},
  "created_at": "2026-11-02T00:00:00Z"
}
```

`kind` 第一版限定为 testing、environment、constraint、troubleshooting、workflow。来源支持多个任务；候选还应记录提取器版本、材料摘要和候选内容摘要。手动录入可以没有来源 task，但必须注明人工来源与审核理由。

正文应自包含，证据引用只允许 sbx 登记的材料标识，不接受候选自行声明的任意宿主文件路径。摘要证明材料绑定关系，不证明内容正确。证据被清理时保留引用与摘要，标记 unavailable，不伪称仍可复核。

### 5.2 任务交接记录

建议 schema 为 `sbx.task-handoff/v1`，至少包含：任务/attempt 身份、目标、已确认约束、完成事项、待办、验证事实、工作区与产物引用、未决操作引用、下一步建议、更新时间。

运行结果、测试结果、交付回执与 unknown 状态由执行记录投影生成。模型生成的说明与建议单独标记；不得改写事实字段。交接记录不保存凭据、完整工具响应或无边界的原始日志。

### 5.3 本次记忆快照

至少记录项目 ID、选择条件、预算、排序规则版本、所选条目 ID/版本/正文摘要、完整上下文摘要、创建时间和注入结果。快照不可变，内容由宿主保存；执行记录只保存安全引用和摘要，公共 JSONL 不输出记忆正文。

## 6. 读取与注入流程

1. 准入时解析当前项目绑定，核验操作者权限与组织上限。TaskSpec 只能选择已授权项目并收窄范围。
2. Source 准备完成后，根据任务显式路径、标签和关键词选择 approved 条目。没有路径信息时不臆测全仓适用；项目通用条目与路径限定条目分开匹配。
3. 先过滤已归档、已替代、超出授权和不满足环境条件的条目，再按明确的稳定规则排序；同分用 ID 排序。达到预算后按条目截断，不截成含义不完整的半条经验。
4. 第一版拟默认上限为 20 条、16 KiB UTF-8 正文，由宿主策略配置并可被任务收窄。这是字节预算，不宣称等价于 provider token 数。
5. 在启动 Agent 前持久化快照并校验摘要，再通过 provider 适配注入。空结果是合法状态。
6. 同一 attempt 的继续执行使用原快照；只核验、审核、交付、核对外部状态时不重新选记忆，也不重新运行 Agent。新 attempt 重新选择并生成新快照。

策略撤销优先于快照复用：在恢复 Agent 前重新核验项目权限与条目撤销状态；若原快照含已撤销内容，阻止恢复并要求显式创建新 attempt，不静默替换原快照。运行中已经读取的内容无法收回，应沿用当前受管执行的取消机制限制后续活动。

同一 Codex 会话可能已经包含旧知识，`--fresh` 也不等于关闭项目记忆。新会话和项目记忆启用状态是两个独立选择，需要分别显示。

## 7. 交接、提取与审核流程

### 7.1 何时生成

执行服务在可持久化的阶段边界更新交接记录；不只在 done 时生成。正常退出、验证失败、等待审核、取消和可恢复中断都应保存当前已知事实。SIGKILL 时由 recover 基于已有记录生成不完整交接，明确哪些信息缺失。

第一版自动提取默认关闭，手动触发；M2 启用后，操作者可配置在 attempt 关闭时自动生成候选。verify、approve、deliver、reconcile 更新证据时，可以显式重新提取，不自动重复整个提取过程。

### 7.2 使用哪些材料

默认使用任务声明的安全摘要、交接记录、验证结果和操作者允许的产物摘要。原始日志、会话历史和工具返回需要单独授权及大小限制。受 Source exclude 排除的材料不能通过扫描共享会话目录重新引入。

默认沿用任务已配置的 provider 与认证，通过有独立超时、成本限制的隔离提取调用生成结构化结果；不恢复业务会话，不授予业务写工具或任意宿主访问。材料作为不可信数据传入。提取所需模型调用会产生额外费用，自动提取必须显式启用。

凭据路径和已知敏感字段在输入选择阶段排除；脱敏只作为附加措施，不能保证任意文本均无秘密。输出经 schema、大小、来源引用检查后保存为 candidate；异常输出不能自动降级为可信正文。

### 7.3 状态与审核

```text
candidate → approved → superseded / archived
    └────→ rejected
```

修改候选正文后必须审核新的内容摘要。批准操作绑定候选摘要和项目当前版本，在项目锁内完成；并发更新导致版本不匹配时返回冲突。已批准条目正文不可原地覆盖，修订创建新版本；已有快照仍能追溯旧版本。

审核人标识在个人模式下是本地操作者声明，在受管模式下必须绑定管理员支持的身份；不能把任意 `--reviewer` 字符串视为已认证身份。

近似重复内容只提示合并。矛盾经验保留适用条件并进入人工处理，不自动按“最新”覆盖。approved 表示经过审核，不表示永久正确；依赖版本、路径变化或复核期限可使条目标记需复核并暂停自动选择。

## 8. 崩溃恢复、锁与结果语义

项目库使用独占项目锁、版本比较和原子文件替换；索引可从条目与审核记录重建。禁止持有项目锁进行模型调用，也不在模型等待期间阻塞任务管理锁。

提取作业状态为 pending、running、succeeded、failed、interrupted。幂等键至少绑定项目、task、attempt、材料摘要与提取器版本。同一作业重复提交返回已有候选；显式替换提取器版本或材料才创建新作业。

模型请求已发出但结果未持久化时，标记 interrupted，不宣称模型调用恰好一次；重试可能再次计费，但不能重复发布候选。提取重试只消费已冻结的材料，不运行原任务、工具写入或交付步骤。

记忆提取状态与执行、验证、交付结果分开。已完成任务的提取失败记录为后处理失败，并提供单独查询/重试入口，不改写业务终态或已有退出结果。启动前显式要求固定快照却无法读取时必须阻止启动；自动读取模式遇到损坏也应报错，不能静默当成无记忆。

## 9. 当前接口与后续扩展

当前可用：

```sh
sbx memory project create my-project
sbx memory project bind --project my-project --repo /path/to/repository
sbx memory handoff <task> --scope git --summary safe-summary.json
sbx memory extract <task> --project my-project --scope git
sbx memory jobs
sbx memory job <job-id>
sbx memory processing <task> --scope git
sbx memory recover <job-id>
sbx memory retry <job-id>
sbx memory candidates --project my-project
sbx memory approve <candidate-id> --project my-project \
  --digest <current-digest> --reviewer operator --reason "已核对依据"
```

完整限制、配置及拒绝/归档流程以 [使用手册](project-memory.md) 为准。Git 命名空间需位于对应仓库，通用任务使用 `--scope general`。所有提取条目仍为 candidate，来源引用不是独立验证。

未来的 export/import、近似重复/冲突提示、认证组织审核尚未实现。拟议导入须验证结构、摘要和大小，仅生成本地候选，保留来源审核信息但不继承本地 approved 权限。

当前 TaskSpec 严格使用 `sbx.execution/v2`，记忆选择来自可信宿主配置和 Git flags；不支持在 TaskSpec 临时追加记忆字段。未来若增加项目授权/快照选择字段，需要同步决策协议版本、严格解码、schema、admit、策略和记录兼容性。不改变 `sbx memory pull` 的 Claude 同步语义。

## 10. 生命周期与保留策略

| 操作 | 任务交接 / 快照 | 项目记忆 | Provider 历史 |
|---|---|---|---|
| stop / recover | 保留并更新可确认事实 | 保留 | 按 provider 现有机制保留 |
| 通用 task clean | 保留 | 保留 | 不隐式清理共享身份卷 |
| Git done / drop | 删除工作区前保留交接和会话引用摘要 | 保留 | 关联归档；不宣称删除历史 |
| prune 任务产物 | 保留摘要，缺失证据标记 unavailable | 不自动删除；标记复核状态 | 不受影响 |
| archive 记忆 | 旧快照可追溯 | 停止后续选择，保留修订 | 不受影响 |

第一版不自动物理删除项目记忆或会话历史。提供占用统计与独立的显式清理规划；已有快照引用的条目不能直接破坏性删除。需要因敏感信息紧急擦除时，单独执行有审计的擦除流程，并明确已注入的 provider 会话也可能包含副本，普通 archive 不等于擦除。

## 11. 使用场景

### 个人：一次排障帮助下一次任务

任务 A 发现测试依赖 Docker，交接记录保存测试失败和后续通过的证据。extract 生成候选，操作者核对后批准。任务 B 绑定同一项目并涉及 core 测试，启动时读取该条目；status 能说明注入了哪条、哪个版本。任务 A done 后该经验仍然存在。

### 团队：评审后共享经验

成员导出获准共享的条目，通过团队已有评审渠道审阅。另一成员显式映射项目并导入为候选，本地审核后使用。导出默认不含原始日志、宿主绝对路径、凭据或其他项目条目；第一版不声称提供中心权限分发。

### 基础设施：可重现的执行上下文

外部系统提交带获准项目引用和 pinned 选择的任务，执行记录保存快照摘要。复核时能重建当时的知识输入；项目授权被撤销则拒绝重新启动 Agent。记忆失败与业务失败分别上报，自动化不因提取失败重试外部写入。

## 12. 实施路线与验收

| 阶段 | 交付内容 | 必须通过的验收 |
|---|---|---|
| M1：受控存储与读取（已实现，受控验收） | 项目注册、手动候选/审核、版本存储、确定性选择、快照、两种 provider 注入、策略与协议适配 | 不串项目；新会话/resume 均注入；预算有效；撤销阻止恢复；done/clean 后仍保留；旧 Claude pull 不回归 |
| M2：任务自动沉淀（已实现，受控验收） | 交接投影、隔离提取调用、候选验证、幂等作业、成本限制、恢复与状态查询 | 失败任务可提取；SIGKILL 后交接诚实；重试不重放 Agent/外部写；额外模型调用可控；候选不自动批准 |
| M3：维护与共享（待实施） | 重复/冲突提示、过期复核、归档、导入导出、占用统计、受管审核身份 | 并发审核不覆盖；损坏索引可重建；不可信导入无越权；导出不泄漏默认排除材料；旧快照可追溯 |

每阶段都应交付实现、对应 schema/协议说明、命令帮助、场景教程和验收证据。重点测试项目绑定与路径边界、并发版本冲突、崩溃点、来源摘要绑定、撤销和清理语义；provider 适配使用受控实现验证，再执行真实 Codex/Claude 的最小新会话与恢复验收，区分受控测试与真实环境结果。

建议按 M1 → M2 → M3 推进。M1 先证明“已确认经验能准确、安全地被后续任务使用”，再增加模型自动提取，最后扩展共享与长期治理。
