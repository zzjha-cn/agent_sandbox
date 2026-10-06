# sbx 设计文档：AI 编码 Agent 沙箱运行时

| 项 | 内容 |
|---|---|
| 状态 | 设计稿 v1.1（已根据 M0 验证结果修订；尚未实现） |
| 日期 | 2026-10-04 |
| 依据 | [CONTEXT.md](CONTEXT.md)（术语表）、ADR 0001–0016（`private/adr/`，未纳入版本控制）、[M0 验证汇总](../spikes/M0-SUMMARY.md) |

---

## 1. 背景与目标

Claude Code 和 Codex 已经承担了大量编码工作。要让它们**放开权限、无人值守地长时间运行**，需要一个安全边界：Agent 可以在里面随意执行命令，却碰不到宿主机上代码以外的东西，也不能随意访问网络。

`sbx` 把本地代码挂载进容器，让容器成为 Agent 的运行时环境。

### 1.1 目标

- **G1 无人值守**：Agent 以免确认模式运行，可以持续数小时。断开终端或崩溃后能接回或续跑。
- **G2 并行**：同一个仓库上可以同时跑多个 Task，互不干扰。
- **G3 兼容交互**：可以随时 attach 进去结对、插话纠偏。
- **G4 可控边界**：文件系统上只暴露代码；网络默认按白名单放行，所有访问都可以审计。
- **G5 团队可用**：单二进制分发；项目配置提交进仓库，团队成员 clone 下来就能用。
- **G6 与宿主机无关**：沙箱只是容器，macOS 和 Linux 上行为一致。

### 1.2 非目标

- 远程、云端或多租户共享的运行环境。
- 防御内核级的容器逃逸（威胁模型见 §8）。
- 封装 git 合并流程，或者替代 code review。
- 宿主机层面的能力，比如防休眠、系统通知、常驻守护进程。
- 兼容 devcontainer.json（以后可以作为适配层加上）。

### 1.3 主要场景

| 场景 | 描述 | 典型命令 |
|---|---|---|
| S1 无人值守（主场景） | 给 Task 派一件事，或者在会话里跑 `/auto-it` 循环，然后走开，第二天回来 review 分支 | `sbx run feat-x` → detach → `sbx ls` |
| S2 批量派发 | 脚本化地发起多个 headless Task | `sbx run fix-y -p "..."` |
| S3 交互结对 | 直接在主工作目录上和 Agent 协作 | `sbx run`（默认 Task `main`） |

---

## 2. 总体架构

```
宿主机（macOS / Linux）
│
├── sbx（Go 单二进制，无常驻进程）
│     ├─ 读取配置（4 层）与信任哈希 ~/.sbx/trust/
│     ├─ 管理 worktree   ~/.sbx/worktrees/<ws>/<task>/
│     ├─ 管理状态和日志  ~/.sbx/state/<ws>/<task>/
│     └─ 通过 docker CLI 编排 ──────────────────────────┐
│                                                        ▼
└── 容器运行时（Docker Desktop / OrbStack / Colima / Podman 的 Linux VM）
      │
      ├── Task: <ws>/feat-x  [proxy = shared，默认]
      │    ┌───────────────────────┐  net: sbx-<ws>-feat-x (internal)
      │    │ agent 容器             │───────────────┐
      │    │  tmux ─ claude/codex  │ HTTP(S)_PROXY= │
      │    │  非 root，2C/4G       │ http://<task>:<token>@sbx-proxy:3128
      │    │  /<abs>/worktree (rw) │               ▼
      │    │  /<abs>/repo/.git (rw)│        ┌──────────────────┐  net: sbx-egress
      │    │  依赖目录 ← volume     │        │ sbx-proxy（共享）  │──────────────► 互联网
      │    └───────────────────────┘   ┌──►│ proxy_auth 识别 Task│──► 上游代理（可选）
      │                                │   │ 按 Task 套用白名单  │   host.docker.internal
      ├── Task: <ws>/fix-y  [shared] ──┘   └──────────────────┘
      │    （有自己的 internal 网络；共享 proxy 也接入这个网络）
      │
      ├── Task: <ws>/spike  [proxy = dedicated]
      │    agent 容器 ──► sbx-<ws>-spike-proxy（独占 sidecar，无需认证）──► 互联网
      │
      └── 全局共享 volume：sbx-home（登录态） sbx-cache（包缓存） sbx-mise（运行时）
```

**关键点**
- Agent 进程本身跑在容器里，而不仅仅是它执行的命令（ADR 0001）。
- 每个 Task 有一个 agent 容器和一个 internal 网络。agent 容器没有直接出口，只能经过 squid（ADR 0005）。
- squid 有两种部署（ADR 0014）：**默认全局共享一个 `sbx-proxy`**，通过代理认证区分 Task；配置了 `proxy = "dedicated"` 的 Task 使用独占的 sidecar。两种部署下，Task 之间的网络都互不可达。
- `sbx` 是无状态的命令行工具，所有状态都在文件系统和 docker 对象里（容器、volume、network 上的 label）。

