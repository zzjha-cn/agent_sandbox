# 自动化调用与 v1 执行协议

G2–G4 的通用入口 `sbx task run/verify/approve/reject/deliver/status/prune` 及 `sbx.execution/v2` 的当前子集见 [通用任务手册](general-tasks.md)。它使用独立的任务命名空间和 v2 内部记录；本页 v1 字段、事件投影与 `artifact_delivery=none` 的能力承诺保持不变。G7 再做完整发布与综合验收。

适用于脚本、CI 或外部调度器。日常交互见 [命令手册](commands.md)，正式测试见 [测试指南](testing.md)。sbx 当前是一次性本地 CLI；调用方准备仓库和配置，不需要常驻 worker 或 master。

## 选择调用方式

| 方式 | 用途 | stdout / 退出码 |
|---|---|---|
| `sbx run --agent codex t -p "..."` | 人工使用的 headless 命令 | provider 日志；agent 成功为 0，非零通常返回一般错误 1，不透传原始 agent code |
| `sbx run --agent codex t -p "..." --jsonl` | 本地内部事件调试 | 内部 JSONL，不承诺公开 v1 schema；agent 非零归进程码 5 |
| `sbx run --envelope request.json` | 稳定机器契约 | `sbx.execution/v1` JSONL；分类退出码见下表 |

`--jsonl` 不是 `--envelope` 的别名。需要兼容协议或墙钟超时，应使用 envelope；普通 run 当前没有 `--timeout` 参数。

## 准备与能力发现

在目标 Git 仓库内执行，使用同一个 sbx 二进制：

```bash
sbx --version
sbx version --json > capabilities.json
sbx isolation status --json
sbx config show
sbx trust --show
```

先完成所需的 `sbx trust`、daemon 旧卷接管及 provider 登录。`admit` 和 `run` 不会代替人批准项目配置。若旧卷尚未接管，确认独占该 daemon 后再执行 `sbx isolation adopt`。

v1 从有效配置的顶层 `default_agent` 选择 provider，默认 `claude`。使用 Codex 时，先在个人全局或该仓库的个人 workspace 配置中设 `default_agent = "codex"`。`--envelope` 不接受 `--agent`，也不接受 envelope 中的 `agent` 字段。

```toml
# 个人全局配置或该仓库的个人 workspace 配置；必须在文件顶层
default_agent = "codex"
```

`version --json` 返回 `schema_version`、`application_version`、`providers`、`capabilities`、`max_timeout_seconds`、`envelope_schema` 和 `artifact_delivery`。调用方应确认支持目标版本与所需 capability；新增响应字段可以忽略，破坏性契约变化需要新版本。

## 提交任务

以下文件的仓库由命令的工作目录决定，不是 JSON 中的路径：

```json
{
  "schema_version": "sbx.execution/v1",
  "task_id": "formal-smoke",
  "prompt": "修复约定的问题，运行项目测试，检查差异并提交到当前分支。不要合并或推送。",
  "fresh": false,
  "timeout_seconds": 300
}
```

保存为 `request.json`，然后执行：

```bash
sbx admit --envelope request.json > admission.json
sbx run --envelope request.json > events.jsonl 2> diagnostics.log
```

也可以 `sbx run --envelope - < request.json`。`admit` 只检查配置、信任和当前准入状态，不创建 worktree、容器、卷或 daemon 归属记录，也不预留资源；两次命令之间状态可以变化，所以 run 仍会重新检查。

| 字段 | 约束 |
|---|---|
| `schema_version` | 必填，严格为 `sbx.execution/v1` |
| `task_id` | 必填，`^[a-z0-9][a-z0-9-]{0,39}$`；同一 Workspace 内的稳定 Task 名，`main` 直接使用仓库根 |
| `prompt` | 必填，非空字符串；通过生成文件交给 provider，不进入启动命令参数 |
| `base` | 可选，首次创建分支的起点；已有容器时忽略并提示 |
| `fresh` | 可选，默认 false；启动新一轮会话时不续接旧对话，不重建容器 |
| `timeout_seconds` | 可选，0 或省略表示无墙钟上限；1～86400 秒为本次 attempt 设置截止时间 |

输入为一个 UTF-8 JSON 对象，总大小至多 1 MiB。未知字段、重复字段、大小写别名、`null` 和尾随第二个 JSON 对象均拒绝。超时值需是范围内的整数，JSON 的 `1.0`、`1e2` 也按整数值解析。

