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

从发布包装（`sbx` 是单个二进制，只调用 `docker` 和 `git`）：

```bash
tar -xzf sbx_<版本>_<os>_<arch>.tar.gz
sudo install -m 0755 sbx /usr/local/bin/sbx
sbx --version
```

或者从源码：

```bash
cd core
make build
ln -sf "$PWD/bin/sbx" /usr/local/bin/sbx   # 可选
make release                               # 交叉编译 darwin+linux × amd64+arm64 到 dist/
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

容器里能看到的只有：这棵 worktree、你仓库的 `.git` 目录、共享的 `sbx-home`（登录态 + Claude 配置）、`sbx-cache`（包管理器缓存）、只读的 `/sbx/gen`（hooks、settings、状态栏）。**宿主机其余目录一概没挂。**

> **`.git` 是可写挂载的，而 `.git/hooks` 和 `.git/config` 以只读盖在它上面。** 仓库必须可写：沙箱里的提交能直接出现在你宿主机的 `git log sbx/<task>` 里、不需要任何同步，靠的就是这个。但"写一下就能在宿主机上执行代码"的那两个口子——埋一个 `pre-commit`，或者把 `core.hooksPath` 指到 Agent 控得住的地方——已经堵上了（submodule 一并处理）。想退回没有防护的行为就写 `git.protect = false`；它是安全开关，项目层开关都不许设。
>
> 剩下的风险：Agent 仍然能改写 `sbx/<task>` 的历史，以及你自己的工具从 `.git` 里读的、hooks 和 config 以外的东西。这是 [design.md](docs/design.md) 里的 R1——现在是**已缓解**，不是已消除。真正不可信的 Agent 或代码库，还是请用一个用完就扔的 clone。

## 网络

出网一律经过 Squid，默认是所有 Task 共用一个（`sbx-proxy`），**默认不拦截**。

```bash
sbx net denied                 # 它被拦了什么（--all 看全部分类，--since 2h 限时间）
sbx net allow fastdl.mongodb.org   # 放行，并对运行中的 Task 热加载，不用重启
sbx run t1 --net allowlist     # 这一次按白名单跑
sbx run t1 --proxy dedicated   # 这一次用独占的代理 sidecar
```

白名单模式（`network.mode = "allowlist"`）只放行内置名单加你配的域名，适合跑不信任的代码。两种模式都经过 Squid，所以日志一直有；云端 MCP 的策略拦截两种模式下都生效（[ADR 0015](docs/CONTEXT.md)）。

> 默认从 allowlist 翻成 open 是 2026-10-08 的决定：白名单挡住 web search 和依赖下载时，Agent 会悄悄找替代方案并报告"测试通过"，这种结果比拦截本身更危险。取舍记在 ADR 0005 的修订里。

## 伴生服务，和够不着的东西

Task 的网络是 `--internal` 的：容器**没有默认路由**，能出 HTTP 只是因为 Squid 就在这张网上。有两样东西能在不动这条不变量的前提下往里加东西——它们都是 Task 网络上的 sidecar，而不是给 Task 自己再插一张网卡。

```toml
# <repo>/.sbx/sandbox.toml —— 可以进仓库：没有地址、没有凭据、没有宿主机路径
[[services]]
name  = "db"
image = "postgres:16"
port  = 5432
env   = { POSTGRES_PASSWORD = "dev", POSTGRES_DB = "app" }
```

跟着 Task 起、被 `sbx done` 一并回收的一次性实例。容器里按 **`db:5432`** 访问——用它的名字，不是 `localhost`：它是另一个容器，而 `localhost` 在 Task 里指的是 Task 自己。Agent 真失控时最坏的后果是毁掉一个本来就一次性的库，所以这一类是首选。

```toml
# ~/.sbx/config.toml 或 ~/.sbx/workspaces/<ws>.toml —— 不能写在项目层
[[forward]]
name = "staging-db"
port = 5432
to   = "db.corp.internal:5432"   # 只写 `to = 5432` 表示你本机的端口
# host = "gitlab.corp.internal"  # 可选：容器里仍用真实域名，证书照样能校验
```

一条一个 `socat` sidecar，容器里按 `staging-db:5432` 访问。它是 Task 网络上的一个普通 peer，不是 Task 的网关，所以 Task 仍然没有自己的出口。三条约束让它是"你开的洞"而不是"你继承来的洞"：

- **`to` 必须解析到私有地址。** 指向公网的话，forwarder 就成了绕过 Squid 的通用隧道——不过滤、不记日志、`sbx net denied` 也看不见。公网访问走 `network.allow`。（内部真的在用公网段的公司可以写 `allow_public = true` 单条解除，会有醒目警告。）
- **项目层不能声明。** 跟着仓库走的配置不该决定你这台机器往哪儿连。
- **启动时解析一次，IP 固化进 sidecar。** `sbx run` 逐条打印实际连到的地址，`sbx ls` 标出哪些 Task 接过，Task 的 `meta.json` 把这件事记下来——事后 review 分支时你知道该用什么尺度看。

接上真实的共享数据库，就意味着网络层不再是护着你的那一层了：Agent 在那头做的事都是真的。请用只读账号或独立 schema，并考虑给这个 Workspace 切 `network.mode = "allowlist"`——沙箱里一旦有了真实数据，守住出口就重新值钱了。

### 明确不提供

这些是决定不做，不是还没做。写下来本身就是目的：不写的话，第一个需要 testcontainers 的人会自己挂上 docker.sock，而且不会知道代价是什么。

| | 为什么不 | 替代做法 |
|---|---|---|
| `/var/run/docker.sock` | 等于宿主机 Docker 的完全控制权：特权容器、你的根文件系统。比 R1 严重一个量级，而且是立即生效的 | `[[services]]` |
| 云厂商凭据、kubeconfig | 爆炸半径最大的一项——花钱、删资源、拖数据 | 这类操作在宿主机做 |
| SSH key 进容器 | 等于你所有仓库的推送权。分支本来就在宿主机上，`git push` 就该在那儿做 | 在宿主机 `git push` |
| `host.docker.internal` 直通 | 一旦可达，容器能连你本机**每一个**监听端口：别的项目的 dev server、所有本地数据库。没有"只开 5432"这回事 | `[[forward]]`，一条一个端口 |
| 给 Task 容器加非 internal 网卡 | 那一刻它就有了默认路由，白名单、策略拦截、访问日志同时失效 | 上面那两种 sidecar |
| 运行期凭据注入、设备 / `--gpus` / `--cap-add` | 是暂不提供而不是永不：常见场景已经被别的方式覆盖，而能力透传需要先有一套声明机制 | — |


## 配置

`~/.sbx/config.toml`，全部字段可选：

```toml
profile = "web-go"            # 内置 Profile：web-go | py-rust
# image = "ghcr.io/me/dev:1"  # 或者直接用现成镜像，覆盖 profile

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

