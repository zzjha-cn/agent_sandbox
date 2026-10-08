# 2026-10-06 · claude 退出后 Task 无法恢复的修复

> 承接 [2026-10-06_m1-implementation.md](2026-10-06_m1-implementation.md)。M1 交互使用中发现的问题。

## 一句话

交互模式下按 `Ctrl-D`（或 `/exit`）退出 claude 后，整个 tmux 会话跟着消失，`sbx attach` 再也进不去，而 `sbx ls` 还显示 `idle`。现在 claude 退出后会话保活、状态如实显示 `exited(agent)`、`sbx run` 能在原会话里接着上次对话重新拉起。

## 现象和定位

用户在真实 Task（`wd-ts`）里按了 `Ctrl-D`，之后：

- `sbx attach wd-ts` 报"Agent 会话不存在"；
- `sbx ls` 显示 `idle`，看上去像还在工作；
- 容器本身是好的：`tini → sleep infinity` 还在，但 `tmux ls` 报 `no server running`，`events.log` 停在最后两条 `SessionStart`。

三件事叠在一起：

| # | 原因 | 位置 |
|---|---|---|
| 1 | `Ctrl-D` 是 claude 的退出（EOF），不是 tmux 的 detach（detach 是 `Ctrl-b` 松手再按 `d`）。两者太容易混 | — |
| 2 | tmux 窗口的根进程就是 claude。claude 一退，窗口关闭 → 这是唯一的窗口 → 会话结束 → tmux server 退出。没东西可以 attach | `agent/runtime.go` 的 `StartClaude` |
| 3 | 状态只看"容器在跑"+ `status.json` 的最后一次 hook 写入，没人确认 claude 还在不在。`SessionEnd` 也没注册 hook | `task.Derive`、`agent.hookEvents` |

## 改动

### 1. 会话保活

tmux 窗口跑的命令从裸的 `claude …` 改成：

```sh
claude … ; printf '\n[sbx] claude 已退出。…\n'; exec bash -l
```

claude 退出后窗口里换成一个 login shell，窗口不关、会话不死，`sbx attach` 随时能进去，工作目录还是 worktree，可以直接跑 git。已在 `sbx/web-go` 镜像里实测：claude 退出后 `has-session` 仍然 ALIVE。

### 2. `SessionEnd` hook

`settings.sbx.json` 增加第 6 个 hook：`SessionEnd → exited`。`task.Derive` 把它渲染成 **`exited(agent)`**（容器还在，claude 已退出），不再假装 idle。

### 3. `sbx run` 能重新拉起，并默认接上上次对话

- `startAgent` 原来是"会话存在就直接返回"，现在改成"会话存在**且** claude 还在跑才返回"；会话还在但 claude 已退出时，用 `tmux respawn-window -k` 在**同一个窗口**里重开，已经 attach 的人不用重进。
- 这个 Task 以前跑过 claude（`state/<ws>/<task>/events.log` 存在）时，默认带 `--continue` 接上上次对话。新建 Task 不带（避免 `--continue` 在没有历史时报错）。
- 新增 `sbx run --fresh`：不接上次对话，开新的一段。

### 4. 冒烟检查

会话不再随 claude 退出而消失，所以 `SmokeCheck` 不能再靠"会话没了"判断启动失败。改成看 `status.json` 是否变成 `exited`，或画面上是否出现退出提示。

### 5. attach 的提示

`attach` 在 claude 已退出时先打印一句"会话里现在是一个 shell，用 `sbx run <task>` 重新拉起"，然后照常进去。

## 文件

| 状态 | 文件 |
|---|---|
| 修改 | `core/internal/agent/runtime.go`（`ClaudeCmd`、`StartClaude`、`RespawnClaude`、`SmokeCheck`）、`core/internal/agent/agent.go`（`SessionEnd` hook）、`core/internal/task/task.go`（`Derive`、`HasPriorSession`）、`core/internal/cli/run.go`（`--fresh`、`startAgent`）、`core/internal/cli/cmds.go`（attach 提示） |
| 测试 | `task_test.go`（`exited(agent)`）、`agent_test.go`（6 个 hooks、`TestClaudeCmdKeepsSessionAlive`） |
| 文档 | `README.md`、`docs/architecture.md`（§6.1、§6.2、§7.3，新增 §7.6）、`docs/walkthrough.md`（新增第 12 幕）、`docs/design.md` §7.2、`docs/CONTEXT.md` 术语表 |

## 验证状态

| 项 | 结果 |
|---|---|
| `make test` | 通过 |
| tmux 保活和 `respawn-window -k` 在 `sbx/web-go:dc6755ad157f` 里实跑 | 通过（claude 退出后会话 ALIVE，respawn 后仍在同一窗口） |
| 真实 Task 上验收 | 通过 |
| `make test-docker` / `make e2e` | 未跑 |

## 留下的问题

- claude 被 SIGKILL 时不会触发 `SessionEnd`，状态会停在最后一次写入。这种情况下会话里也不会留下 shell（窗口直接没了），表现回退成改动前的样子：`sbx run` 会新建会话恢复。
- `--continue` 依据的是 `events.log` 是否存在，不是真的去问 claude 有没有这个目录的历史。极端情况（claude 换了会话存储位置、sbx-home 被重建）下 `--continue` 可能启动失败，冒烟检查会报出来，用 `--fresh` 可以绕过。
