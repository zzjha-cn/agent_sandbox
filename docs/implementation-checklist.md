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

## M1 MVP：一个 Task 能无人值守地跑起来

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
- [ ] **M1-22** 端到端验收：跑通本阶段开头的完成标准，并确认：
  - 容器里 `curl` 一个白名单之外的域名会失败。
  - 容器里看不到宿主机的 `~/.ssh`。
  - 宿主机上的 `node_modules` 没有被写入。

---

## M2 安全与配置

> 完成标准：篡改 `.sbx/` 会被拦住；被拒的域名能一键放行；open 模式和 dedicated proxy 模式都可用；你和团队成员 clone 下来，执行 `sbx trust` 后就能 `sbx run`。

### 配置
- [ ] **M2-1** 合并四层配置（§9.1）：列表取并集，标量后者覆盖前者；`sbx config show` 打印最终生效的值，并标注每个值来自哪一层。
- [ ] **M2-2** 项目层校验：出现密钥类字段或绝对路径时直接报错。

### 信任确认
- [ ] **M2-3** `internal/trust`：计算哈希，保存快照，生成 diff（§9.3）。
- [ ] **M2-4** 在 `sbx run` 里加上信任检查；`sbx trust` 命令。
- [ ] **M2-5** 测试：修改 `sandbox.toml` 或 `.sbx/Dockerfile` 后，`run` 会被拒绝；执行 `trust` 之后可以运行；已经在跑的 Task 不受影响。

### 网络
- [x] **M2-6** open 模式：`--net open` 和配置项 `network.mode`；shared 模式下生成 open 片段。**默认值已改为 `open`**（2026-10-08，ADR 0005 修订）。
- [ ] **M2-7** dedicated 模式：sidecar 的创建和销毁，`--proxy dedicated` 和配置项 `network.proxy`。
- [ ] **M2-8** 上游代理：`network.upstream` 对应 `cache_peer`；Linux 上给 squid 容器加 `--add-host host-gateway`。
- [ ] **M2-9** 项目层白名单 `network.allow`。
- [x] **M2-10** `sbx net denied [task]`：解析 access.log，shared 模式下按用户名过滤；**分三类**统计：不在白名单（默认展示）/ 策略拦截和遥测（`--all`）/ 407 认证失败（§6.5）。
- [ ] **M2-10a** `network.cloud_mcp` 和 `--cloud-mcp` 解除云端 MCP 拦截（ADR 0015）。<br>R11 已解决：`cloud_mcp = false`（默认）时在注入的 settings 里写 `disableClaudeAiConnectors: true`，claude 不再去取云端连接器，重试风暴消失。`--cloud-mcp` 命令行开关还没做。
- [x] **M2-11** `sbx net allow <host>...`：写入个人全局配置（保留注释和排版），对运行中的 Task 重新下发白名单并 reconfigure。`--project` 待 M2-1 的项目层配置，当前会明确报错。

### 认证
- [ ] **M2-12** `sbx login claude|codex`：按 M0-1 的结论实现。
- [ ] **M2-13** API key 注入：`agents.<name>.api_key_env` 和 `api_key_file`，优先级高于订阅登录。

### 资源
- [ ] **M2-14** 并发检查：running 和 idle 的 Task 数达到 `max_running` 时拒绝启动；`max_running × memory > Docker VM 内存 × 0.85` 时给出警告（R10）。
- [ ] **M2-15** 资源上限支持在各配置层覆盖。

---

## M3 完整体验

> 完成标准：整夜跑 `/auto-it`，第二天通过通知和 `sbx ls` 掌握全部结果。py-rust 和自定义 Profile 都可用，Codex 可用。

### 镜像和 Profile
- [ ] **M3-1** `assets/profiles/py-rust/Dockerfile`（§5.2 B），并加上对应的语言栈白名单预设。
- [ ] **M3-2** 自定义 Profile：读取 `.sbx/Dockerfile` 或 `profile.image`；检查是否为 Debian 或 Ubuntu 系，不是就给出明确报错。
- [ ] **M3-3** 根据项目里的版本文件（`.tool-versions`、`mise.toml`、`.nvmrc`、`.python-version`、`rust-toolchain.toml`）用 mise 自动安装运行时，结果放进 `sbx-mise`。
- [x] **M3-4** `sbx upgrade`：只重建 Agent 层；支持锁定 Agent 版本。（M1 收尾时提前实现，镜像里关闭自动更新；冒烟测试复用下次 run 的检查）
- [ ] **M3-5** 依赖遮盖改为由配置驱动（`deps.mask`），并给每个 Profile 设好默认值（`.venv`、`target` 等）。

### Agent
- [ ] **M3-6** 支持 Codex：生成 `config.toml`，带上 `--dangerously-bypass-approvals-and-sandbox`，支持 `--agent codex` 和 `default_agent`。
- [ ] **M3-7** headless 模式：`sbx run <task> -p "..."`，输出写入 `run.log`；`sbx logs`。

### 状态和通知
- [ ] **M3-8** 注入状态 hooks（M0-5 已验证事件映射：SessionStart/Stop → idle，UserPromptSubmit/PreToolUse → running），`status.json` 区分 running 和 idle。
- [ ] **M3-9** `sbx ls` 补全显示：idle 状态、`exited(code|oom)`、最后活动时间、被拒请求数，以及 `--all`。
- [ ] **M3-10** 执行 `on_idle` / `on_exit`：在容器内运行，**`on_idle` 挂在 Notification 事件上**（空闲约 60 秒），自动把 webhook 的主机加进该 Task 的白名单。

### 其他命令
- [ ] **M3-11** `sbx port <task> <port>`：先确定 R7 的实现方式（转发容器还是重建容器），只绑定到 `127.0.0.1`。
- [ ] **M3-12** `sbx drop <task>`：输入 task 名确认后，删除分支。
- [ ] **M3-13** `sbx doctor`：检查 docker、上游代理、登录态、信任状态、`sbx-proxy` 的健康状况（R8）、VM 内存是否足够（R10），以及**交互模式的冒烟测试**（tmux 启动后没有对话框，R9），发现问题时给出修复建议。

### 发布
- [ ] **M3-14** 交叉编译 darwin/linux × amd64/arm64；写安装说明。
- [ ] **M3-15** 写 README：快速上手、团队接入（提交 `.sbx/sandbox.toml`，然后各自 `sbx trust`）、常见问题（被代理拦截、OOM、登录）。

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