envelope 不可与位置参数或 `--prompt`、`--base`、`--fresh`、`--jsonl`、`--detach`、`--net`、`--proxy`、`--cloud-mcp`、`--agent` 混用。网络、认证和资源策略来自本地配置，而不是信封中的任意覆盖。

同一个 Task 不能同时提交两个 headless attempt；活动执行会被拒绝。重试不是幂等重放，可能重复执行工具和外部操作；由调用方决定是否重试。Codex 的恢复按当前 Task 记录的 session ID，不能借 `--agent` 改换已有 Task 的 provider。

## 读取结果与错误

stdout 每行是一个 v1 JSON 对象；stderr 是诊断。不要合并两个流后再当 JSON 解析，也不要用人类提示文案判断状态。执行事件以 `task_id`、`attempt_id` 关联，重复运行同一 Task 会得到新的 attempt ID。

正常执行收尾记录包含 `result`。成功必须同时满足进程退出 0、`result.state = "finished"`、`agent_exit_code = 0`；事件名 `completed` 本身不代表成功。读取 `artifact_state`、`cleanup_state`，不要从单个 agent code 推导全部结果。

| 进程码 | 含义 |
|---|---|
| 0 | 执行成功；version/admit 成功也返回 0，但不是一次已完成的 agent 执行 |
| 2 | 信封输入或版本无效 |
| 3 | 准入拒绝，如活动 Task、并发限制、daemon owner/namespace 不符 |
| 4 | 环境/runner/agent 启动失败；原始 `agent_exit_code` 未必存在 |
| 5 | agent 运行结束且非零；原码在 `result.agent_exit_code` |
| 6 | 结果或事件交付失败 |
| 7 | 内部执行错误 |
| 124 | 超时 |
| 130 | 取消（SIGINT 或 SIGTERM） |

输入、加载配置或信任等早期失败可能只有 `type=failed`、`error_code` 的响应，没有 attempt ID 或 `result`。调用方必须同时处理早期失败、终态结果及进程码。其他 CLI 语法/一般错误仍可能是 1；不能把所有 sbx 命令都按上表解释。

结果中的 `cleanup_state` 为 `not_needed`、`complete` 或 `failed`。清理失败保留原执行分类，详细资源错误在 stderr，不能因为最终 JSON 可解析就视为清理成功。超时/取消会用独立的有界 cleanup context 停止实际执行；创建失败回滚本次拥有的资源、保留 worktree，恢复失败保护旧 Task 状态。全局身份/cache/mise 和归属卷按设计保留。

v1 声明 `artifact_delivery = "none"`、`artifact_state = "not_produced"`：成功表示 agent 执行成功，不保证有提交、测试通过或自动导出产物。代码结果仍由调用方审查 worktree 和分支，sbx 不自动 merge/push。

## 超时、取消和日志保存

- 为无人值守请求显式设置 `timeout_seconds`，包括环境准备和执行时间；预留清理时间，进程可能在截止时间之后才返回。
- 调用方并发排空 stdout/stderr，设置比 sbx 截止时间更长的外部 watchdog。watchdog 先发 SIGTERM，等待退出和清理，最后才考虑强制终止；仅杀死宿主 CLI 不能证明容器和工具已经停止。
- 完全关闭 stdout 管道可能得到 OS SIGPIPE，无法保证还能补发 JSON 或固定退出 6；缺失终态记录应归为交付未确认，并检查 Task 实际状态。
- 保存请求、二进制版本、配置摘要、完整 stdout/stderr、进程码、最终结果及 `sbx ls`/分支检查。prompt、provider 日志和诊断可能包含业务内容，应按业务数据保存；不要导出认证文件或完整容器环境。

## 中断后的持久化状态

`sbx execution status <task> --json` 提供独立的 `sbx.execution-status/v1` 查询。G1 为现有 Git 路径保存阶段意图、结果与历次 attempt，不保存 prompt、凭据或原始错误；事件输出失败不会抹去已记录的执行事实。它不改变 v1 的产物承诺，也不发布尚未实现的 v2。

上次进程在准备资源时被强制终止、清理失败或关键记录损坏，会阻断新 run，需先核对。执行阶段中断或交互/后台交接会提示检查 runtime；显式新 run 仍走 inspect/admit，不能把它当作自动重放旧 prompt。状态查询及各 recovery 值见 [命令手册](commands.md)。记录在 `done/drop` 后保留，单任务历史文件上限 32 MiB；达到上限时拒绝追加，保留原历史，可使用新任务名继续独立工作。
