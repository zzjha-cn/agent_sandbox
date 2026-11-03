# 项目记忆：模式、审核与任务使用

[首页](index.html) · [设计](design.md) · [命令](commands.md) · [完整设计与后续路线](project-memory-design.md)

当前完成 M1–M3：本地项目记忆、候选审核与固定快照、Codex/Claude 启动适配、任务交接与隔离提取，以及到期复核、维护检查、显式共享和受管审核身份。原生记忆导入、原生记忆备份与中心共享服务尚未实现。

## 1. 先选择模式

| 模式 | 启动行为 | 适用场景 |
|---|---|---|
| native | Codex 显式启用原生记忆生成和使用；Claude 沿用原生机制及现有宿主同步 | 个人日常开发 |
| managed | 关闭原生自动记忆；注入 sbx 已审核的项目记忆快照 | 多 provider 复用、可追溯执行 |
| off | 关闭原生自动记忆，不注入项目记忆 | 临时、敏感或不应积累经验的任务 |

个人配置未指定模式时默认为 native。安装组织策略的受管执行默认 off，拒绝 native；可以通过可信宿主配置选择 managed。这里的 managed 记忆模式与组织受管模式是两个不同概念。个人审核身份为本地操作者声明；组织模式的修改与导出需要管理员配置宿主 UID 和项目授权，身份来自系统进程而非 --reviewer。配置见第 10 节。

Codex 启动前检查运行时是否提供 memories feature；不支持时升级任务镜像，不静默忽略模式。native 不保证每次任务结束立即产生经验，实际生成时机、额度限制和后台处理由 Codex 决定。当前未提供原生记忆内容同步或按项目分库。

所有模式都保留已有会话历史。关闭记忆不会删除旧会话里已经出现的内容，也不阻止 Agent 显式读取其可访问文件；共享身份卷仍然属于同一可信操作者，模式开关不构成租户隔离。

```sh
# 在 Git 仓库中运行；原生模式不需要创建项目记忆库。
sbx run --agent codex --memory native fix-login
# 切换现有任务模式前先停止，再明确新建会话。
sbx stop fix-login
sbx run --agent codex --memory off --fresh fix-login
```

## 2. 创建项目并录入经验

以下本地命令不需要 Docker；只有 bind 需要 Git 仓库。项目名是稳定本地 ID，限 1–80 个字母、数字、下划线或连字符。

```sh
sbx memory project create my-project
sbx memory project bind --project my-project --repo /absolute/path/to/repository
```

主仓库与 worktree 使用同一绑定。新 clone 或仓库迁移需要显式重新绑定，remote URL 不作为身份凭据。已绑定到另一项目的目录不能静默改绑。

保存 `entry.json`：

```json
{
  "kind": "testing",
  "title": "集成测试前检查 Docker",
  "content": "执行 Docker 集成测试前检查 daemon 可用；单元测试通过不代表集成测试通过。",
  "source": "operator: 已核对本次集成测试日志"
}
```

```sh
sbx schema memory-input
sbx memory add --project my-project --file entry.json
sbx memory candidates --project my-project
```

kind 支持 testing、environment、constraint、troubleshooting、workflow。title、content、source 必填；source 是人工来源说明，不代表系统已独立验证。可选 paths 限定相对路径或 glob，可选 tags 用于筛选。`core/**` 匹配 core 下各层路径；其他模式沿用 Go path.Match。单条序列化输入不超过 8 KiB，不在其中保存凭据。

add 永远生成 candidate。JSON 不允许自行提供 approved、reviewer、revision 等存储字段。条目和审核历史由宿主项目库管理，不直接编辑存储文件。

## 3. 审核、替代与归档

先核对 candidates 或 show 输出中的内容、来源、适用条件和 digest，再执行：