## 用你自己的镜像

内置 Profile 只是图方便，不是必须的。决定 Task 跑在什么环境里有三条路，优先级从高到低：

```toml
image = "ghcr.io/me/devbox:2026-10"   # 1. 直接用现成镜像
```
```bash
<repo>/.sbx/Dockerfile                # 2. 仓库里放一个 Dockerfile，不用写任何配置
profile = "web-go"                    # 3. 内置 Profile（默认）
```

不管选哪条，**你只需要提供语言环境**。sbx 会自动在上面叠一层 Agent 层——tini、tmux、git、ripgrep、Node、claude-code、mise，以及一个 UID 和你一致的 `agent` 用户——claude 不用你自己装。镜像 tag 是输入内容的 hash，底换了会自动重建。

动手前有两件事要知道：

- **底必须是 Debian 或 Ubuntu 系。** Agent 层用 `apt-get` 装东西，Alpine 或红帽系的底会在构建时挂掉，而且报出来的是 apt 的错，不是 sbx 的提示。
- **依赖遮盖跟着 `profile` 走，不跟着镜像走。** 默认遮盖项是按 `profile` 字段查的，而设 `image` 并不会改 `profile`——所以你为 Rust 准备的镜像，继承的仍然是 `web-go` 的遮盖项。又因为列表在各层之间取并集，`mask` **只能追加、删不掉**：设了 `image` 再写 `mask = ["target"]`，最后拿到的是 `node_modules`、`.next` **和** `target` 三条，多出来的那两条还会在 worktree 里凭空建出空的挂载点。正确的写法是把 `profile` 一并设成最接近的那个内置值，再往上加：

  ```toml
  image   = "ghcr.io/me/rustbox:1"
  profile = "py-rust"           # 遮盖 .venv 和 target；不写的话继承的是 web-go 的
  ```

`image` 在项目层是允许写的，所以团队可以把镜像选择提交进 `<repo>/.sbx/sandbox.toml`；又因为 `.sbx/` 在信任确认的覆盖范围内，`git pull` 把镜像换掉的话，`sbx run` 会先停下来给你看 diff。

## 团队怎么用

项目层配置跟着仓库走，所以新人接入就三步：

1. 把 `<repo>/.sbx/sandbox.toml` 提交进仓库（Profile、网络模式、额外白名单、依赖遮盖），项目要自己的镜像就再加一个 `<repo>/.sbx/Dockerfile`。
2. 队友 clone 下来执行 `sbx trust`，看一眼 sbx 摊开给他的内容。
3. `sbx run`。

**这个文件里写不了的东西**（三条红线，是强制的不是建议）：任何像凭据的字段、任何绝对路径、任何会在容器里执行的命令（`on_idle` 这类）。一个仓库不该决定你的凭据从哪来，也不该决定你的沙箱里跑什么。

## 出问题了看这里

| 现象 | 怎么回事 |
|---|---|
| `sbx ls` 里是 `exited(oom)` | 容器撞到内存上限了。调大 `resources.memory`，或者调小 `max_running`——`sbx doctor` 会告诉你这两个数乘起来在不在 Docker VM 的预算里 |
| Agent 说某个下载失败了 | allowlist 模式下被代理拦了。`sbx net denied` 看拦了什么，`sbx net allow <host>` 放行并对运行中的 Task 热加载 |
| `sbx run` 停下来让你登录 | `sbx-home` 里的凭据过期了。`sbx login` 一次，所有 Task 共享 |
| `sbx run` 卡在 `.sbx/` 上 | 项目配置被改过。它会把改动打出来——看完执行 `sbx trust` |
| 说不清哪里不对 | `sbx doctor`。docker、代理、登录、信任、通知、Agent 首次启动状态都查一遍，每个问题给一条可以直接复制的修复命令 |

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

**M1、M2、M3（Codex 除外，M3-6）和 M4 的大部分都已完成。** 可用命令：`run / attach / shell / stop / ls / path / done / drop / logs / port / net / memory / login / config / trust / prep / doctor / upgrade`。

已知边界：没有 `sbx merge`；Codex 还没接（M3-6）；上游代理只支持 http（只有 SOCKS 的话要自己加一层转发）；底层镜像必须是 Debian/Ubuntu 系；伴生服务和 forward 都按名字访问，不是 `localhost`。另见[明确不提供](#明确不提供)。

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