---

## 3. 领域模型

```
Workspace (git 仓库) 1 ──── * Task 1 ──── 1 Sandbox
                                │             ├─ agent 容器
                                │             ├─ proxy 绑定：共享 sbx-proxy 上的凭据和白名单片段，
                                │             │             或独占的 squid sidecar
                                │             ├─ internal 网络
                                │             └─ 依赖 volume ×N
                                ├─ worktree + 分支（main Task 除外）
                                └─ state 目录（状态、日志、proxy 日志）
```

| 实体 | 标识 | 说明 |
|---|---|---|
| Workspace | `ws = <basename>-<sha1(abs_path)[:6]>` | 用仓库的绝对路径确定，避免不同目录下同名仓库冲突 |
| Task | `<ws>/<task>` | task 名只允许 `[a-z0-9-]`；`main` 是保留名 |
| 分支 | `sbx/<task>` | 从当前 HEAD（或 `--base` 指定的分支）创建 |
| 容器 | `sbx-<ws>-<task>`；共享 proxy `sbx-proxy`；独占 proxy `sbx-<ws>-<task>-proxy` | label：`sbx.ws`、`sbx.task`、`sbx.role=agent\|proxy`、`sbx.proxy=shared\|dedicated` |
| 网络 | `sbx-<ws>-<task>-net`（internal）、`sbx-egress`（全局共享） | |
| 代理凭据 | `task-id = <ws>.<task>`，`token` 为 32 字节随机数 | 只在共享模式下使用；保存在 `state/.../proxy.cred`，只注入到本 Task 的容器 |
| volume | `sbx-<ws>-<task>-dep-<i>`；全局 `sbx-home`、`sbx-cache`、`sbx-mise` | |

### 3.1 Task 状态机

```
                 sbx run
   (不存在) ──────────────► running ◄──────┐
                              │  ▲          │ 用户输入 / 新 prompt
               Agent Stop hook│  │          │
                              ▼  │          │
                             idle ──────────┘
                              │
          sbx stop / 宿主机重启 │                 headless 结束 / OOM
                              ▼                       │
                           stopped ──sbx run──► running   ▼
                                                     exited(code|oom)
   任意状态 ──sbx done──► (销毁，保留分支)
   任意状态 ──sbx drop──► (销毁，删除分支，需要确认)
```

状态由两部分推导：docker 容器状态（运行中、已退出、OOMKilled）和 state 目录里的 `status.json`（由 Agent hooks 写入 `running` 或 `idle`）。

---

## 4. 文件系统与挂载

### 4.1 宿主机目录

```
~/.sbx/
  config.toml                     # 个人全局配置
  workspaces/<repo>.toml          # 个人对某个项目的覆盖
  trust/<ws>.json                 # 信任哈希（不挂载进任何容器）
  worktrees/<ws>/<task>/          # Task 的 worktree
  state/<ws>/<task>/
    status.json                   # Agent hooks 写入
    run.log                       # headless 输出
    proxy/access.log              # 独占模式下的 squid 访问日志
    proxy.cred                    # 共享模式下的代理凭据
    meta.json                     # 创建参数：base、profile、网络模式、proxy 部署、端口映射
  proxy/                          # 共享 sbx-proxy 的数据
    squid.conf                    # 主配置（由 sbx 渲染）
    passwd                        # 各 Task 凭据的 htpasswd 文件
    tasks/<task-id>.conf          # 每个 Task 的 ACL 片段
    allow/<task-id>.txt           # 每个 Task 的白名单
    access.log                    # 共享访问日志（带用户名，按 Task 拆分）
```

### 4.2 agent 容器的挂载表

