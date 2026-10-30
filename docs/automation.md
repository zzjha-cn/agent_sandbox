# 自动化与统一执行协议

2026-10-30：开发阶段收敛后，机器任务输入、JSONL 事件与执行记录统一使用 v2。`sbx.execution/v1` 输入已停止支持。简单 Git/交互式命令继续保留，内部共用 `execution.Service`；G7 已发布完整结构 schema、能力条件、恢复指引和迁移规则；组织治理仍属于 G8。

## 选择入口

| 用途 | 命令 | 工作区与完成语义 |
|---|---|---|
| 交互式代码会话 | `sbx run fix-login` | 保留 Git worktree/分支；省略任务名使用 main 仓库目录 |
| 仓库内一次性 headless | `sbx run fix-login -p "修复问题" --jsonl` | 保留 Git 工作区语义；输出统一 v2 事件，不自动声明产物验证或交付 |
| 显式通用任务 | `sbx task run --spec task.json` | 按可信 Source 准备隔离副本，执行判据与 Sink |
| 显式任务的便捷入口 | `sbx run --spec task.json` | 与 `task run --spec` 使用相同提交函数、超时、事件及执行流程 |
| 只读准入 | `sbx admit --spec task.json` | 检查任务/策略、provider 能力、记录恢复门禁和 runtime 准入，不预留资源 |

`run/admit --envelope` 仅保留为 `--spec` 的参数别名，接受同一个 v2 TaskSpec，不再选择 v1 流程。`run --spec` 不允许位置任务名或 `--prompt/--base/--fresh/--jsonl/--detach/--net/--proxy/--cloud-mcp/--agent` 覆盖；provider 由 TaskSpec 明确选择。

## 提交任务

操作者先在 `$SBX_HOME/execution-policy.toml` 注册 Source、判据、Sink 和所需服务授权，具体可复制示例见 [通用任务规范](general-tasks.md)。例如，注册好 input、report 和 reports 后：

```json
{
  "schema_version": "sbx.execution/v2",
  "task_id": "monthly-report",
  "prompt": "读取输入，生成包含 rows 和 total 的 report.json。",
  "agent": {"provider": "codex"},
  "source": {"kind": "files", "ref": "input"},
  "outputs": {"include": ["report.json"]},
  "criteria": [{"id": "report", "kind": "files", "ref": "report"}],
  "sink": {
    "kind": "local_directory",
    "target_ref": "reports",
    "delivery_key": "monthly-report-001"
  },
  "limits": {"timeout_seconds": 300}
}
```

```bash
sbx version --json
sbx admit --spec task.json > admission.json
sbx task run --spec task.json > events.jsonl 2> diagnostics.log
# 等价提交入口：sbx run --spec task.json
sbx task status monthly-report --json
```

显式任务无需当前目录是 Git 仓库。省略 source 代表空工作区，不代表当前仓库；需要仓库内容时注册 Git Source。`admit` 不创建工作区、容器、执行记录或 daemon 归属记录；它不是完整源文件预扫描或未来执行保证，run 会在锁和各阶段内重新检查。

任务是单个 UTF-8 JSON 对象，最多 1 MiB。顶层与嵌套对象均拒绝未知字段、重复字段、大小写别名、null 和尾随 JSON。可选字段应省略；`criteria` 省略与显式空数组的语义仍不同。`limits.timeout_seconds` 为 0～86400 的整数值，支持精确表示整数的 `1.0`、`1e2`；0 或省略表示不额外设置截止时间。

`version --json` 提供当前 schema、应用版本、providers、capabilities、本地交付类型和最大超时。新增 `schemas`、`capability_details`、`record_schema`、`compatibility`。`sbx schema` 列出契约名，`sbx schema task` 离线导出 Draft 2020-12 Schema；已发布文件及结构/授权检查边界见 [schema 目录](schemas/README.md)。能力表示程序实现，不表示当前机器已配置授权或 provider 登录有效；本地可执行性仍以 admit 和逐次检查为准。旧 `envelope_schema` 已移除。

## 事件与结果

所有机器执行事件由同一个 JSONL sink 输出，每行带 `schema_version="sbx.execution/v2"`，包含 task/attempt 身份和安全的阶段元数据。stdout 与 stderr 分开消费，避免将 provider 日志当成协议事件。

最终 `result` 保留全部结果维度：执行、验证、产物交付、事件交付、清理和阶段状态；新增 `mode` 与 `workspace_kind`，显式区分 interactive/headless 和 repository/git_worktree/isolated_copy。Git 便捷命令没有声明产物判据时，验证和交付为 not_requested，不能据 agent 成功推导业务完成。

