<h1 align="center">sbx</h1>

<p align="center">
  在 Docker 沙箱里跑 Claude Code —— 跳过权限确认，也不用拿自己的机器冒险。
</p>

<p align="center">
  <a href="https://zzjha-cn.github.io/agent_sandbox/"><b>项目主页</b></a> ·
  <a href="README.md">English</a> ·
  <b>简体中文</b>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white">
  <img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  <!-- <img alt="Status" src="https://img.shields.io/badge/status-M1%20%2B%20M2%20%E9%83%A8%E5%88%86-orange"> -->
</p>

---

要让编码 Agent 真正省事，就得把权限确认关掉；一关掉，它就能动你机器上的任何东西。sbx 把这个取舍换了个地方解决：**每个任务一个容器、一棵独立 worktree、一条独立分支**，容器里 `--dangerously-skip-permissions` 随便开，外面该干净还是干净。

你同时还得到：几个任务**真正并行**跑而不互相踩脚；装一堆依赖不污染宿主机；出网全程走代理，**事后查得到它访问过什么**。

```console
$ cd ~/code/shop
$ sbx run fix-login          # 建分支 + 建容器 + 进 Claude Code，一条命令
$ sbx ls
workspace shop-e76272 (/Users/you/code/shop)
TASK        STATUS   BRANCH          AHEAD  DIFF         LAST-ACTIVE  PATH
add-search  idle     sbx/add-search  2      7f +210 -14  12m ago      ~/.sbx/worktrees/shop-e76272/add-search
fix-login   running  sbx/fix-login   1      3f +48 -6    8s ago       ~/.sbx/worktrees/shop-e76272/fix-login
```

`Ctrl-b` 松手再按 `d` 就离开，Agent 继续跑。回来 `sbx attach fix-login`，干完 `sbx done` 再 `git merge`。

## 它解决什么

| 痛点 | sbx 的做法 |
|---|---|
| 想关权限确认，又怕 Agent 删错东西 | 它只看得到一棵 worktree、一个容器；宿主机其余部分根本没挂进去 |
| 两个任务同时改同一个仓库会打架 | 每个 Task 一棵 git worktree + 一条 `sbx/<task>` 分支，物理隔离 |
| Agent 装的依赖弄脏了本地环境 | `node_modules` 之类用 volume 遮住，容器内外互不可见 |
| Agent 跑了一晚上，不知道它干了什么、连了哪 | 状态写进 hooks（`sbx ls` 可见），出网全走 Squid（`sbx net denied` 可查） |
| 离开终端 Agent 就断了 | Agent 跑在容器内的 tmux 里，关掉终端、重启宿主机都还在 |

## 快速开始

**前提**：Docker Desktop 已启动（建议给到 10GB 内存）、Go 1.27、一个 Claude 订阅。

### 1. 装

```bash
cd core
make build
ln -sf "$PWD/bin/sbx" /usr/local/bin/sbx   # 可选
```

### 2. 配（需要宿主机代理时才要）

`~/.sbx/config.toml`：

```toml
[network]
upstream = "http://host.docker.internal:7890"   # 留空表示直连
```

### 3. 登录（只需一次，之后所有 Task 共享）

```bash
sbx login            # 在一个临时容器里打开 claude 的登录流程
sbx login --status   # 现在登录的是哪个账号
```

照提示打开链接、粘贴授权码。凭据存在 volume `sbx-home` 里，所有 Task 共享，不在任何 Task 内部。`sbx login --logout` 清掉，`--force` 换账号。

### 4. 跑第一个任务

```bash
cd ~/code/shop
sbx run fix-login
```

就这样。worktree、分支、容器、网络、代理凭据都建好了，Claude Code 已经在里面等你。

## 工作流程

```bash
sbx run fix-login              # 开工（再跑一次 = 恢复，并接上上次对话）
sbx run add-search --detach    # 再铺一个，不进去
sbx ls                         # 谁在跑、提交了几个、改了多少、多久没动静
sbx attach fix-login           # 回到会话（Ctrl-b 松手再按 d 离开）
code "$(sbx path fix-login)"   # 用编辑器看它改了什么
sbx done fix-login             # 收工：拆容器，留分支
git merge sbx/fix-login        # 合并（sbx 不会自动合并）
```

**`Ctrl-D` 是退出 Claude，不是离开。** 但退出后 tmux 会话还在，`sbx run` 可以重新拉起并接上上次对话 —— 手滑不丢东西。