| 来源 | 容器内路径 | 模式 | 说明 |
|---|---|---|---|
| Task worktree（`main` Task 用主工作目录） | **和宿主机相同的绝对路径** | rw | 报错路径能直接点开 |
| `<repo>/.git` | 和宿主机相同的绝对路径 | rw | worktree 提交需要；已接受风险，见 ADR 0003 |
| `sbx-<ws>-<task>-dep-<i>` | worktree 内的依赖目录 | rw | 遮盖 `node_modules`、`.venv`、`target` 等（ADR 0008） |
| `sbx-home` | `/home/agent` 下的 `.claude/`、`.codex/` 等状态目录 | rw | 登录态、会话历史（ADR 0006） |
| `~/.claude/{skills,agents,commands}`（存在的目录） | `/sbx/host-claude/<name>`，再由 sbx 在 `~/.claude/` 下建软链接指过去 | **ro** | 宿主机配置只读。目录里指向外部的软链接在容器里不可用，`sbx run` 时警告（M1 实现） |
| `~/.claude/CLAUDE.md` | 每次 `sbx run` **快照复制**到 `gen/host-claude/CLAUDE.md`，`~/.claude/CLAUDE.md` 软链接到 `/sbx/gen/host-claude/CLAUDE.md` | ro | 单文件不能直接 bind mount（M0-3），所以复制；宿主机修改后下次 `sbx run` 生效（M1 实现） |
| `~/.claude/projects/<key>/memory/`（项目自动记忆） | 不挂载。每次拉起 claude 前**导入**到 `sbx-home` 的同名目录；`sbx memory pull` 展示差异后导回宿主机 | — | 三方比较，冲突不覆盖，`MEMORY.md` 按行合并（ADR 0016） |
| `~/.codex/{AGENTS.md,...}` | `/home/agent/.codex/...` | **ro** | 同上 |
| 生成目录 `~/.sbx/state/<ws>/<task>/gen/`（hooks 脚本、`settings.sbx.json`） | `/sbx/gen` | ro | **挂载目录，不挂载单个文件**（M0-3：单文件 bind mount 会读到截断的内容）。hooks 通过 `claude --settings /sbx/gen/settings.sbx.json` 注入（M0-5） |
| `sbx-cache` | `/sbx/cache/{npm,pip,uv,go-mod,go-build,cargo}` | rw | 全局包缓存。**用环境变量显式指向这里**（`npm_config_cache`、`PIP_CACHE_DIR`、`UV_CACHE_DIR`、`GOMODCACHE`、`GOCACHE`、`CARGO_HOME`），不依赖各镜像的默认路径（M0-2：golang 镜像的默认路径是 `/go/pkg/mod`） |
| `sbx-mise` | `/home/agent/.local/share/mise` | rw | 全局共享的语言运行时 |
| `~/.sbx/state/<ws>/<task>/` | `/sbx/state` | rw | 状态和日志；不作为安全依据 |

**挂载与写入的通用规则（M0-3）**：sbx 生成、需要更新的文件一律放在目录里挂载；更新时先写临时文件再 `rename`。

**明确不挂载**：`~/.ssh`、`~/.aws`、`~/.config/gh`、`~/.gitconfig`，以及 home 目录下的其他任何内容。

**git 身份**：从宿主机的 git config 读出 `user.name` 和 `user.email`，以 `GIT_AUTHOR_*` / `GIT_COMMITTER_*` 环境变量注入，不挂载 `.gitconfig`。容器里没有推送远端的凭据，提交只留在本地。

**依赖遮盖与 volume 的嵌套顺序**：docker 会把后声明的挂载叠加在前面的挂载之上，所以先挂 worktree，再挂依赖 volume。

---

## 5. 镜像：Profile 加 Agent 层（ADR 0007）

### 5.1 分层

```
┌──────────────────────────────────────┐
│ Agent 层（由 sbx 生成，embed 进二进制） │  claude、codex、tmux、git、ripgrep、tini
│                                      │  agent 用户（UID/GID 和宿主机一致）、entrypoint、hooks 脚本
├──────────────────────────────────────┤
│ Profile（语言环境）                    │  web-go / py-rust / 自定义 Dockerfile / 镜像名
├──────────────────────────────────────┤
│ Debian / Ubuntu 基础镜像               │
└──────────────────────────────────────┘
          ▼ 构建结果
   sbx/<profile>:<hash>
   hash = sha256(profile 内容, Agent 层模板版本, Agent CLI 版本锁定, UID/GID)
```

### 5.2 内置 Profile

| Profile | 内容 |
|---|---|
| `web-go`（A，默认） | Go（最新稳定版）、Node LTS、corepack 和 pnpm、Next.js 开发需要的系统库、基础 Python 3 |
| `py-rust`（B） | uv（负责 Python 版本和依赖）、rustup 加 stable 工具链、cargo 常用组件（clippy、rustfmt） |

- 项目里有 `.tool-versions`、`mise.toml`、`.nvmrc`、`.python-version`、`rust-toolchain.toml` 时，按文件内容自动安装对应版本，安装结果放在全局共享的 `sbx-mise` 或 rustup 目录里。
- 自定义 Profile：在 `<repo>/.sbx/Dockerfile` 里写，或者在配置里写 `profile.image = "<镜像名>"`。前提是 Debian 或 Ubuntu 系。

### 5.3 Agent 层要点

- `ENTRYPOINT ["tini", "--", "/sbx/entrypoint.sh"]`。容器启动后常驻；Agent 由 `sbx` 通过 `docker exec` 在 tmux 会话 `agent` 里拉起。带参数时 entrypoint 直接 `exec "$@"`（用于 `claude auth login` 这类一次性命令，M1 实现时修正）。
- Profile 里装到 `/usr/local/<x>/bin` 的工具要软链接到 `/usr/local/bin`：tmux 和 `bash -l` 是 login shell，`/etc/profile` 会重置 PATH（M1 实现时发现 go 找不到）。
- Agent CLI 默认装最新版本。`sbx upgrade` 只重建 Agent 层，Profile 层的缓存保留；配置里可以锁定版本，比如 `agents.claude.version = "x.y.z"`。
- UID/GID 构建时和宿主机用户对齐，避免 Linux 宿主机上 bind mount 出现属主问题。
- **必须以非 root 用户运行**（M0-2：root 下有些依赖权限的测试结果会和宿主机不一致；M0-5：以非 root 运行一切正常）。`sbx-home` 和 `sbx-cache` 首次创建时，按 Agent 用户的 UID/GID 初始化属主。
- 默认环境变量：`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`（减少遥测外联，M0-4），以及上面那组包缓存路径变量。
- 构建时通过 `--build-arg HTTP(S)_PROXY` 走上游代理，**允许重试**，必要时可以配置镜像源（M0-4：rustup 出现过一次偶发的 TLS EOF）。