```sh
sbx memory show <entry-id> --project my-project
sbx memory approve <entry-id> --project my-project \
  --digest <显示的当前digest> --reviewer operator --reason "已核对测试证据"
# 或拒绝：
sbx memory reject <entry-id> --project my-project \
  --digest <显示的当前digest> --reviewer operator --reason "依据不足"
sbx memory list --project my-project
sbx memory project show my-project
```

摘要绑定审核对象；旧摘要、重复批准或并发状态变化会拒绝。project show 包含所有修订，list 显示各条目的当前状态。已批准内容不可原地覆盖。

更新经验时，提交新候选并在 entry.json 的 supersedes 中填写原 approved 条目 ID。批准新候选时，原条目在同一原子保存中变为 superseded。单独撤销使用：

```sh
sbx memory archive <entry-id> --project my-project \
  --digest <当前approved版本digest> --reviewer operator --reason "环境已变更"
```

archive 停止后续选择，保留旧版本；不等于从已存在的 Agent 会话或备份中擦除内容。

## 4. Git 任务使用受管记忆

```sh
sbx run --agent codex --memory managed --memory-project my-project new-fix
sbx memory status new-fix
# 默认 scope 为 git；在当前仓库查询同名任务。
sbx memory status new-fix --scope git --attempt <attempt-id>
```

如果已绑定当前仓库，可以省略 --memory-project。没有路径条件时只选择项目通用经验；不会自动把所有路径限定条目注入。每次最多 20 条，完整参考正文最多 16 KiB，按稳定 ID 排序；candidate、rejected、archived、superseded 不进入上下文。

要配置路径、标签或长期使用 managed，在 `~/.sbx/config.toml` 或宿主 workspace 配置中添加：

```toml
[memory]
mode = "managed"
project = "my-project"
paths = ["core/internal/execution/starter.go"]
tags = ["integration"]
```

paths 需要匹配条目的路径条件；指定 tags 时还要求至少一个标签匹配。未指定 tags 时不按标签排除。memory 整个配置节禁止放入仓库 `.sbx/sandbox.toml` 或 prep 声明，避免仓库自行选择其他项目知识。

Claude 使用方式相同，只需在新任务中选择 `--agent claude`。同一个逻辑 task 不允许切换 provider。

## 5. 固定快照与通用任务

```sh
sbx memory snapshot --project my-project --paths core/internal/execution/starter.go
```

命令生成不可变快照，并输出完整内容与 digest。将该摘要配置在可信宿主配置中：

```toml
[memory]
mode = "managed"
project = "my-project"
snapshot = "sha256:替换为snapshot输出的64位十六进制摘要"
# 使用 snapshot 时不能同时配置 paths/tags；条件已经保存在快照内。
```

随后按已有流程提交通用任务：

```sh
sbx admit --spec task.json
sbx task run --spec task.json
sbx memory status <task-id> --scope general
```

M1/M2 不增加 TaskSpec 字段；通用任务只读取宿主全局 memory 配置，不读取当前目录的仓库/workspace 配置。`sbx run --spec` 也不接受 --memory 等 Git flags。不同自动化项目可使用独立 SBX_HOME 与各自可信配置；这不改变 Docker daemon 的既有归属规则。

固定快照不绕过撤销：条目归档或被替代后，旧快照不能用于再次启动 Agent。对既有业务结果执行 verify、approve、deliver、reconcile 不重新选记忆，也不会因记忆撤销而重放 Agent 或外部操作。

## 6. 会话与清理

恢复已有受管会话时沿用原上下文。项目新增经验不会自动加入旧会话；要使用更新的经验，Git 入口先 stop，再使用 --fresh；通用任务创建新的 task ID。改变模式、项目、选择条件或 pinned 配置时，也需要新会话。

原有会话没有 sbx 记忆记录时，只允许以 native 兼容恢复；要切换到 managed/off，必须新建会话。正在运行的会话不能靠再次 run 立即切换模式或 --fresh。

存储位置：

