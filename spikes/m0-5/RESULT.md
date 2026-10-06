# M0-5 结果：Agent 状态 hooks（Claude）

- 日期：2026-10-04
- 环境：`sbx-m05`（M0-4 的测试镜像加上 tmux、jq），**以非 root 用户 node（1000:1000）运行**，HOME 是 volume `m05-home`（登录态从 M0-1 复制过来）；网络用 M0-4 的 shared squid（task-a，已按方案 A 拦截云端 MCP）
- 复现：`./run.sh`（headless 部分）；交互部分的操作步骤见下文
- Codex 的 `notify` 随 Codex 一起推迟到 M3-6

## 结论：✅ 通过，hooks 能可靠地区分 running 和 idle

hooks 通过 `--settings /sbx/hooks/settings.json` 注入，不需要改 `~/.claude/settings.json`；`status.sh` 先写临时文件再 rename。

| 模式 | 事件序列（events.log） | 结果 |
|---|---|---|
| headless `claude -p` | `idle SessionStart → running UserPromptSubmit → running PreToolUse → idle Stop` | ✅ |
| 交互模式（tmux 里的 TUI） | `idle SessionStart → running UserPromptSubmit → running PreToolUse → idle Stop → idle Notification` | ✅ |

- **Stop**：回合结束，表示 Agent 在等输入。把状态设为 `idle`。
- **Notification**：空闲约 **60 秒**后触发（12:18:50 Stop → 12:19:50 Notification），表示"需要人处理"。**`on_idle` 通知适合挂在这个事件上**，而不是 Stop，这样能避免短暂停顿也触发通知。
- **PreToolUse**：可以用来刷新"最后活动时间"。
- tmux 的 `new-session -d` 和 `send-keys` 都能驱动 TUI。attach/detach 机制成立（ADR 0004）。

## 重要发现 1：交互模式需要预先写入引导状态 ⚠️

用 `claude auth login` 登录后，`-p` 能直接使用，但**交互模式的 TUI 会把这次当作第一次启动**：依次弹出主题选择 → 登录方式选择（又要求 OAuth）→ 跳过权限确认的警告。无人值守的场景下，这些对话框会让 Agent 一直卡住。

`sbx` 在创建或启动 Task 前，必须预先写入以下内容：

| 文件 | 字段 | 作用 |
|---|---|---|
| `~/.claude.json` | `hasCompletedOnboarding: true` | 跳过引导和登录方式选择 |
| `~/.claude.json` | `projects["<容器内工作目录>"].hasTrustDialogAccepted: true` | 跳过"信任目录"对话框（工作目录和宿主机路径一致，所以要按 worktree 的绝对路径逐个写） |
| `~/.claude/settings.json` | `theme: "dark"` | 跳过主题选择 |
| `~/.claude/settings.json` | **`skipDangerousModePermissionPrompt: true`** | 跳过 bypass permissions 的警告（实测发现是这个字段；`bypassPermissionsModeAccepted` 不起作用） |

- 这些都是 Claude Code 的内部状态，**版本升级后可能会变**。`sbx doctor` 和 Agent 层的升级流程里应该加一个"冒烟测试"：在 tmux 里启动后检测画面上有没有对话框。
- `~/.claude.json` 同时被 Agent 写入，修改时必须用 jq 加 tmp + rename 的原子方式。

## 重要发现 2：白名单必须包含 `.claude.com`

交互模式启动时会探测 `platform.claude.com`（OAuth 回调和平台服务也在这个域名下），不通就直接报 `ERR_PROXY_TUNNEL` 并退出。**内置白名单要加上 `.claude.com`**（之前只有 `.claude.ai` 和 `.anthropic.com`）。

## 方案 A（拦截云端 MCP）的实测

- claude 在 `mcp-proxy.anthropic.com` 被拦截时，**headless 和交互模式都能正常工作**，方案 A 可行。
- 但它会**持续重试**：这一轮里 `TCP_DENIED(_ABORTED)/403` 一共 94 次。`sbx net denied` 必须把"**按策略拦截**"（云端 MCP、已知遥测）和"**不在白名单里**"分开展示，前者默认不显示。
- 待查（M2）：有没有官方的环境变量或配置能直接关闭 claude.ai MCP 的加载，从源头减少重试。proxy 层的拦截依然作为兜底，不依赖这个开关。

## 其他

- 以非 root 用户运行一切正常，HOME volume 需要 `chown 1000:1000`。`sbx` 创建 `sbx-home` 时要按 Agent 用户的 UID/GID 初始化属主。