---

## 6. 网络（ADR 0005、0014）

### 6.1 拓扑

- `sbx-<ws>-<task>-net`：用 `docker network create --internal` 创建，**没有外部路由**。agent 容器只接入这个网络。
- squid 容器同时接入 Task 的 internal 网络和全局的 `sbx-egress` 网络（普通 bridge）。
- 不经过代理的流量（比如裸 TCP、直连 IP）因为没有路由，会直接失败。
- `NO_PROXY=localhost,127.0.0.1`。

### 6.2 两种部署

| | shared（默认） | dedicated |
|---|---|---|
| squid 实例 | 全局一个 `sbx-proxy` | 每个 Task 一个 `sbx-<ws>-<task>-proxy` |
| 接入 Task 网络 | `docker network connect` 接入每个 Task 的 internal 网络（网络别名 `proxy`） | 创建时直接接入 |
| 识别 Task | `proxy_auth`：用户名就是 task-id，密码是随机 token | 不需要，整个实例只服务一个 Task |
| 代理地址 | `http://<task-id>:<token>@proxy:3128` | `http://proxy:3128` |
| 白名单、模式变更 | 重写 `tasks/<task-id>.conf` 和 `allow/<task-id>.txt`，然后 `squid -k reconfigure` | 重写本实例的配置，然后 reconfigure |
| 日志 | 共享 access.log（带用户名），按 Task 拆分 | 独立的 access.log |
| 生命周期 | 第一个 shared Task 启动时创建；最后一个 shared Task 停止后由 `sbx` 顺手停掉 | 跟随 Task |
| 适用场景 | 日常默认使用，资源占用最小 | 需要不同的上游代理、排查故障、或希望故障互不影响 |

选择方式：`network.proxy = "shared" | "dedicated"`，可以写在任意配置层，`sbx run --proxy dedicated` 按 Task 覆盖。

**安全性**：
- 每个 Task 的容器只拿得到自己的 token，没法冒用其他 Task 的白名单。
- Task 的 internal 网络之间互不相通，只有共享 proxy 同时接在这些网络上，而 proxy 不转发 Task 之间的流量（只有 `http_port` 一个入口）。

### 6.3 白名单

最终白名单 = 内置层 ∪ 语言栈预设层 ∪ 项目层 ∪ 个人覆盖 ∪ 通知域名，再减去策略拦截层。生成白名单时要**去重**：`.github.com` 已经包含 `github.com`，两者并存时 Squid 会报警告。

| 层 | 内容示例 |
|---|---|
| 内置层 | `.anthropic.com`、`.claude.ai`、**`.claude.com`**（交互模式启动时会探测 `platform.claude.com`，不通就直接退出，M0-5）、`.openai.com`、`.chatgpt.com`、`github.com`（codex 会访问） |
| **策略拦截层**（优先于所有放行） | `mcp-proxy.anthropic.com`（claude.ai 云端 MCP 连接器，**默认拦截**，ADR 0015）。项目或 Task 可以用 `network.cloud_mcp = true` 解除 |
| 语言栈预设（按 Profile 启用） | `registry.npmjs.org`、`.yarnpkg.com`、`pypi.org`、`files.pythonhosted.org`、`proxy.golang.org`、`sum.golang.org`、`crates.io`、`static.crates.io`、`static.rust-lang.org`、`github.com`、`.githubusercontent.com`、常用国内镜像 |
| 项目层 | `sandbox.toml` 里的 `network.allow` |
| 通知域名 | 从 `on_idle` / `on_exit` 命令里解析出的 webhook 主机 |

### 6.4 Squid 配置骨架

**dedicated 模式**（单 Task）：

```squid
http_port 3128
acl allowlist dstdomain "/etc/squid/allowlist.txt"
acl SSL_ports port 443
acl CONNECT method CONNECT

# allowlist 模式（open 模式下把下面两行换成 http_access allow all）
http_access allow CONNECT SSL_ports allowlist
http_access allow allowlist
http_access deny all

# 可选：串联宿主机代理
cache_peer host.docker.internal parent 7890 0 no-query default
never_direct allow all          # 有 upstream 时强制走上游；可以按域名 ACL 细分直连

access_log stdio:/var/log/squid/access.log squid
cache deny all                  # 只做转发，不缓存
```

**shared 模式**（主配置加每个 Task 的片段）：