| 路径 | 内容 |
|---|---|
| `$SBX_HOME/project-memory/projects/<project>.json` | 原子保存的条目与完整审核修订 |
| `$SBX_HOME/project-memory/bindings.json` | 仓库根与项目绑定 |
| `$SBX_HOME/project-memory/snapshots/<digest>.json` | 可固定引用的不可变快照 |
| `$SBX_HOME/task-memory/<task-key-hash>/` | session、启动上下文、handoff、人工 summary 与自动 processing 记录 |
| `$SBX_HOME/memory-extractions/<job-id>/job.json` | 冻结材料、提取状态、调用计数、模型输出与候选 ID |
| 共享卷 `/home/agent/.codex/memories/` | Codex 原生生成状态，与受管项目库独立 |

task-memory 中的 session/attempt 快照是启动前的准备记录，不能单凭该文件证明模型已经读取或遵守了知识；需结合对应执行结果判断启动是否成功。status 输出的是宿主记录，不是容器内原生记忆生成进度。

stop、task clean、Git done/drop 均不删除独立项目库或 task-memory。当前只提供归档，不自动物理删除、自动导出或提供原生记忆备份。`sbx memory pull` 仍只负责 Claude 原生记忆同步；managed/off 启动时跳过该自动导入，done/drop 也只对 Claude native 任务提示导回记忆。

## 7. 任务交接：先看事实，再补充进度

```sh
sbx memory handoff fix-login
sbx memory handoff <task-id> --scope general --attempt <attempt-id>
```

handoff 从持久化执行记录投影，包含 attempt、记录序号、阶段、Agent 退出码、验证摘要、输入/产物/回执摘要、未决操作和 recovery。正常退出、失败、取消、等待审核与阶段检查点均更新；记录未关闭或执行阶段仍在运行/移交时标记 incomplete。记录不能证明进程已死亡，也不能把 Agent 退出码 0 当作测试通过。SIGKILL 后再次运行此命令可从最后落盘的事实重建；它不启动 Agent、不查询外部系统、不改变 unknown。

目标、约束、完成事项和下一步由操作者通过安全摘要明确补充。例如保存 `handoff-summary.json`：

```json
{
  "goal": "修复登录集成测试",
  "constraints": ["不修改线上配置"],
  "completed": ["定位到本地测试依赖不可用"],
  "todo": ["启动测试依赖后重新验证"],
  "notes": ["集成测试需要 Docker；必须单独验证 daemon 可用"],
  "next": ["核对测试环境后执行集成测试"]
}
```

```sh
sbx schema memory-summary
sbx memory handoff fix-login --summary handoff-summary.json
```

备注保存为独立文件，后续事实投影不会覆盖它。输出的 operator_summary 是人工声明，不属于验证证据。没有明确提供摘要时不从 prompt、工作区或日志猜测目标；历史记录中没有的事实也不会补造。

## 8. 从任务提取候选

提取使用任务 provider 的模型 API：Codex 对应 OpenAI Responses，Claude 对应 Anthropic Messages。它是一次无工具、无会话续接的宿主 HTTP 调用，不运行 Codex/Claude CLI，不挂载工作区，不经过任务容器代理。当前只支持官方 API 端点；订阅登录态、自定义 provider 端点和原生记忆文件导入不在本阶段范围内。

在可信全局配置（或 Git 的宿主 workspace 配置）中选择有权限使用的模型，并沿用 `agents.<provider>.api_key_env` 或 `api_key_file`。例如：

```toml
# ~/.sbx/config.toml；不要提交凭据值。
default_agent = "codex"

[agents.codex]
api_key_env = "OPENAI_API_KEY"

[memory.extract]
auto = false
model = "替换为你的账户可用且支持 Responses 的模型ID"
timeout_seconds = 90
max_output_tokens = 2048
max_calls = 2
```

Claude 配置改为 agents.claude 的 API key，model 使用支持 Messages API 的 Claude 模型。提取默认优先取该 attempt 的 provider 准备记录；老任务没有准备记录时用当前可信 default_agent。不自动复用、导出或转换订阅凭据。

