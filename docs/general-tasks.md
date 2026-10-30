# 通用任务：输入、验证与本地交付

`sbx task` 是通用任务的显式 v2 入口，在 Git 仓库之外也能使用。支持同步 headless Claude/Codex、Git revision/文件副本/空输入、稳定产物、独立验证与本地目录/Git 交付。G5 的运行期只读工具见 [动态工具规范](dynamic-tools.md)，G6 的预先声明写操作、回执核对与业务状态验证见 [外部操作规范](external-operations.md)。`sbx run` 保留 Git/交互入口；`sbx run --spec` 与 `sbx task run --spec` 共用提交路径。机器输入/事件和执行记录已统一为 v2，`--envelope` 仅是参数别名，v1 输入被拒绝，见 [自动化与迁移](automation.md)。G7 已完成 schema/能力发布与迁移规则，见 [自动化规范](automation.md)；G8 组织策略与事件汇总已完成当前范围，见 [组织规范](organization.md)；受管模式只接受同步 TaskSpec（含 Git Source）。远端产物发布仍属后续范围。

## 1. 可信注册表与任务声明

操作者在 `$SBX_HOME/execution-policy.toml` 注册资源。该文件必须是当前操作者拥有的普通文件，不允许 group/other 写入；不读取项目中的同名文件。任务声明只能选择注册引用，不能提供宿主路径、任意宿主验证命令或自行授权覆盖。

示例（替换为自己的绝对路径；验证镜像使用 `docker image inspect <镜像> --format '{{.Id}}'` 得到的不可变 `sha256:…`）：

```toml
[sources.monthly]
kind = "files"
path = "/absolute/path/to/monthly-input"
include = ["."]
exclude = ["private/**"]

[criteria.report]
kind = "files"
version = "1"
required = ["report.json"]
allowed_changes = ["report.json"]
allow_unchanged = false

[criteria.report.format]
"report.json" = "json"

[criteria.report.contains]
"report.json" = ["rows", "total"]

[criteria.review]
kind = "manual"
version = "1"

[sinks.reports]
kind = "local_directory"
path = "/absolute/path/to/published-report"
allow_replace = false
```

注册目录的父目录必须预先存在。目录交付将选定输出发布为整个目标目录；路径指向本次最终目录，而不是隐式追加任务名的父目录。引用映射及权限会在准备、验证/继续和交付前重新读取。

`report-task.json`：

```json
{
  "schema_version": "sbx.execution/v2",
  "task_id": "monthly-report",
  "prompt": "Read the input and create report.json with rows and total.",
  "source": {"kind": "files", "ref": "monthly"},
  "agent": {"provider": "codex"},
  "outputs": {"include": ["report.json"], "patch": true},
  "criteria": [
    {"id": "report", "kind": "files", "ref": "report"},
    {"id": "review", "kind": "manual", "ref": "review"}
  ],
  "sink": {
    "kind": "local_directory",
    "target_ref": "reports",
    "conflict": "reject",
    "delivery_key": "monthly-report-001"
  },
  "limits": {"timeout_seconds": 120}
}
```

```sh
sbx task run --spec report-task.json
sbx task status monthly-report --json
sbx task approve --spec report-task.json --reviewer operator-name
```

未配置人工判据时，全部自动判据通过后直接交付。人工等待会持久化并释放锁、结束当前 CLI；审核继续使用独立预算，不重新运行 agent。`reject` 记录拒绝结论；`verify` 对同一快照重新验证；`deliver` 只核对/重试交付。

## 2. 输入和工作区规则

- `files` 可注册目录或单个文件，始终复制到隔离工作区，不直接写回源目录。单文件以原文件名进入工作区，注册的 include 也相对该文件所在目录。
- `git` 注册仓库路径；`source.revision` 固定提交/引用，省略时解析 HEAD 并记录实际提交。默认只读取提交中的原始 blob，不受 archive 的 export-ignore/export-subst 改写；不复制 `.git`。
- Git 的 `source.working_tree=true` 显式选择已跟踪工作树内容，`source.untracked=true` 再加入未被 Git 忽略的未跟踪文件；注册项须分别允许 `allow_working_tree`、`allow_untracked`。工作树选择要求 revision 等于 HEAD。选中的链接、特殊文件和 submodule 拒绝准备；需作为单独受支持输入注册。
- 省略 source 准备空目录，不执行 git init。Git 便捷命令 `sbx run <task>` 仍选择当前仓库/worktree；这是独立的工作区选择语义。
- include 支持相对文件、目录前缀和 glob；`**` 可匹配多层。可信选择范围是上限，任务 include 只能在此范围内筛选。拒绝绝对路径、`..`、反斜线和非法模式。
- 默认输入/产物各 64 MiB、10000 个文件、单文件 16 MiB。操作者可用 `[input_limits]`、`[artifact_limits]` 的 `bytes/files/file_bytes` 调整。任务无法放宽这些限制。
- `.git`、`.sbx`、`.ssh`、`.aws`、`.claude`、`.codex`、`.env`/`.env.*`、`auth.json`、`credentials.json` 不进入副本/产物；可信注册表还可增加 exclude。
- 准备保存清单、内容摘要、实际 revision、授权绑定及基线。后续 run 会比较源身份、内容、revision 和授权；变化要求新任务，不能静默更换输入或覆盖旧工作区。每次 run 仍是新 attempt，不是 prompt 幂等重放。