```squid
# squid.conf（主配置）
http_port 3128
auth_param basic program /usr/lib/squid/basic_ncsa_auth /etc/squid/passwd
auth_param basic realm sbx
acl SSL_ports port 443
acl CONNECT method CONNECT
include /etc/squid/tasks/*.conf
http_access deny all

cache_peer host.docker.internal parent 7890 0 no-query default   # 可选
never_direct allow all
logformat sbx %ts.%03tu %un %>a %Ss/%>Hs %rm %ru
access_log stdio:/var/log/squid/access.log sbx
cache deny all
```

```squid
# tasks/<task-id>.conf（allowlist 模式）
acl u_<id> proxy_auth <task-id>
acl a_<id> dstdomain "/etc/squid/allow/<task-id>.txt"
http_access allow u_<id> CONNECT SSL_ports a_<id>
http_access allow u_<id> a_<id>

# tasks/<task-id>.conf（open 模式）
acl u_<id> proxy_auth <task-id>
http_access allow u_<id>
```

- 共享模式下只有一个上游配置，来自个人全局配置。需要不同上游的 Task 应该用 dedicated 模式。
- squid 容器的启动命令先删 `/run/squid.pid` 再 `exec squid -NYC`，并在配置里设 `shutdown_lifetime 1 seconds`：否则 `docker stop` 超时被 SIGKILL 后残留 PID 文件，下次 `docker start` 时 squid 报 "already running" 退出（M1 实现时发现）。
- `include /etc/sbx/tasks/*.conf` 至少要匹配一个文件，sbx 会放一个占位的 `00-empty.conf`。

- 白名单改动后执行 `docker exec <proxy> squid -k reconfigure` 热加载，不需要重启 Task。
- Linux 宿主机上，squid 容器需要加 `--add-host=host.docker.internal:host-gateway`。
- ✅ M0-3 已验证：Clash 一类的混合端口（`7890`）可以直接当 HTTP 上游，日志里能看到 `FIRSTUP_PARENT`。如果宿主机代理只提供 SOCKS，仍然需要一个转发层（暂不实现）。
- tasks 片段里的策略拦截要写在放行规则之前：`http_access deny u_<id> cloud_mcp`，然后才是 `http_access allow u_<id> a_<id>`（M0-5 已验证）。

### 6.5 被拒请求

- 从 access.log 里按域名聚合（共享模式下再按 `%un` 用户名过滤出当前 Task），**分三类统计**（M0-4、M0-5）：
  - **不在白名单**：`TCP_DENIED/403`，且域名不属于策略拦截层或已知遥测，这是**默认展示的唯一一类**。
  - **策略拦截 / 已知遥测**：云端 MCP（被拦后会持续重试，实测一轮 94 次）、`*.datadoghq.com` 等。默认隐藏，`--all` 时才显示。
  - **认证失败**：`TCP_DENIED/407`。有的客户端会先不带凭据试一次再重发，正常情况下会有少量；数量很大才说明配置有问题。
- `sbx net denied [task]` 列出被拒的域名、次数和最近时间。
- `sbx net allow <host> [--project|--personal]` 把域名写进对应层的配置；如果写的是项目层，会触发信任确认（§9），然后对运行中的 proxy 执行 reconfigure。

---

## 7. 认证与 Agent 配置（ADR 0006）

### 7.1 登录态

| 方式 | 流程 |
|---|---|
| 订阅登录（默认） | `sbx login claude\|codex`：启动一个临时 agent 容器（挂载 `sbx-home`），执行对应 CLI 的登录流程，凭据落到 `sbx-home`，所有 Task 共享 |
| API key | `~/.sbx/config.toml` 里写 `agents.claude.api_key_env = "ANTHROPIC_API_KEY"`（从宿主机环境变量读取）或 `api_key_file = "..."`，启动时以环境变量注入，优先级高于订阅登录 |

**M0-1 结论**：
- Claude：`claude auth login` 在容器里交互完成（显示 URL，粘贴授权码），**不需要回调端口**；全新容器能复用 volume 里的凭据。备选是 `claude setup-token`，生成长期 token 后作为环境变量注入。
- Codex：使用 `codex login --device-auth`（设备码）。推迟到 M3-6 再验证。

### 7.2 生成的 Agent 配置

- **Claude 的状态 hooks**（通过 `--settings /sbx/gen/settings.sbx.json` 注入，M0-5 已验证）：
  | 事件 | 写入 | 说明 |
  |---|---|---|
  | `SessionStart` | `idle` | 会话就绪 |
  | `UserPromptSubmit` | `running` | 开始一个回合 |
  | `PreToolUse` | `running` | 刷新最后活动时间 |
  | `Stop` | `idle` | 回合结束，等待输入 |
  | `Notification` | `idle`，并执行 `on_idle` | 空闲约 60 秒后触发，表示"需要人处理"。**`on_idle` 挂在这个事件上，不挂在 Stop 上**，避免短暂停顿也发通知 |
  - `status.json` 先写临时文件再 rename；同时追加 `events.log` 方便排查。
  - 不复制宿主机的 hooks。