想让改动直接落在仓库目录里、宿主机 `git status` 立刻可见？**省略 task 名**：`sbx run` 会用 main Task 直接操作仓库根，不建 worktree 也不建分支。代价和适用场景见[文档](docs/commands.md#我想直接在仓库目录里干活main-task)。

## 核心模型

| | 是什么 |
|---|---|
| **Workspace** | 一个 git 仓库，`cd` 进去自动识别 |
| **Task** | 一条工作线 = 一个容器 + 一棵 worktree + 一条分支 `sbx/<task>` |
| **main Task** | 省略 task 名的特例：直接用仓库根，不隔离 |

容器里能看到的只有：这棵 worktree、共享的 `sbx-home`（登录态 + Claude 配置）、`sbx-cache`（包管理器缓存）、只读的 `/sbx/gen`（hooks、settings、状态栏）。**宿主机其余目录一概没挂。**

## 网络

出网一律经过一个共享的 Squid（`sbx-proxy`），**默认不拦截**。

```bash
sbx net denied                 # 它被拦了什么（--all 看全部分类，--since 2h 限时间）
sbx net allow fastdl.mongodb.org   # 放行，并对运行中的 Task 热加载，不用重启
sbx run t1 --net allowlist     # 这一次按白名单跑
```

白名单模式（`network.mode = "allowlist"`）只放行内置名单加你配的域名，适合跑不信任的代码。两种模式都经过 Squid，所以日志一直有；云端 MCP 的策略拦截两种模式下都生效（[ADR 0015](docs/CONTEXT.md)）。

> 默认从 allowlist 翻成 open 是 2026-10-08 的决定：白名单挡住 web search 和依赖下载时，Agent 会悄悄找替代方案并报告"测试通过"，这种结果比拦截本身更危险。取舍记在 ADR 0005 的修订里。

## 配置

`~/.sbx/config.toml`，全部字段可选：

```toml
profile = "web-go"            # 内置 Profile（目前只有这一个）

[network]
upstream  = ""                # 宿主机代理，空 = 直连
mode      = "open"            # open | allowlist
allow     = []                # 追加到内置白名单

[resources]
cpus = 2 ; memory = "3g" ; pids = 1024

[deps]
mask = ["node_modules"]       # 每项挂一个 volume 遮住，不污染宿主机
```

配置一共四层，后者覆盖前者、列表取并集：内置默认值 → `~/.sbx/config.toml` → `<repo>/.sbx/sandbox.toml`（提交进仓库，队友共享）→ `~/.sbx/workspaces/<ws>.toml`（你对这一个仓库的个人覆盖）。

```bash
sbx config show    # 生效配置，外加每个值来自哪一层
sbx trust          # 确认这个仓库的 .sbx/ 内容
```

项目层跟着仓库走，所以**密钥类字段和绝对路径在那一层会直接报错**。也正因为它会被 `git pull` 改掉，`.sbx/` 和上次确认的内容不一致时 `sbx run` 会停下来给你看 diff；没有 `.sbx/` 的仓库完全碰不到这套东西。字段全集和改完什么时候生效：[docs/commands.md](docs/commands.md#配置四层adr-00090010design-91)。

## 文档

| 文档 | 讲什么 |
|---|---|
| [项目主页](https://zzjha-cn.github.io/agent_sandbox/) | **从这里开始**：日常场景、架构图、一问一答，网页版（英文） |
| [commands.md](docs/commands.md) | **命令手册**：每条命令、每个参数、配置字段、环境变量 |
| [walkthrough.md](docs/walkthrough.md) | 一个连续场景从头到尾：每条命令背后 Docker / git / squid 各做了什么 |
| [architecture.md](docs/architecture.md) | 组件怎么拼的，数据往哪流 |
| [design.md](docs/design.md) | 完整设计与取舍 |
| [CONTEXT.md](docs/CONTEXT.md) | 术语表与决策索引 |

## 项目状态

**M1（MVP）完成，M2 进行中。** 可用命令：`run / attach / shell / stop / ls / path / done / net / memory / login / config / upgrade`。

已知边界：内置 Profile 只有 `web-go`；网络只有 shared proxy（dedicated 待 M2-7）；没有 `sbx merge`。

路线图见 [implementation-checklist.md](docs/implementation-checklist.md)。

## 开发

```bash
make test          # 单元测试
make test-docker   # 带 docker 的集成测试（首次会构建镜像，要几分钟）
make lint          # golangci-lint；没装则退回 go vet + gofmt
make e2e           # 端到端验收（需要已登录）
```

加 `-v / --verbose` 可以打印执行的每一条 docker 和 git 命令。

sbx 只通过 `docker` 和 `git` 两个 CLI 干活，不用 SDK（ADR 0013）；Dockerfile、squid 模板、白名单都 `go:embed` 进二进制，跑起来不依赖源码目录。

## 许可

[Apache-2.0](LICENSE)。仓库里的全部内容 —— Dockerfile、squid 模板、hooks、状态栏脚本 —— 都是为 sbx 写的，同一许可。
