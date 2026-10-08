# sbx 命令手册

> 按"你会在什么时候用到它"递进排列：装一次 → 日常回路 → 会话控制 → 观察 → 网络 → 维护，最后是参数、配置和环境变量的速查。
> 不想要 worktree、想让改动直接落在仓库目录里，看[「我想直接在仓库目录里干活」](#我想直接在仓库目录里干活main-task)。
> 想看一个连续场景从头到尾发生了什么，去 [walkthrough.md](walkthrough.md)；想看组件怎么拼的，去 [architecture.md](architecture.md)。
> 本文描述 M1 + 部分 M2 的**实际代码行为**（`core/internal/cli/`），不是计划。

## 先有的三个概念

| 概念 | 是什么 | 从哪来 |
|---|---|---|
| **Workspace** | 一个 git 仓库。id = `<目录名>-<仓库根路径 sha1 前 6 位>`，例如 `shop-e76272` | `cd` 到仓库里任意位置自动识别 |
| **Task** | Workspace 下的一条工作线。一个 Task = 一个容器 + 一个 worktree + 一个分支 `sbx/<task>` | `sbx run <task>` |
| **main Task** | 省略 task 名时的特例：**直接用仓库根**，不建 worktree，没有专属分支 | `sbx run` |

**几乎所有命令都要在仓库目录里执行**（要靠 cwd 推断 Workspace）。只有 `sbx upgrade` 例外，它只读全局配置。

一句话记住路径：**Task 的改动在它自己的 worktree 里（`~/.sbx/worktrees/<ws>/<task>`），不在你的仓库目录里**，合并分支之后才回到仓库。想让改动直接落在仓库目录里，见[「我想直接在仓库目录里干活」](#我想直接在仓库目录里干活main-task)。

---

## 第 1 层：装一次

```bash
cd core && make build                       # 得到 bin/sbx
ln -sf "$PWD/bin/sbx" /usr/local/bin/sbx    # 可选，放进 PATH
```

需要 Docker Desktop 已启动，内存建议 10GB。

需要宿主机代理时写 `~/.sbx/config.toml`（全部字段见[下文](#配置文件sbxconfigtoml)）：

```toml
[network]
upstream = "http://host.docker.internal:7890"
```

**登录一次，所有 Task 共享。** 第一次 `sbx run` 会因为没有凭据而停下，并把命令打印给你，照抄执行即可：

```bash
docker run -it --rm -e HOME=/home/agent -v sbx-home:/home/agent \
  -e HTTPS_PROXY=http://host.docker.internal:7890 sbx/web-go:<hash> claude auth login
```

凭据落在 volume `sbx-home` 里，`sbx done` 不会删它（只删 `sbx.kind=dep` 的 Task 私有 volume）。OAuth token 过期后在容器里重新登录一次，写的是同一个 volume，所有 Task 立刻跟着生效。

---

## 第 2 层：日常回路

四条命令就是一个完整的生命周期：

```bash
cd ~/code/shop
sbx run fix-login         # ① 开工：建 worktree + 分支 sbx/fix-login，起容器，进 claude
                          #    离开：Ctrl-b 松手再按 d
sbx ls                    # ② 看看它干得怎么样
sbx done fix-login        # ③ 收工：删容器/网络/dep volume/worktree/state，保留分支
git merge sbx/fix-login   # ④ 在主仓库里合并（sbx 不会自动合并）
git branch -D sbx/fix-login
```

### `sbx run [task]`

一条命令干三件事，**按容器是否存在自动分支**：

| 情况 | 行为 |
|---|---|
| 容器不存在 | 新建：worktree → 镜像 → 网络 → 接入 proxy → volume → 容器 → 起 claude。任何一步失败都会逆序清理（worktree 保留） |
| 容器存在但停了 | `docker start`，再确认 claude 在跑 |
| 容器在跑、claude 也在 | 什么都不做，直接 attach |
| 容器在跑、claude 退了（Ctrl-D / `/exit`） | **在原 tmux 会话里重新拉起**，默认带 `--continue` 接上上次对话 |

所以 `sbx run <同一个名字>` 是幂等的恢复命令，手滑退出 claude 之后再跑一次就回来了。

| 参数 | 说明 |
|---|---|
| `--base <ref>` | 新建分支的起点，默认当前 HEAD。Task 已存在时忽略并提示 |
| `-d, --detach` | 只启动不进入，适合批量铺任务 |
| `--fresh` | 重新拉起时开一段新对话（默认接上这个 Task 的上次对话） |
| `--net open\|allowlist` | 只影响本次，不写配置 |

`--base` 用在从某个历史提交或另一分支起步：`sbx run hotfix --base v1.2.0`。分支已存在时，`ls` 里的 AHEAD/DIFF 基准自动取它和 HEAD 的分叉点（merge-base）。

### `sbx done <task>...`

可以一次给多个名字。**不会自动合并，分支一定保留**，结束后打印合并和删分支的命令。

两道防线：

- **未提交的改动** → 直接报错，让你先去提交；`--force` 才丢弃。
- **已提交未合并** → 分支和 commit 都在 `.git` 里，删 worktree 不影响。

删的是：容器、Task 网络、proxy 上的这个用户、`sbx.kind=dep` 的依赖 volume、worktree、state 目录。**不删**：分支、`sbx-home`、`sbx-cache`、镜像。最后一个 Task 结束时顺带停掉 `sbx-proxy`。

同时会提醒一次项目记忆（见 `sbx memory pull`）——趁容器还在的时候读，所以提醒发生在删之前。

---

## 我想直接在仓库目录里干活（main Task）

默认的 `sbx run <task>` 会新开分支 `sbx/<task>` 和一个独立 worktree，改动**不在你的仓库目录里**。想让 Agent 直接改你 `cd` 进去的那个目录、宿主机 `git status` 立刻看得见 —— **省略 task 名**：

```bash
cd ~/code/shop
git switch -c try-refactor   # 建议先自己开一个分支（见下面的代价 ③）
sbx run                      # main Task：直接用仓库根
```

没有 `--no-worktree` 之类的开关，`main` 就是这个用法的全部入口。

| | `sbx run fix-login` | **`sbx run`（main）** |
|---|---|---|
| 工作目录 | `~/.sbx/worktrees/<ws>/fix-login` | **你的仓库根**（bind mount 进容器） |
| 分支 | 新建 `sbx/fix-login` | **不建，用你当前 checkout 的那个分支** |
| `--base` | 生效 | 忽略并提示 |
| `ls` 里的样子 | `sbx/fix-login` + AHEAD / DIFF | `(repo root)`，AHEAD / DIFF 都是 `-` |
| `done` 时 | 删 worktree | **仓库根一个字节不动**，只拆容器 |

### 代价

1. **你和 Agent 在同一棵工作树上。** 它 `git checkout`、`git stash`、改文件，你编辑器里那份跟着变。并行干活基本不可能 —— worktree 方案要解决的正是这个。
2. **一个 Workspace 只有一个 main。** 要同时跑两条线就必须用命名 Task。
3. **它直接提交到你当前分支。** 没有 `sbx/<task>` 这层隔离，不满意只能自己 reset，所以进去之前先开个分支。

> 容器里仍然会用 dep volume 遮住 `node_modules`（`deps.mask`），Agent 装的依赖不会污染你宿主机的那份 —— 这一条在 main 下也成立。

### 其实你未必需要它

worktree 就是宿主机上的真实目录，不是容器内部的东西：

```bash
code "$(sbx path fix-login)"    # 编辑器直接打开，和普通项目没区别
cd "$(sbx path fix-login)"      # git diff / git log 都正常
sbx ls                          # 不进目录也能看 AHEAD / DIFF
```

**怎么选**：要边看边改、随时介入一条线 → `sbx run`（main）。要并行铺几个任务、或者想保留"不满意就扔掉整个分支"的退路 → 命名 Task + `sbx path`。后者是 sbx 的主场景。

---

## 第 3 层：进出会话

```bash
sbx attach fix-login      # 回到 Agent 的 tmux 会话
sbx shell fix-login       # 在同一个容器里另开一个 bash（不打扰 Agent）
sbx stop fix-login        # 停容器，保留一切；sbx run 可恢复
```

三个都要求容器在运行，否则提示你去 `sbx run`。

**离开会话是 `Ctrl-b` 松手再按 `d`**（tmux detach），Agent 继续跑。`Ctrl-D` 是退出 claude 本身 —— 退出后 tmux 会话仍然活着（里面变成一个 shell，`ls` 显示 `exited(agent)`），`sbx run` 可以把它重新拉起来，所以手滑了不丢东西。

`attach` 到一个 claude 已退出的 Task 时会先提示你这件事，再把你放进那个 shell。

**`stop` 和 `done` 的区别**：`stop` 只是关机，容器、worktree、对话全在；`done` 是拆除。中途不干了用 `stop`，干完了用 `done`。

---

## 第 4 层：观察

```bash
sbx ls
sbx path fix-login
code "$(sbx path fix-login)"
cd "$(sbx path fix-login)"
```

`sbx ls` 列出当前 Workspace 的 Task（有容器的 + 有 state 目录的并集）：

| 列 | 含义 |
|---|---|
| `TASK` | Task 名 |
| `STATUS` | 见下表 |
| `BRANCH` | `sbx/<task>`，main Task 显示 `(repo root)` |
| `AHEAD` | 相对 base 的提交数。**`0` 意味着 Agent 还没提交过任何东西** |
| `DIFF` | `3f +10 -2` = 3 个文件、+10 行、-2 行（相对 base 的三点 diff） |
| `LAST-ACTIVE` | hooks 最后一次写状态的时间 |
| `PATH` | worktree 路径（`~` 缩写） |

STATUS 由容器状态和 claude hooks 写的 `status.json` 共同推导：

| 状态 | 含义 |
|---|---|
| `absent` | 没有容器（只剩 state） |
| `stopped` | 容器已停（含 `docker stop` 的 SIGTERM/SIGKILL） |
| `exited(<code>)` / `exited(oom)` | 容器异常退出；`oom` 说明内存不够，调 `resources.memory` |
| `starting` | 容器在跑，但还没收到第一个 hook |
| `running` | Agent 正在干活（`UserPromptSubmit` / `PreToolUse`） |
| `idle` | Agent 停下了：可能干完了，**也可能在等你回话**（`Notification`） |
| `exited(agent)` | 容器还在，claude 退了，会话里是 shell。`sbx run` 可重新拉起 |

> **`idle` 是最需要你亲自看一眼的状态。** 它不区分"任务完成"和"Agent 在问你要不要继续"。配合 `AHEAD 0` 基本可以断定它停在要人确认上 —— attach 进去看最后一句话。

更细的时间线在 `~/.sbx/state/<ws>/<task>/events.log`，每行带日期和时区。

---

## 第 5 层：网络

**默认不拦截**（`network.mode = "open"`，2026-10-08 起）。两种模式都走同一个 Squid（`sbx-proxy`），所以日志一直有，云端 MCP 的策略拦截在两种模式下都生效。

```bash
sbx net denied                 # Agent 被代理拦了什么
sbx net denied fix-login       # 只看一个 Task
sbx net denied --since 2h      # 只看最近两小时
sbx net denied --all           # 连策略拦截、遥测、认证失败一起显示
sbx net allow repo.mongodb.org # 放行域名，写进 ~/.sbx/config.toml 并热加载
sbx run t1 --net allowlist     # 本次用白名单模式
```

`net denied` 默认只显示**"不在白名单"**这一类 —— 也就是真正可能挡住 Agent 干活的。另外两类折叠成一行计数（`--all` 展开）：

| 分类 | 说明 | 要不要管 |
|---|---|---|
| 不在白名单 | allowlist 模式下没放行的域名 | 要。看着像依赖源就 `net allow` |
| 策略拦截/遥测 | `mcp-proxy.anthropic.com`（ADR 0015）、datadog/statsig/sentry | 不用。拦掉是故意的，不影响干活 |
| 认证失败 | 代理鉴权没过 | 异常，说明 Task 的凭据或 proxy 片段有问题 |

`net allow` 会**直接改 TOML 文本而不是重新序列化**，所以你手写的注释和排版都保留；重复执行不动文件。改完立刻对当前 Workspace 里所有运行中的 Task 下发新名单并 `squid -k reconfigure`，不用重启容器。当前是 open 模式时它会提醒你"本来就不拦"。

内置白名单（`core/assets/allowlist/`，allowlist 模式下自动生效，不用你写）：`builtin.txt`（anthropic / claude.ai / openai）、`<profile>.txt`（web-go：npm、pypi、goproxy 等）。你在配置里写的 `network.allow` 是**追加**。域名以 `.` 开头表示连子域一起放行（Squid `dstdomain` 语义）。

> **选哪个模式**：默认 open 换来的是 Agent 能 web search、能装任意依赖，代价是 design §8.2 的"数据外传"缓解在默认配置下不生效。要跑不信任的代码或在意外传，就 `--net allowlist`。理由和放弃了什么，记在 ADR 0005 的 2026-10-08 修订里。

---

## 第 6 层：维护

```bash
sbx memory pull       # 把沙箱里新记的项目记忆导回宿主机
sbx memory pull -y    # 不询问直接写
sbx upgrade           # 升级沙箱里的 claude
```

**记忆是单向自动的**：每次 `sbx run` 把宿主机这个仓库的记忆（`~/.claude/projects/<key>/memory/`）导入沙箱；沙箱里新记的**不会自动回来**，要 `memory pull`（ADR 0016）。它先按文件列出差异（`add` / `update` / `merge` / `conflict` / `keep`），确认后才写；两边都改过的标 `conflict`，不自动合，留给你手处理。`sbx done` 时会提醒一次。

**`sbx upgrade`** 在 Profile 镜像里 `npm view` 查最新版，重建 Agent 层，把版本号记在 `~/.sbx/claude-version`。容器里的 claude 不会自动更新，**已有 Task 仍用旧镜像**，`done` 之后重新 `run` 才换上。配置里固定了 `agents.claude.version` 时它会拒绝执行，让你改配置。这是唯一不需要在 git 仓库里执行的命令。

---

## 宿主机的哪些东西会进沙箱

容器里的 `~/.claude` 是 volume `sbx-home`，**和你宿主机的 `~/.claude` 是两套**。只有这张表上的内容会被带进去：

| 宿主机 | 进去的方式 |
|---|---|
| `~/.claude/CLAUDE.md` | 每次 `run` 快照复制到 `/sbx/gen/host-claude/`，软链到容器的 `~/.claude/CLAUDE.md` |
| `~/.claude/skills/` `agents/` `commands/` | 只读挂载到 `/sbx/host-claude/<name>`，软链进容器的 `~/.claude/` |
| `~/.claude/projects/<key>/memory/` | 每次 `run` 导入；回来要 `sbx memory pull`（ADR 0016） |
| git `user.name` / `user.email` | 读仓库配置，注入成 `GIT_AUTHOR_*` / `GIT_COMMITTER_*` |
| 时区 | 读 `/etc/localtime`，注入 `TZ` |

**不会进去的**：`~/.claude/settings.json`、`~/.claude/scripts/`、`plugins/`、MCP 配置、各种 API key。沙箱的 claude 配置是另外两层：

- `sbx-home` 里的 `~/.claude/settings.json`——preseed 建的（`theme`、`skipDangerousModePermissionPrompt`），**持久且所有 Task 共享**，你在容器里手改的东西留在这里；
- `/sbx/gen/settings.sbx.json`——每次 `run` 重新渲染，用 `--settings` 注入，放 hooks、`disableClaudeAiConnectors` 和状态栏。

**状态栏**是 sbx 内置的，不依赖你宿主机那份：脚本打包在 sbx 二进制里，渲染到 `/sbx/gen/statusline.sh`，显示模型、目录、分支、未提交文件数、上下文占用条和你的上一句话。要换成自己的，改 `core/assets/agent-layer/statusline.sh` 后 `make build`，下次 `run` 生效，**不用重建镜像**。

---

## 参数速查

### 全局

| 参数 | 说明 |
|---|---|
| `-v, --verbose` | 打印执行的每一条 docker 和 git 命令，外加本次生效配置。排查问题第一个加它 |

### 按命令

| 命令 | 参数 | 默认 |
|---|---|---|
| `run [task]` | `--base <ref>` | 当前 HEAD |
| | `-d, --detach` | false |
| | `--fresh` | false（接上次对话） |
| | `--net open\|allowlist` | 跟随配置 |
| `done <task>...` | `--force` | false（脏就报错） |
| `net denied [task]` | `--all` | false（只看"不在白名单"） |
| | `--since <dur>` | 0（全部），例如 `2h`、`30m` |
| `net allow <host>...` | `--project` | **未实现**，待 M2-1，会直接报错 |
| `memory pull` | `-y, --yes` | false（先问） |
| `attach/shell/stop/ls/path/upgrade` | 无 | |

---

## 配置文件：`~/.sbx/config.toml`

M1 只有两层：内置默认值 + 这个文件（ADR 0009）。项目级配置待 M2-1。未知字段只警告不报错。

```toml
profile       = "web-go"      # 内置 Profile，目前只有这一个
default_agent = "claude"
max_running   = 3             # 注意：当前代码只校验合法性，还没有真正限流

[network]
upstream  = ""                # 宿主机代理，空=直连。只支持 http 上游
proxy     = "shared"          # dedicated 未实现，写了会报错
mode      = "open"            # open（默认，全放行）| allowlist（只放白名单）
cloud_mcp = false             # true 则不注入 disableClaudeAiConnectors，也不拦 mcp-proxy
allow     = []                # 追加到内置白名单，只在 allowlist 模式下起作用

[resources]
cpus   = 2
memory = "3g"                 # 形如 3g / 512m
pids   = 1024

[deps]
mask = ["node_modules"]       # 每一项在容器里挂一个独立 volume 遮住，避免污染宿主机

[agents.claude]
version = "latest"            # 写死版本会让 sbx upgrade 拒绝执行
```

**生效优先级**：命令行 flag（`--net`）> 配置文件 > 内置默认值。`--net` 只改这一次执行，不落盘；`sbx net allow` 落盘。

**改了配置什么时候生效**：`network.allow` 对运行中的 Task 可以用 `net allow` 热加载；`resources`、`deps.mask`、`profile` 是建容器时定的，要 `done` 后重建；`mode` 下次 `run` 时重新渲染 proxy 片段。

## 环境变量

| 变量 | 作用 |
|---|---|
| `SBX_HOME` | sbx 数据目录，默认 `~/.sbx`。测试或多套环境隔离用 |
| `SBX_HOST_CLAUDE` | 宿主机 `~/.claude` 的位置，影响记忆导入导出 |
| `TZ` | 有则直接用，否则读 `/etc/localtime` 的软链；注入容器，让 Agent 的日志和提交时间跟你一致 |

## 当前的边界

- **没有 `sbx merge`**，合并永远是你在主仓库手动做；分支有 commit 没合并时 `done` 不会提醒。
- `max_running` 可配但**没有实际限流**。
- `network.proxy = "dedicated"` 和 `net allow --project` 都会明确报错，不是静默失败。
- `sbx ls` 的 CJK 列宽对不齐（tabwriter 按字节算宽度）。