- **Claude 首次启动状态的预置**（M0-5：否则交互模式的 TUI 会卡在引导、登录方式选择和 bypass 警告这些对话框上）。每次 Task 启动前幂等写入：
  | 文件（位于 sbx-home） | 字段 |
  |---|---|
  | `~/.claude.json` | `hasCompletedOnboarding: true`；`projects["<worktree 绝对路径>"].hasTrustDialogAccepted: true` |
  | `~/.claude/settings.json` | `theme: "dark"`；`skipDangerousModePermissionPrompt: true` |
  - 写入方式：jq 加 tmp + rename（这个文件 Agent 也会写）。
  - 这些都是 Claude Code 的内部字段，**升级后可能会变**。`sbx upgrade` 和 `sbx doctor` 要做冒烟测试：在 tmux 里启动后检查画面上是否出现对话框（R9）。
- **Codex**（`config.toml`）：配置 `notify` 命令写状态。
- **启动参数**：claude 用 `--dangerously-skip-permissions`，codex 用 `--dangerously-bypass-approvals-and-sandbox`。

---

## 8. 安全模型

### 8.1 威胁模型

**防护对象**：
- Agent 误操作（删文件、乱装东西、改系统配置）。
- 被 prompt 注入的 Agent 外传代码或密钥。
- 供应链里的恶意脚本（postinstall 等）读取宿主机文件或回连外部。

**不防护**：内核或容器运行时 0day、宿主机本身已被攻破、你自己确认放行的域名。

### 8.2 威胁、缓解与残余风险

| 威胁 | 缓解 | 残余风险 |
|---|---|---|
| 读写宿主机的其他文件 | 只挂载 worktree 和 `.git`；凭据目录不挂载 | worktree 里的 `.env` 可见，建议只放开发用的假凭据 |
| 篡改宿主机的 Agent 配置，植入 hooks | 宿主机配置只读挂入；settings 由 `sbx` 生成 | 无 |
| 通过 `.git/hooks`、`.git/config` 在宿主机执行代码 | —（ADR 0003 的已接受风险） | **存在**。后续可以评估：启动前做快照比对，或在宿主机侧设置 `core.hooksPath` |
| 篡改主仓库的 refs 和默认分支 | — | **存在**。依赖你在合并前 review |
| 篡改 `.sbx/` 来扩大自己的权限 | 信任哈希，变更后拒绝启动（ADR 0010） | 当前运行中的 Task 不受影响，直到它重启 |
| 数据外传 | internal 网络，只能经过 squid 白名单 | 白名单内的域名（比如 GitHub gist）仍然可以被滥用 |
| 推送到远端 | 容器里没有 git 凭据 | 无 |
| 资源耗尽，拖垮其他 Task | 每个 Task 的 CPU、内存、pids 上限；全局并发上限 | 无 |
| 绕过 proxy 的 iptables 规则 | 不依赖容器内的 iptables，用网络拓扑隔离 | 无 |

---

## 9. 配置（ADR 0009、0010）

### 9.1 合并顺序

内置默认 → `~/.sbx/config.toml` → `<repo>/.sbx/sandbox.toml` → `~/.sbx/workspaces/<repo>.toml`。

列表取并集，标量值后者覆盖前者。项目层出现密钥字段（`*_key`、`*token*`、`*secret*`）或绝对路径时，直接报错。

### 9.2 示例

`~/.sbx/config.toml`（个人全局）：

```toml
default_agent = "claude"
max_running   = 3
on_idle = "curl -s -X POST https://open.feishu.cn/open-apis/bot/v2/hook/xxx -d '{\"msg_type\":\"text\",\"content\":{\"text\":\"$SBX_TASK idle\"}}'"

[network]
upstream = "http://host.docker.internal:7890"   # 宿主机代理；留空表示直连
proxy    = "shared"                               # shared（默认）| dedicated

[resources]
cpus = 2
memory = "3g"
pids = 1024

[agents.claude]
# api_key_env = "ANTHROPIC_API_KEY"   # 配置后优先于订阅登录
# version = "latest"
```

`<repo>/.sbx/sandbox.toml`（项目级，提交进仓库）：

```toml
profile = "web-go"            # 或 "py-rust"；有 .sbx/Dockerfile 时以它为准

[network]
mode  = "allowlist"           # allowlist | open
# proxy = "dedicated"         # 这个项目需要独占 proxy 时打开
# cloud_mcp = true            # 允许 claude.ai 云端 MCP 连接器（默认拦截，ADR 0015）
allow = ["api.example-internal.com"]

[deps]
mask = ["node_modules", "web/node_modules", ".next"]

[ports]
expose = [3000]               # 只是声明，实际映射由 sbx port 按需进行

[resources]
memory = "4g"                 # 覆盖个人全局默认值（默认 3g）
```

### 9.3 信任确认