```sh
# 优先使用已附加的交接备注；也可用 --summary 为本次提取单独提供摘要。
sbx memory extract fix-login --project my-project
sbx memory extract <task-id> --scope general --project my-project \
  --attempt <attempt-id> --summary handoff-summary.json
sbx memory jobs
sbx memory job <job-id>
sbx memory candidates --project my-project
```

默认 project 来自可信 memory.project 或仓库绑定；通用任务没有仓库绑定时应显式提供。失败或尚未关闭的任务也可以手动提取，状态会如实进入材料；原始日志、完整工具响应、provider 历史目录都不被扫描。摘要最大 16 KiB，冻结材料最大 64 KiB，模型文本最大 32 KiB，每次最多 5 条候选。

模型输出分为 explanation、suggestions 和 candidates。说明与建议只是模型判断；执行事实不允许由模型输出改写。每条候选必须引用已冻结的 handoff/summary 摘要，保存提取器版本、task、attempt、作业 ID、材料与输出摘要。来源引用证明可追溯，不证明经验正确；继续按第 3 节核对当前 digest 后批准。

输入/输出拒绝未知字段、重复 JSON key、过大内容及可识别的凭据模式；这不是任意秘密的完整检测器。人工摘要应先去除凭据、个人信息及不允许外发的内容。未提供足够证据时返回零条候选属于成功，不补造经验。

## 9. 自动沉淀、失败查询与重试

只有显式设置以下配置，才会在 managed 记忆模式的同步 headless run 关闭 attempt 后自动提取：

```toml
[memory]
mode = "managed"
project = "my-project"

[memory.extract]
auto = true
model = "替换为账户可用的模型ID"
timeout_seconds = 90
max_output_tokens = 2048
max_calls = 2
```

先完成第 8 节的 API key 配置。自动流程读取事实投影和该 attempt 已附加的安全备注，默认不会自动扫描测试日志。交互与 detached 运行没有后台提取守护进程；使用 memory extract 手动触发。verify/approve/deliver/reconcile 之后可显式重新提取，不因证据变化自动再次调用模型。组织受管执行仍不允许模型提取或附加人工任务摘要；M3 的项目维护/审核授权不包含任务材料外发。

```sh
sbx memory processing fix-login
sbx memory processing <task-id> --scope general --attempt <attempt-id>
sbx memory job <job-id>
# 孤立 worker 的 running 状态：只标记 interrupted，不发送模型请求。
sbx memory recover <job-id>
# 明确重试，可能再次计费；只使用冻结材料，不重跑原任务。
sbx memory retry <job-id>
```

业务终态和事件先保存，再释放任务锁进行有界后处理；当前命令会等待这一步返回。提取失败只写 processing/job，不改变业务退出结果、验证或交付回执。自动提取未启用时没有 processing 记录。若查询到 running，不代表 worker 仍然存活；recover 必须取得独占作业锁后才会标记中断。processing 有 job 引用时，查询会以该 job 的最新状态为准。

作业状态为 pending → running → succeeded/failed，孤立 running 经 recover 成为 interrupted。幂等 ID 绑定项目、task/attempt、冻结材料、provider、model、提取器版本及预算；相同输入返回已有作业。失败不会被重复 extract 隐式重试；retry 需要显式调用。改变摘要、模型或预算会创建新作业；重试不重读源摘要，provider/model/预算不变，仅重新解析当前可信宿主认证配置。

超时限 1–300 秒；输出预算 256–8192 tokens；每个作业最多预留 1–3 次调用（默认 2，包含失败与中断）。材料大小、输出 tokens 和调用次数控制额外使用量，不提供美元费用上限；账户侧预算仍由 provider 管理。认证失败也消耗一次调用预留。常见 model_call_failed 应检查 API key、模型权限、网络与超时；原始服务错误和响应不写入作业，避免凭据泄漏。