通用任务的工作目录在 `$SBX_HOME/workspaces/tasks/<task>`，执行记录在 `executions/tasks/<task>`，稳定快照在 `artifacts/tasks/<task>`；内部记录使用 `sbx.execution-record/v2`。Git 便捷任务也使用相同 v2 记录实现。旧 v1 记录保留原样并拒绝继续，不做隐式升级；开发期迁移步骤见 [自动化手册](automation.md)。

## 3. 产物与验证

先确认 agent 终态，再停止并重新检查容器，消除其后台子进程继续写入的可能；随后生成完整工作区和选定输出的稳定快照。快照不挂载给 agent；测试验证器只得到另一个副本。工作区后续变化不修改旧快照，继续/交付前会重新核对内容摘要。双重清单检查发现一般并发变更；这不等于对恶意宿主写入者提供文件系统事务。

`outputs.include` 显式选文件；`allow_empty` 默认 false。`outputs.patch=true` 是本阶段补充的可选字段：额外生成可用 `git apply -p1` 应用的 `changes.patch`，包括二进制差异与删除。只要 patch=true，include 可为空。若选定文件已占用 changes.patch 则拒绝，避免覆盖模型产物。patch 同样受排除范围及容量限制。

文件判据支持必要文件、JSON/CSV/UTF-8 文本、必要文本片段、允许修改范围和是否允许无变化。必要文件/格式检查针对最终选定产物，修改范围针对完整冻结工作区与基线的差异，包括删除。允许修改范围不等于交付范围。

测试判据示例：

```toml
[criteria.unit]
kind = "tests"
version = "1"
image = "sha256:替换为64位镜像摘要"
command = ["sh", "-c", "go test ./..."]
timeout_seconds = 60
```

使用不可变镜像摘要，把实际验证程序与版本绑定；镜像/命令/判据变化会使旧证据失效。测试运行在独立 Docker 容器：无网络、无 agent 身份/凭据卷、无 Docker socket、只读根文件系统、drop ALL capabilities、no-new-privileges、1 CPU/512 MiB/128 PIDs，以及 64 MiB 临时目录。工作区副本可写，修改不会写入稳定产物。测试须自行包含离线依赖；需要资源或依赖配置扩展时另行定义可信策略，当前不会临时放开网络。

结果分别为 passed、failed、needs_review、error。agent exit 0 不替代验证；非零测试退出是业务失败，验证器启动/超时/OOM/清理错误是验证故障。证据绑定产物组合摘要、判据配置/版本和观测时间；人工结论另外保存 reviewer、时间与对应摘要。省略 criteria 为 not_requested；显式 `[]` 表示免验证，交付还要求注册表 `allow_unverified=true`。

## 4. 交付、重试与保留

目录交付在目标旁构建临时目录、写入 `.sbx/receipt.json`，再 rename 提交。默认 reject；replace 还需注册表 allow_replace。覆盖先把旧目录移到 `*.sbx-backup-<delivery_key>`，再提交新目录；这两个 rename 之间可能短暂缺少目标，不能声称原子覆盖。中断时保留 stage/backup 并核对后继续；旧备份不会自动删除。

Git 本地目标注册 `kind="git_local"`、仓库 path 和完整 `branch="refs/heads/..."`。交付生成选定产物的本地 tree/commit，通过 compare-and-swap 更新指定分支。它是本地产物分支，不自动修改 checkout、合并源分支、push 或调用远端。replace 只控制已授权的本地分支更新。

交付前先持久化 key、产物摘要、目标绑定；目标锁同时保留 key 绑定日志。相同 key 不能改绑另一产物/目标。该日志最大 1 MiB，损坏或尾部不完整时拒绝新写入，不能删除锁文件绕过。已提交的目标可通过安全回执和实际内容独立核对。任务记录中已完成的交付不会因重试而重新发布；目标后来改变时明确报错。

```sh
sbx task status monthly-report --json
sbx task verify --spec report-task.json
sbx task deliver --spec report-task.json
```

这些命令不重新提交 prompt。仅重试本地交付可在 Docker 不可用时进行；重跑测试仍需要 Docker。每次继续重新核验引用/授权、判据版本和快照；撤权、改绑或快照变化会拒绝继续。未知结果保持可见，不用新 run 绕过。status 不读取配置、不要求 Git、不调用 Docker；它是阶段事实查询，不是全自动资源修复。

恢复提示包括 continue_completion、continue_verification、retry_delivery、reconcile_operations、reconcile_retention，以及沿用的准备/清理核对。收集意图未完成时，verify 会隔离未提交快照并从已停止的工作区重新收集；已完成快照则直接复用。准备/清理未核对时仍保守阻断，不能删除记录伪装成新任务。

快照默认保留，无时间型 GC。确认恢复/交付完成后可显式清理：

```sh
sbx task prune monthly-report <attempt-id>
```

pending review、未决操作、未完成交付或清理失败阻断 prune。清理意图先落盘，重启后可继续；只清理指定快照及其未提交隔离副本，保留清单、判据、回执、输入基线与交付目标。目标旁的覆盖备份、key 日志也保留，需操作者在核对后另外管理。

stdout 是带 `schema_version="sbx.execution/v2"` 的 JSONL；日志在 stderr。退出码沿用 0/2/3/4/5/6/7/124/130，并增加 8 验证不通过、9 待人工判断、10 产物交付失败、11 结果未知。事件发送失败和产物交付失败分别记录。最终状态以进程退出码、结果维度及持久化查询共同判断。