- 信任哈希 = sha256（`<repo>/.sbx/` 下所有文件按路径排序后，每个文件的路径和内容）。
- `sbx run` 时如果哈希和 `~/.sbx/trust/<ws>.json` 里的不一致：打印 diff（和上次信任时保存的快照比较），退出码非 0。
- `sbx trust`：展示当前内容或 diff，确认后写入哈希和快照。

---

## 10. CLI 规格

| 命令 | 行为 |
|---|---|
| `sbx run [task] [--agent claude\|codex] [--base <ref>] [--net open\|allowlist] [--proxy shared\|dedicated] [-p "<prompt>"]` | 创建或恢复 Task，启动 Agent。省略 task 时用 `main`。有 `-p` 时走 headless 模式，否则进入 tmux 并自动 attach（`--detach` 只启动不 attach） |
| `sbx path [task]` | 打印 Task 的工作目录（`main` 为仓库根），用于 `cd $(sbx path <task>)` 或在编辑器里打开（M1 实现时补充） |
| `sbx attach <task>` | `docker exec -it ... tmux attach -t agent` |
| `sbx ls [--all]` | 列出当前 Workspace 的 Task（`--all` 列出所有 Workspace）：状态、分支、ahead 数和 diff 统计、最后活动时间、工作目录（PATH）、被拒请求数 |
| `sbx stop <task>` | 停止 agent 容器（以及独占 proxy），保留一切；如果这是最后一个 shared Task，顺带停掉 `sbx-proxy` |
| `sbx shell <task>` | 在 Task 容器里打开一个 bash |
| `sbx logs <task>` | 查看 `run.log` |
| `sbx port <task> <port>` | 映射到宿主机 `127.0.0.1` 上的随机空闲端口并打印地址（实现方式：在 egress 网络上起一个 socat 转发容器，或重建 agent 容器） |
| `sbx net denied [task]` / `sbx net allow <host> [--project\|--personal]` | 见 §6.4 |
| `sbx login claude\|codex` | 见 §7.1 |
| `sbx trust` | 见 §9.3 |
| `sbx upgrade` | 重建 Agent 层；已有的 Task 下次启动时生效 |
| `sbx memory pull [--yes]` | 把沙箱里新记的项目记忆导回宿主机：先展示差异，确认后写入（ADR 0016） |
| `sbx done <task>` | 删除容器、Task 网络、依赖 volume、worktree、state，以及共享 proxy 上的凭据和片段；保留 `sbx/<task>` 分支 |
| `sbx drop <task>` | 同上，并删除分支；需要输入 task 名确认 |
| `sbx doctor` | 检查 docker 可用性、上游代理连通性、登录态、信任状态 |

### 10.1 `sbx run` 主流程

```
1. 解析 Workspace：git rev-parse --show-toplevel → ws id
2. 加载 4 层配置并合并、校验
3. 信任检查：.sbx/ 哈希 ≠ trust 记录 → 打印 diff，退出
4. Task 已存在？
     ├─ running/idle → 直接 attach（或在 -p 模式下拒绝）
     └─ stopped      → 跳到第 9 步
5. 并发检查：running+idle 的 Task 数 ≥ max_running → 报错退出
6. 准备 worktree：git worktree add ~/.sbx/worktrees/<ws>/<task> -b sbx/<task> <base>
   （main Task 跳过这一步，直接使用主工作目录）
7. 确保派生镜像存在：计算 hash，不存在则构建 Profile 再叠加 Agent 层
8. 创建 Task 网络、依赖 volume；按 proxy 部署方式：
     ├─ shared    → 确保 sbx-proxy 在运行；生成凭据，写入 passwd 和 tasks/allow 片段；
     │              network connect 到 Task 网络；squid -k reconfigure
     └─ dedicated → 创建 squid sidecar（写入白名单和上游配置）
   然后创建 agent 容器（注入对应的 HTTP(S)_PROXY）
9. 启动容器；docker exec 在 tmux 会话里拉起 Agent（headless 模式下直接执行并写 run.log）
10. 写 state/meta.json，然后 attach（或返回）
```

---

## 11. 资源与端口（ADR 0012）

- 每个 Task 的默认值：`--cpus 2 --memory 3g --pids-limit 1024`，`max_running = 3`；squid 容器固定为 `--memory 128m`（shared 模式下全局只有一个）。
- **推荐的 Docker VM 配置：10GB 内存**（宿主机 16GB）。3 × 3g + squid ≈ 9.1GB，在 VM 里留有余量，macOS 也还剩约 6GB（R10 已决定，2026-10-04）。
- `--memory` 是上限而不是预留。`sbx run` 和 `sbx doctor` 在 `max_running × memory > VM 内存 × 0.85` 时给出警告。
- `max_running` 统计处于 running 或 idle 状态的 Task（以 agent 容器是否在运行为准）。超过上限时报错，不排队。
- 用 `docker inspect` 读取 `State.OOMKilled`，在 `sbx ls` 里显示为 `exited(oom)`。
- 端口默认不暴露；`sbx port` 只绑定到宿主机的 `127.0.0.1`。

