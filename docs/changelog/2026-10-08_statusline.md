# 2026-10-08 内置状态栏 + 命令手册

## 内置 statusLine

**问题**：宿主机 `~/.claude/settings.json` 里的 `statusLine` 在沙箱里完全不生效 —— sbx 只把 `CLAUDE.md`、`skills/`、`agents/`、`commands/` 和项目记忆带进容器（`agent.hostDirs`），`settings.json` 和 `scripts/` 都不在列。容器里的 `~/.claude/settings.json` 是 preseed 自己建的另一份。

**做法**：把脚本打包进 sbx 二进制，和 hooks 走同一条路。

- `RenderGen` 把它渲染到 `<state>/gen/statusline.sh`（0755），容器里是只读的 `/sbx/gen/statusline.sh`。
- `SettingsJSON` 加上 `"statusLine": {"type":"command","command":"/sbx/gen/statusline.sh"}`。

**为什么不放镜像、不放 volume**：

| 方案 | 问题 |
|---|---|
| 烤进基础镜像 | 改一行脚本就改了镜像 hash，所有 Task 得 `done` 重建才能换上 |
| 写进 `sbx-home` volume | 共享可变状态，没有升级路径，和用户手改的内容混在一起 |
| **渲染进 `/sbx/gen`** | 跟着 `make build` 走，下次 `run` 生效，只读，不碰 volume |

依赖 `bash`、`jq`、`git`，Agent 层镜像里本来就有，**不用重建镜像**。

**验证**：在 `sbx/web-go:dc6755ad157f` 里喂真实形状的 statusLine JSON 跑了一遍，输出正常：

```
Opus 5 | 📁work | 🔀master (3 files uncommitted, no upstream) | ███░░░░░░░ 31% of 200k tokens
💬 把白名单改成可选
```

## docs/commands.md（新增）

按"什么时候用到"递进的命令手册：三个概念 → 装一次 → 日常回路 → 会话 → 观察 → 网络 → 维护，外加参数速查、配置全字段、环境变量和当前边界。新增一节「宿主机的哪些东西会进沙箱」，就是上面这个问题的正面回答。

单独写出来的三件事：`sbx run` 的四种分支行为；`idle` 不区分"干完了"和"在等你"；open / allowlist 的取舍。

另加一节「我想直接在仓库目录里干活（main Task）」：用户问"有什么参数能直接在本地分支操作"，答案是**没有参数，省略 task 名就是**。这个意图之前只在概念表里顺带提了一句，没人能从"main Task"这个名字反推出它解决的是"想在宿主机直接看到变更"。新一节把两种模式并排对比，并写明三个代价（共用工作树、一个 ws 只有一个 main、直接提交到当前分支），以及"其实 worktree 也在宿主机上，`code "$(sbx path x)"` 就能打开"这个多数情况下更好的退路。

## 顺带修的

- `docs/walkthrough.md` 第 0 幕写着"网络：allowlist 模式"，默认翻转后就错了，而全文拦截叙事都建立在这个前提上 —— 改成在场景配置里显式写 `mode = "allowlist"` 并标注默认已是 open；放行域名的说明换成 `sbx net allow`。
- `docs/CONTEXT.md` 的「命令草图」加声明：那是设计期草图含未实现命令，以 `commands.md` 为准。
- `sbx net denied` 的提示文案还在教人手改 config.toml 再重启，改成 `sbx net allow`。

## 已知问题

- 用户宿主机的 `settings.json` 指向 `~/.claude/scripts/context-bar.sh`，实际文件名是 `ctx_bar.sh`，宿主机那份状态栏本身就没生效（和沙箱无关）。
- 沙箱状态栏的配色/风格写死在脚本里，没有做成配置项。