模型请求已发出但未落盘时可能已经计费，重试不保证模型只调用一次。已校验输出先持久化，再原子发布候选；若在发布后崩溃，重试可以复用输出，稳定候选 ID 避免重复发布，也不会覆盖后来完成的人工审核。

## 10. M3：长期维护与团队共享

### 10.1 到期复核、归档与维护检查

```sh
# 审核时可明确有效期；时间必须晚于当前时间。
sbx memory approve <entry> --project my-project --digest <current-digest> \
  --reviewer local-operator --reason '已核对当前版本' \
  --review-due '2027-06-01T00:00:00Z'
# 过期后重新验证事实，再续期；使用 show 返回的最新 digest。
sbx memory renew <entry> --project my-project --digest <current-digest> \
  --reviewer local-operator --reason '升级后重新验证' \
  --review-due '2027-12-01T00:00:00Z'
sbx memory inspect --project my-project
sbx memory stats --project my-project
sbx memory check --project my-project
```

省略 review-due 的批准没有自动到期时间。到期不抹掉 approved 历史，但选择与固定快照复用都会拒绝到期条目；inspect 返回 review_due 提示。renew 只作用于 approved 条目，要求未来期限并增加修订，旧快照仍可读取追溯，但不能无声改成新内容继续启动。到期与续期均不重放任务或外部业务写；verify/deliver/reconcile 仍按原业务证据运行。归档使用既有 archive 命令，保留历史并停止注入。

inspect、stats、check 返回同一个 memory-maintenance/v1 报告：项目 sequence、当前状态计数、历史修订数、项目 JSON 文件实际字节和 8 MiB 上限，以及维护提示。占用量只统计项目权威文件，不包含快照、提取作业或 Docker 卷。三个命令均验证完整历史；check 不修改数据。当前视图直接从历史推导，没有独立索引需要重建；权威记录损坏时明确报错，需从可信备份恢复，不能自动把损坏内容变成有效记录。

重复提示对同 kind、同路径/标签范围的正文做大小写和空白归一化；相同标题、范围但正文不同则提示 possible_conflict。这些是词法提示，不证明经验语义重复或互相矛盾，也不自动合并、撤销或批准。需要修订时按 supersedes 流程创建候选；批准替代版本后旧条目及快照继续可追溯。

### 10.2 在两台机器或成员之间显式共享

```sh
# 发送方：必须逐项选择已批准且未到期的条目，最多 20 条。
sbx memory export --project my-project --entries <entry-1>,<entry-2> > shared-memory.json
# 查看共享包，按团队已有的评审与文件传递流程分享。
# 接收方：显式选择已有的本地项目；外部项目名不会创建或映射本地项目。
sbx memory project create local-project
sbx memory import --project local-project --file shared-memory.json
sbx memory candidates --project local-project
# 核对每条候选，再用本地 digest/reviewer/reason 批准。
sbx memory approve <imported-entry> --project local-project --digest <local-digest> \
  --reviewer local-operator --reason '本项目已验证适用'
sbx memory snapshot --project local-project
```

共享包为 memory-bundle/v1，最大 256 KiB；导入拒绝未知字段、重复 JSON key、null、尾随对象、无效摘要、越界路径与 supersedes。导出只包含所选条目的 kind/title/content/paths/tags、通用来源标签、来源项目 ID 和内容摘要，不包含宿主原始 source、日志、提取 task/job、审核身份、完整历史或原批准权限。**正文与标题按选中内容原样导出**，其中若有内部路径、业务信息或凭据，需要发送前自行检查；字段裁剪不等于内容脱敏。

来源项目名和包摘要用于追溯、完整性和重放去重，不证明发送者身份；团队传递与评审渠道由部署者提供。导入在锁内原子生成本地 candidate，保存 import_origin，不继承源端 approved、身份、到期时间或授权。重复导入相同来源项目与条目内容返回现有条目，不增加修订，也不覆盖后来批准、拒绝或归档的结果。它不是项目库完整备份或跨机器实时同步。新增的到期、导入与身份字段会写入修订；旧二进制可能因严格解码而拒绝读取，降级前应保留原始备份并使用支持 M3 的版本读取，不能删字段伪造兼容。