---

## 12. 实现结构（ADR 0013）

```
core/                  # Go module sandx（M1 实际位置）
  cmd/sbx/main.go
  internal/
    config/      # 4 层加载、合并、校验
    trust/       # 哈希、快照、diff
    workspace/   # ws id、worktree 管理
    task/        # Task 生命周期、状态推导、meta
    image/       # Profile 解析、派生镜像 hash、构建
    docker/      # docker CLI 封装（exec.Command，解析 --format json 输出）
    proxy/       # shared/dedicated 两种部署、squid 配置和片段渲染、凭据、reconfigure、access.log 解析
    agent/       # settings/config 生成、启动参数、登录流程
  assets/        # embed：Agent 层 Dockerfile、entrypoint、hooks 脚本、
                 #        profiles/web-go、profiles/py-rust、squid.conf.tmpl、allowlist 预设
docs/
  design.md
  CONTEXT.md
  architecture.md      # 实现现状
  walkthrough.md       # 按场景走一遍
  changelog/
  private/             # 不纳入版本控制
    adr/
    m1-acceptance.md
```

- 只依赖 docker CLI，不引入 Docker SDK，以兼容 Podman 等实现。
- 外部依赖尽量少：TOML 解析库和 CLI 框架（cobra 或标准库 flag）。

---

## 13. 里程碑

| 阶段 | 范围 | 完成标准 |
|---|---|---|
| **M0 验证** ✅ | ① 容器内的 OAuth 登录 ② macOS bind mount 的 IO 性能 ③ Squid 串联宿主机代理 ④ 常见工具对带认证的代理 URL 的兼容性 ⑤ 状态 hooks | 已完成（Codex 部分推迟），见 [M0-SUMMARY](../spikes/M0-SUMMARY.md) |
| **M1 MVP** | `run`/`attach`/`ls`/`stop`/`done`；Profile `web-go`；Agent 层叠加；共享 squid（allowlist 模式，proxy_auth 识别 Task）；sbx-home；依赖遮盖 | 能在一个真实仓库上开 2 个并行 Task，让 claude 无人值守完成一次改动，并在主仓库看到对应分支 |
| **M2 安全与配置** | 4 层配置；信任确认；`net denied/allow`；open 模式；上游代理；`login`；API key | 篡改 `.sbx/` 会被拦住；被拒的域名能一键放行；dedicated proxy 模式可用 |
| **M3 完整体验** | `py-rust`；自定义 Dockerfile；headless `-p`；状态 hooks 和 `on_idle`/`on_exit`；`port`；资源上限和 OOM 识别；`upgrade`；`drop`；`doctor`；Codex 支持 | 整夜跑 `/auto-it`，第二天通过通知和 `sbx ls` 了解全部结果 |

---

## 14. 风险与开放问题

| # | 问题 | 影响 | 处理 |
|---|---|---|---|
| R1 | `.git` 可写带来的宿主机执行风险（hooks、config） | 沙箱逃逸 | 已接受；M3 之后评估快照比对或 `core.hooksPath` |
| R2 | OAuth 回调在容器里不可用 | 订阅登录受阻 | ✅ Claude 已解除（M0-1）；Codex 在 M3-6 验证 |
| R3 | macOS bind mount 的 IO 性能 | 构建和测试变慢 | ✅ 已解除（M0-2：依赖放在 volume 时只比宿主机慢 6%） |
| R4 | 宿主机代理只提供 SOCKS | Squid 无法串联上游 | ✅ 已解除（M0-3：混合端口可以当 HTTP 上游） |
| R5 | 白名单太严，Agent 夜里被卡住 | 进度停滞 | `net denied` 可以快速定位；语言栈预设要尽量完整 |
| R6 | 共享 proxy 需要带认证的代理 URL，部分工具可能不支持 | 这些工具在 shared 模式下无法联网 | ✅ 已解除（M0-4：被测的 10 个工具都兼容） |
| R8 | 共享 proxy 是单点 | 异常时所有 shared Task 断网 | `sbx doctor` 检测并重建；对此敏感的 Task 用 dedicated |
| R7 | `sbx port` 的实现方式（转发容器还是重建容器） | 实现复杂度 | M3 实现时决定 |
| R9 | Claude Code 的内部状态字段（引导、bypass 警告）随版本变化 | 交互模式卡在对话框上，无人值守失效 | 升级时和 doctor 里做冒烟测试；出现问题时退回 headless 模式 |
| R10 | Docker VM 内存（8GB）不足以支撑 `max_running × memory` | 并发高峰时 VM 级 OOM | ✅ 已决定：VM 调到 10GB，每个 Task 3g × 3；超出时给出警告 |
| R11 | 云端 MCP 被拦截后 claude 持续重试 | 日志噪音，有少量开销 | `net denied` 分类隐藏；M2 查找能从源头关闭的官方开关 |