内部阶段错误可携带更具体的 stage；不再使用旧 v1 的有损阶段/结果投影。调用方应依据 error_kind、结果维度和进程退出码判断，不依赖人类日志或仅看 completed 事件。

| 进程码 | 含义 |
|---|---|
| 0 | 当前命令成功；version/admit 的成功不表示任务已执行 |
| 2 | 任务输入/版本无效 |
| 3 | 准入或授权拒绝 |
| 4 | 环境、启动或验证设施故障 |
| 5 | 机器输出模式下 agent 非零退出，原码保存在结果中 |
| 6 | 事件/响应输出失败 |
| 7 | 内部错误、执行记录损坏或不兼容 |
| 8 | 完成判据不通过 |
| 9 | 等待人工判断 |
| 10 | 产物交付失败 |
| 11 | 外部操作结果未知 |
| 124 / 130 | 超时 / 取消 |

普通人类 Git headless 模式的 agent 非零仍按一般 CLI 错误返回 1；机器集成使用 --jsonl 或显式任务。输入及策略等早期错误可能只有 failed/error_code，没有 attempt 或 result。流程已经开始的失败可能输出多行事件，调用方必须逐行解析。进程码、终态事件及持久化记录需要共同核对。

## 恢复与副作用

```bash
sbx task verify --spec task.json
sbx task deliver --spec task.json
sbx task reconcile --spec task.json
sbx execution status fix-login --json  # 仓库便捷任务
```

verify/deliver/reconcile 不重新提交 prompt。外部写结果未知时先查询，禁止自动重发或用新 attempt 绕过；动态读取与声明式写入分别见 [G5](dynamic-tools.md) 和 [G6](external-operations.md)。取消不等于远端回滚。

Git 和通用任务统一由同一 FileStore 读写 `sbx.execution-record/v2`；工作区和记录目录仍按任务身份隔离，不因为统一格式而把两种任务合并。Git status 使用 `sbx.execution-status/v2`，通用任务 status 使用 `sbx.task-status/v2`，后者还包含工具审计。两者新增 `next_actions`，包含 action、带占位符的 command、requires_spec 和 reason；仅提供恢复指引，不授予权限。record_invalid 或 prepare/cleanup/retention 未决仍要求人工核对。

## 开发期迁移

这是主动采用的不兼容变更，不提供 v1 隐式升级或降级：

1. v1 输入改为 v2，明确 agent、source、criteria 与交付选择；顶层 timeout_seconds 移到 limits。base/fresh 属于 Git 便捷命令选项，不能机械塞进 v2 TaskSpec。
2. 原调用方若依赖当前仓库/worktree，使用 `run <task> -p ... --jsonl`；需要稳定产物和验证交付则注册 Git Source，改用显式任务。
3. 旧 v1 磁盘记录保留原样，新二进制读取时报不兼容并阻断执行。使用旧二进制核对原 SBX_HOME 的未决操作/资源后，选择新的 SBX_HOME 重新配置并登录；不自动复制身份、迁移记录或删除历史。
4. 已有 v2 通用记录可继续读取；旧记录未保存的 mode/workspace_kind 可能为空，不伪造历史事实。

新目录代表独立任务身份，不能用它重复提交旧 home 中结果未知的业务操作。保留原 home、旧二进制和回执直到核对完成。服务端写入没有通用 Undo。

收敛实现与本轮验收见 [架构收敛记录](architecture-convergence.md)。

旧 meta/config 的读取、升级和回退规则见 [迁移规范](migration.md)，完整发布验收见 [G7](g7-acceptance.md)。


## G8 组织策略与事件 outbox

安装固定 root 策略后，`run --spec` / `task run --spec` 与恢复命令执行组织和个人权限交集；Git 任务使用 Git Source。受管模式拒绝交互/detach/直接 shell 等无法持续监督的入口，未安装策略的个人用法保留。策略任意更新、禁用、到期或失效会取消当前活动，恢复时重新授权，未知业务写继续先核对。

`sbx policy show` 显示有效上限和摘要；`sbx schema team-event` 导出 `sbx.team-event/v1`。事件先写私有本地 outbox，`sbx events status` 查看积压，`sbx events flush` 使用独立成员 token 显式上传。`event_delivery_state` 不代表远端 collector 已收件。完整安装、脱敏、访问控制和可绕过边界见 [组织规范](organization.md)，实测见 [G8](g8-acceptance.md)。