### 10.3 受管审核身份与并发版本

管理员在既有、固定的 `/etc/sbx/organization.toml` 中增加：

```toml
# 保留 schema_version、organization、revision、enabled、expires_at 等原字段。
# 501 只是示例；管理员应使用该机器 id -u 得到的实际 UID。
[memory_reviewers."501"]
subject = "alice"
projects = ["my-project", "local-project"]
```

策略文件及父目录沿用 root 归属、不可被组/其他用户写入和拒绝 symlink 的现有校验。只接受明确 UID 与项目 ID，没有通配授权；未配置、过期、禁用的策略均不允许记忆修改。subject 是管理员绑定的操作者名称；实际身份依据宿主进程 UID，--reviewer 不能覆盖。root 也需要显式列入授权。命令会记录 subject、organization、policy_digest，以便追溯当时的授权依据。

```sh
sbx memory project show my-project
# 从上一步读取 sequence，从 show/candidates 读取待审核条目的 digest。
sbx memory approve <entry> --project my-project --digest <current-digest> \
  --sequence <current-project-sequence> --reason '按团队标准验证'
```

受管 approve/reject/archive/renew 必须提供 sequence；个人模式也可提供以检测项目级并发。digest 与 sequence 在同一存储锁内比较，任一改变都返回冲突，应重读后审核。受管创建、绑定、添加、审核、快照、导入与导出仅限获准项目；这些权限不会授权模型外发任务材料。离线本地读取保留，不把审核写入权限当作保密访问控制。

这是管理员授权的本机审核机制，存储仍由可信宿主操作者拥有；不能抵御宿主管理员或存储所有者直接编辑文件。跨机器 UID 需分别配置，不提供签名审计、中心身份服务或恶意租户隔离。更换 SBX_HOME 不能更换固定管理员策略，但也不提供跨不同 Home 的统一审核库。

## 11. 验证与后续工作

当前验收覆盖：项目隔离、审核摘要与并发冲突、选择预算、固定快照、撤销拒绝、会话切换、清理后保留，以及两种 provider 的新会话/恢复、交互/headless 启动脚本。脚本测试使用受控 CLI，验证正文作为数据传递，不执行正文中的 shell 表达式。

M2 验收覆盖事实投影、人工备注保留、受控 HTTP → CLI 提取 → 审核 → 快照闭环、失败任务提取、自动后处理失败隔离、独占锁、调用预算、进程被杀后的恢复，以及发布后崩溃重试不覆盖已有审核。HTTP 测试使用受控响应，不发起付费调用。

M3 验收覆盖显式导出、私有元数据排除、不可信包验证、原子候选导入、并发与重复导入不覆盖审核、到期停止选择/固定快照复用、续期与归档、项目 sequence 冲突、UID 项目授权和身份防冒用，以及 CLI 全流程。管理员策略授权使用受控快照测试，未在本机安装或修改 /etc 策略。

本轮未完成真实容器与真实模型验收，真实账户权限、网络可达性及模型产出质量仍需用用户配置验证。原生导入桥、中心共享服务、更广的产物摘要提取仍属后续目标。完整目标见 [设计提案](project-memory-design.md)。

原生开关依据：[Codex 配置参考](https://learn.chatgpt.com/docs/config-file/config-reference)、[Codex 原生记忆](https://learn.chatgpt.com/docs/customization/memories)、[Claude 自动记忆](https://code.claude.com/docs/en/memory)。它们控制原生行为，不提供 sbx 项目审批和版本契约。

提取 API 依据：[OpenAI Responses](https://developers.openai.com/api/docs/guides/migrate-to-responses)、[Anthropic Messages](https://platform.claude.com/docs/en/api/messages)。提取路径不提供业务工具调用。
