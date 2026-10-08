# M1 实施清单：MVP

| 项 | 内容 |
|---|---|
| 目标 | 一个 Task 能在沙箱里无人值守地跑起来；两个 Task 能并行 |
| 依据 | [design.md v1.1](design.md)、ADR 0001–0015（`private/adr/`，未纳入版本控制）、[M0-SUMMARY](../spikes/M0-SUMMARY.md)、总清单 [implementation-checklist.md](implementation-checklist.md) 的 M1-1 至 M1-22 |
| 日期 | 2026-10-04 |
| 代码位置 | `core/`（module `sandx`，二进制名 `sbx`） |

**M1 完成标准**：在一个真实仓库上开 2 个并行 Task，claude 无人值守地各完成一次改动，在主仓库里能看到两个 `sbx/<task>` 分支；同时满足 §9 的安全检查。

---

## 0. 范围

**M1 要做**
- 命令：`sbx run`（交互模式，可以 `--detach`）、`attach`、`stop`、`shell`、`ls`、`done`
- 内置 Profile `web-go`、Agent 层叠加、派生镜像缓存
- shared proxy（allowlist 模式、proxy_auth、策略拦截云端 MCP）
- 两层配置：内置默认 + `~/.sbx/config.toml`
- Claude 首次启动状态的预置；状态 hooks 只写文件（`sbx ls` 读取 running/idle）

**M1 不做**（括号里是计划放在哪个阶段）
- 四层配置合并和项目配置（M2）
- 信任确认（M2）
- 并发上限的检查（M2）
- open 模式、dedicated proxy、上游代理的开关和 `net denied/allow`（M2）
  - 上游代理在 M1 **先写死从配置读取**，因为你的环境离不开它，见 WP4
- `sbx login`（M2，M1 用手动步骤代替）
- headless `-p`、`on_idle` 通知、`port`、`drop`、`doctor`、py-rust、自定义 Profile、Codex（M3）

---

## 1. 前置条件（开工前完成）

- [x] **P-1** Docker Desktop → Settings → Resources：**内存调到 10GB**，CPU 保持 8（ADR 0012 修订）。完成后用 `docker info --format '{{.MemTotal}}'` 确认约等于 10GB。
- [x] **P-2** 确定 Go module 路径（实际：`core/`，module `sandx`）。原计划先用 `sbx`，等有了远端仓库再改成真实地址（只需改 `go.mod` 和 import）。
- [ ] **P-3** 清理 M0 的测试镜像（可选）：`docker rmi sbx-m04-tools sbx-m05`。测试 volume `m01-home`、`m05-home` 可以保留做对照。
- [ ] **P-4** 选一个**真实验收仓库**，用于 WP9（建议用你手头一个 Next.js 或 Go 项目，规模中等，有测试）。

---

## 2. 工作包与顺序

```
WP1 骨架 ─┬─► WP2 docker 封装 ─┬─► WP5 镜像 ──────────┐
          │                    ├─► WP6 网络与 proxy ───┤
          ├─► WP3 配置 ────────┤                       ├─► WP8 命令 ─► WP9 验收
          └─► WP4 Workspace/Task ┴─► WP7 容器与 Agent ──┘
```

规模估计：S ≤ 半天，M ≈ 1 天，L ≈ 2 天以上。

---

## WP1 工程骨架 · S · 对应 M1-1、M1-3

- [x] **1.1** `go mod init sbx`；Go 版本跟宿主机一致（1.27）。
- [x] **1.2** 按 design §12 建目录：`cmd/sbx`、`internal/{config,workspace,task,image,docker,proxy,agent,fsutil}`、`assets/`。
- [x] **1.3** CLI 框架用 cobra：根命令，加上 `run/attach/stop/shell/ls/done` 六个空子命令，全局参数 `--verbose`（打印执行的 docker 和 git 命令）。
- [x] **1.4** `internal/fsutil`：`AtomicWrite(path, data, perm)`（在同一目录下写临时文件，fsync，再 rename）。**之后所有会被容器读取的文件都必须通过它写入**（M0-3，M1-15b）。
- [x] **1.5** `Makefile`：`build`、`test`（单元测试）、`test-docker`（`-tags docker` 集成测试）、`lint`（golangci-lint）、`e2e`（运行 `scripts/e2e-m1.sh`）。
- [x] **1.6** `assets/` 用 `//go:embed` 打包，提供 `assets.FS`。

**验收**：`make build test lint` 全部通过；`sbx --help` 能列出 6 个子命令。

---

## WP2 docker CLI 封装 · M · 对应 M1-2

- [x] **2.1** `docker.Client`：用 `exec.Command("docker", ...)` 执行，捕获 stdout 和 stderr；出错时返回的错误里**带上完整命令行和 stderr**。
- [x] **2.2** 方法（只做 M1 用得到的）：
  - 容器：`Run(spec)`、`Start`、`Stop`、`Rm`、`Exec(name, cmd, opts{tty, user, env})`、`ExecInteractive`（把 stdin、stdout、tty 直接交给用户，用于 attach 和 shell）、`Inspect`（解析 `State.Status/ExitCode/OOMKilled`）
  - 网络：`NetworkCreate(name, internal bool, labels)`、`NetworkConnect(net, ctr, alias)`、`NetworkDisconnect`、`NetworkRm`
  - volume：`VolumeCreate(name, labels)`、`VolumeRm`
  - 镜像：`ImageExists(ref)`、`Build(ctxDir, dockerfile, tag, buildArgs, labels)`（构建输出透传给用户）
  - 查询：`ListByLabel(kind, selector)`，用 `--format '{{json .}}'` 逐行解析
- [x] **2.3** `RunSpec` 结构体：name、image、labels、network、mounts（bind 或 volume，ro 或 rw）、env、user、resources（cpus、memory、pids）、addHosts、entrypoint、cmd。把它转换成 docker 参数的逻辑做成纯函数，方便测试。
- [x] **2.4** 测试：`RunSpec → []string` 用 golden 文件做单元测试；`-tags docker` 集成测试覆盖创建、查询、删除一个 busybox 容器、一个网络、一个 volume。

**验收**：单元测试和 docker 集成测试通过；故意执行一条错误命令时，错误信息里有完整命令和 stderr。

---

## WP3 配置（最小版）· S · 对应 M1-4

- [x] **3.1** 用 Go 结构体定义 schema（字段名和 design §9.2 一致）：`default_agent`、`max_running`、`network{upstream, proxy, mode, cloud_mcp, allow}`、`resources{cpus, memory, pids}`、`deps{mask}`、`agents.claude{version}`。
- [x] **3.2** 内置默认值：`resources = {2, "3g", 1024}`、`max_running = 3`、`network.mode = "allowlist"`、`network.proxy = "shared"`、`network.cloud_mcp = false`、`deps.mask = ["node_modules"]`。
- [x] **3.3** 读取 `~/.sbx/config.toml`（不存在时用默认值），然后覆盖到默认值上。未知字段**给出警告**，不报错。
- [x] **3.4** `network.upstream`：M1 就要支持（你的环境必须走宿主机代理）。只做字符串解析，得到 `host:port`，供 WP6 渲染 `cache_peer`。
- [x] **3.5** 测试：默认值、覆盖、未知字段警告、非法的 memory 字符串。

**验收**：单元测试通过；`sbx run --verbose` 能打印出最终生效的配置。

---

## WP4 Workspace 与 Task · M · 对应 M1-5、M1-6、M1-7

- [x] **4.1** `workspace.Resolve(cwd)`：用 `git rev-parse --show-toplevel` 找到仓库根；用 `git rev-parse --git-common-dir` 找到 `.git` 的绝对路径（**在 worktree 里运行 sbx 时也要能找回主仓库**）。
- [x] **4.2** ws id = `<basename>-<sha1(abs_root)[:6]>`，basename 要规范化成 `[a-z0-9-]`。
- [x] **4.3** task 名校验：`^[a-z0-9][a-z0-9-]{0,39}$`；`main` 是保留名。
- [x] **4.4** worktree：
  - 创建：`git worktree add ~/.sbx/worktrees/<ws>/<task> -b sbx/<task> <base>`（base 默认是当前 HEAD；支持 `--base`）。
  - 已存在：复用。分支已存在但 worktree 不存在：用 `git worktree add <path> sbx/<task>` 挂上已有分支，并给出提示。
  - `main` Task：直接使用仓库根，不创建 worktree。
- [x] **4.5** state 目录 `~/.sbx/state/<ws>/<task>/`：`meta.json`（base、profile、image、创建时间、proxy 模式、task-id）、`gen/`、`status.json`、`events.log`。全部用 `AtomicWrite`。
- [x] **4.6** 状态推导 `task.Status()`：
  - 容器不存在 → `absent`
  - 容器已停止 → `stopped`，或 `exited(code|oom)`
  - 容器在运行 → 读取 `status.json`：`running` / `idle`；没有这个文件时显示 `starting`
- [x] **4.7** 测试：
  - 在临时目录里建一个 git 仓库，测 Resolve、ws id、worktree 的创建和复用、在 worktree 里调用 Resolve 能回到主仓库；
  - 状态推导用假的 inspect 结果做表驱动测试。

**验收**：单元测试通过；在 worktree 目录里运行 `sbx ls`，识别到的仍然是主仓库对应的 ws。

---

## WP5 镜像 · M · 对应 M1-8、M1-9、M1-10

- [x] **5.1** `assets/profiles/web-go/Dockerfile`：基于 `debian:bookworm-slim`，安装 Go（官方 tarball，取最新稳定版）、Node LTS（NodeSource 或官方 tarball）、corepack 和 pnpm、`python3`、`python3-venv`、`build-essential`、`ca-certificates`、`curl`。
- [x] **5.2** `assets/agent-layer/Dockerfile.tmpl`（参数：`BASE`、`UID`、`GID`、`CLAUDE_VERSION`）：
  - 安装 `tini tmux git ripgrep jq`；用 `npm i -g @anthropic-ai/claude-code@${CLAUDE_VERSION}`（codex 在 M3 再加）。
  - 创建用户 `agent`（UID/GID 和宿主机一致；如果已经存在同 UID 的用户，就**改名或复用**，比如 node 镜像里 1000 号用户叫 node）。
  - 环境变量：`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`；`npm_config_cache=/sbx/cache/npm`、`PIP_CACHE_DIR=/sbx/cache/pip`、`UV_CACHE_DIR=/sbx/cache/uv`、`GOMODCACHE=/sbx/cache/go-mod`、`GOCACHE=/sbx/cache/go-build`、`CARGO_HOME=/sbx/cache/cargo`。
  - 把 `assets/agent-layer/entrypoint.sh` 复制进镜像：`exec sleep infinity`（Agent 由 sbx 通过 exec 拉起）。
  - `ENTRYPOINT ["tini","--","/sbx/entrypoint.sh"]`，`USER agent`。
- [x] **5.3** `assets/agent-layer/hooks/status.sh`：来自 M0-5 的脚本，状态目录改为 `/sbx/state`。它在 WP7 渲染进 gen 目录，**不打进镜像**，方便以后不重建镜像就能更新。
- [x] **5.4** `image.Ensure(profile)`：
  - hash = sha256（profile Dockerfile 内容、agent-layer 模板、`CLAUDE_VERSION`、UID、GID）取前 12 位。
  - 先构建 `sbx/profile-web-go:<profile_hash>`，再构建 `sbx/web-go:<hash>`。已存在就跳过。
  - 构建参数 `HTTP(S)_PROXY` 取自 `network.upstream`（M0-4），失败时**自动重试 1 次**。
  - 打上 label `sbx.kind=image`。
- [x] **5.5** 测试：hash 的稳定性（相同输入得到相同 hash；改任意一个输入，hash 就变）；docker 集成测试：构建一次派生镜像，然后验证：
  - 以 `agent` 身份运行 `id`、`claude --version`、`tmux -V`、`jq --version` 都正常；
  - `echo $GOMODCACHE` 输出预期路径。

**验收**：第一次 `Ensure` 能构建成功；第二次立即返回；容器里默认用户不是 root。

---

## WP6 网络与 shared proxy · L · 对应 M1-11、M1-12、M1-13

- [x] **6.1** `assets/allowlist/builtin.txt`：`.anthropic.com .claude.ai .claude.com .openai.com .chatgpt.com github.com`。
- [x] **6.2** `assets/allowlist/web-go.txt`：`registry.npmjs.org .yarnpkg.com pypi.org files.pythonhosted.org proxy.golang.org sum.golang.org .githubusercontent.com codeload.github.com`，再加上你常用的国内镜像（待补）。
- [x] **6.3** `assets/allowlist/policy-block.txt`：`mcp-proxy.anthropic.com`（ADR 0015）。
- [x] **6.4** 白名单渲染 `proxy.RenderAllow(layers...)`：取并集 → **去重**（`.x.com` 已经包含 `x.com`）→ 排序；如果 `cloud_mcp=false`，策略拦截域名不能出现在放行列表里。
- [x] **6.5** 主配置模板 `assets/proxy/squid.conf.tmpl`，按 design §6.4 的 shared 段写：`auth_param basic`、`include /etc/sbx/tasks/*.conf`、`logformat sbx`、`cache deny all`；有 `upstream` 时加上 `cache_peer` 和 `never_direct allow all`。
- [x] **6.6** Task 片段模板 `tasks/<task-id>.conf`：
  ```
  acl u_<id> proxy_auth <task-id>
  acl blk_<id> dstdomain "/etc/sbx/block/<task-id>.txt"     # cloud_mcp=false 时
  acl a_<id> dstdomain "/etc/sbx/allow/<task-id>.txt"
  http_access deny u_<id> blk_<id>
  http_access allow u_<id> CONNECT SSL_ports a_<id>
  http_access allow u_<id> a_<id>
  ```
  ACL 名称里的 `<id>` 要把 task-id 中的 `.`、`-` 替换成 `_`。
- [x] **6.7** 凭据：task-id = `<ws>.<task>`，token 用 32 字节随机数（hex），写入 `state/.../proxy.cred`（权限 0600）；`~/.sbx/proxy/passwd` 用 **apr1** 格式（M0-4 用 `openssl passwd -apr1` 验证过；Go 里用纯 Go 实现，或者调用 openssl，任选其一）。
- [x] **6.8** `proxy.EnsureShared()`：
  - 创建网络 `sbx-egress`（普通 bridge），带 label `sbx.kind=net`。
  - 容器 `sbx-proxy` 不存在时：`--entrypoint squid ubuntu/squid:latest -f /etc/sbx/squid.conf -NYC`，挂载 `~/.sbx/proxy:/etc/sbx:ro`（**整个目录**），`--memory 128m`；在 Linux 宿主机上加 `--add-host host.docker.internal:host-gateway`。
  - 容器已停止就启动它。
- [x] **6.9** `proxy.AttachTask(task)`：写入 allow、block、片段、passwd（全部原子写入）→ 执行 `docker network connect --alias proxy <task-net> sbx-proxy` → `docker exec sbx-proxy squid -k reconfigure`；**再检查一次 reconfigure 的输出里有没有 `ERROR|FATAL`**，有就报错。
- [x] **6.10** `proxy.DetachTask(task)`：删除片段和 passwd 里对应的行 → reconfigure → network disconnect。
- [x] **6.11** `proxy.StopIfIdle()`：如果没有任何运行中的 shared Task，就停掉 `sbx-proxy`（不删除）。
- [x] **6.12** Task 网络：`sbx-<ws>-<task>-net`，加 `--internal`，带 label。
- [x] **6.13** 测试：
  - 单元测试：白名单去重和减去策略拦截层、片段渲染（golden 文件）、ACL 名称转义、apr1 能被 `basic_ncsa_auth` 接受（这一项放进 docker 集成测试）。
  - docker 集成测试，复现 M0-4 的身份隔离矩阵：A 放行、B 被拒、冒用被拒、不带凭据被拒、策略拦截生效。

**验收**：集成测试复现出 M0-4 和 M0-5 的结论，包括 `mcp-proxy.anthropic.com` 返回 403。

---

## WP7 容器与 Agent · L · 对应 M1-14、M1-15、M1-15a、M1-15b、M1-16、M1-21

- [x] **7.1** volume：`sbx-home`、`sbx-cache` 首次创建时，用一次性容器 `chown <UID>:<GID>`，并在 `sbx-cache` 里建好 `npm pip uv go-mod go-build cargo` 这几个子目录。Task 的依赖 volume 命名为 `sbx-<ws>-<task>-dep-<i>`，对应 `deps.mask` 里的每一项，创建后同样要 chown。
- [x] **7.2** **宿主机 Claude 配置的挂载方式**（新问题：`~/.claude/CLAUDE.md` 是单个文件，按 M0-3 的结论不能直接单文件挂载）：
  - 单个文件（`CLAUDE.md`）：每次 `run` 时**快照复制**到 `gen/host-claude/CLAUDE.md`。
  - 目录（`skills/`、`agents/`、`commands/`）：以目录方式**只读**挂载到 `/sbx/host-claude/<name>`。
  - 容器启动后由 sbx 执行一个 exec，在 `~/.claude/` 下创建指向 `/sbx/...` 的软链接（幂等）。
  - ⚠️ 如果宿主机的 skills 目录里有**指向外部的软链接**，在容器里会失效。WP7 实现时检测这种情况并给出警告（只警告，不修复）。
- [x] **7.3** 挂载列表（design §4.2），全部以宿主机的相同路径或固定路径挂载：
  - worktree（`main` Task 用仓库根）：rw，相同路径
  - `<repo>/.git`：rw，相同路径（worktree Task 才需要；`main` Task 已经包含在仓库根里）
  - 依赖 volume：叠加在 worktree 里的 `deps.mask` 路径上，**要排在 worktree 挂载之后**
  - `sbx-home` → `/home/agent`；`sbx-cache` → `/sbx/cache`
  - `state/<ws>/<task>/` → `/sbx/state`（rw）；`state/<ws>/<task>/gen/` → `/sbx/gen`（ro）
  - 宿主机 Claude 目录 → `/sbx/host-claude/*`（ro）
- [x] **7.4** 环境变量：
  - `HTTP(S)_PROXY`、`http(s)_proxy` 都设为 `http://<task-id>:<token>@proxy:3128`，`NO_PROXY=localhost,127.0.0.1`
  - 从宿主机 git config 读出 `GIT_AUTHOR_NAME/EMAIL` 和 `GIT_COMMITTER_NAME/EMAIL`
  - `SBX_TASK`、`SBX_WS`
- [x] **7.5** 资源：`--cpus 2 --memory 3g --pids-limit 1024`（取自配置）；label 为 `sbx.ws`、`sbx.task`、`sbx.role=agent`、`sbx.proxy=shared`。
- [x] **7.6** 渲染 gen 目录（原子写入）：`hooks/status.sh`（来自 assets）、`settings.sbx.json`（M0-5 的五个 hook 事件，命令指向 `/sbx/gen/hooks/status.sh`）。
- [x] **7.7** **预置首次启动状态**（design §7.2，M1-15a），在容器内以 `agent` 身份执行一个 jq 脚本（幂等，tmp + rename）：
  - `~/.claude.json`：`.hasCompletedOnboarding = true`、`.projects["<worktree 绝对路径>"].hasTrustDialogAccepted = true`；文件不存在时先创建 `{}`。
  - `~/.claude/settings.json`：`.theme //= "dark"`、`.skipDangerousModePermissionPrompt = true`。
- [x] **7.8** 启动 Agent：在 worktree 目录下执行 `tmux new-session -d -s agent -x 220 -y 50 'claude --dangerously-skip-permissions --settings /sbx/gen/settings.sbx.json'`。如果 tmux 会话已经存在，就不再重复启动。
- [x] **7.9** **启动冒烟检查**：启动后轮询 15 秒，抓取 tmux 画面。如果出现已知对话框的关键字（`Select login method`、`Bypass Permissions mode`、`Choose the text style`、`trust`），或者 `status.json` 一直没出现 `SessionStart`，就报错，并打印画面的最后 20 行（R9）。
- [x] **7.10** 手动登录说明，写进 `README.md` 的"首次使用"一节（M1-21）：
  ```bash
  docker run -it --rm -u <UID>:<GID> -e HOME=/home/agent -v sbx-home:/home/agent \
    -e HTTPS_PROXY=http://host.docker.internal:7890 sbx/web-go:<hash> claude auth login
  ```
  `sbx run` 时如果检测到 `claude auth status` 返回 `loggedIn: false`，就给出提示并退出。
- [x] **7.11** 测试：
  - 单元测试：挂载列表生成（golden 文件，区分 `main` Task 和 worktree Task）、jq 预置脚本的幂等性（在测试里对同一份 JSON 执行两次，结果相同）。
  - docker 集成测试：起一个容器后检查 `id -u` 不为 0；`~/.ssh` 不存在；在 `node_modules` 里写一个文件，宿主机上看不到。

**验收**：`sbx run demo --detach` 之后，`sbx attach demo` 能直接看到 Claude 的输入框，**没有任何对话框**；`status.json` 变为 `idle / SessionStart`。

---

## WP8 命令 · M · 对应 M1-17 至 M1-20

- [x] **8.1** `sbx run [task] [--base <ref>] [--detach]`，按 design §10.1 执行，跳过第 3 步（信任检查）和第 5 步（并发检查）：
  ```
  Resolve ws → 读取配置 → task 已存在？
    ├─ 容器在运行 → 跳到 attach
    └─ 已停止 → start → 补启动 tmux（7.8）→ 冒烟检查（7.9）→ attach
  否则：worktree → image.Ensure → proxy.EnsureShared → Task 网络
       → proxy.AttachTask → volume → gen 渲染 → docker run
       → 预置（7.7）→ 登录检查（7.10）→ 启动 tmux（7.8）→ 冒烟检查（7.9）
       → 写 meta.json → attach（--detach 时只打印提示）
  ```
  中途失败时，**清理掉本次新建的资源**（容器、网络、片段），但保留 worktree，并打印失败的步骤。
- [x] **8.2** `sbx attach <task>`：`docker exec -it -u agent <ctr> tmux attach -t agent`；如果 tmux 会话不存在，提示用 `sbx run` 重新拉起。
- [x] **8.3** `sbx shell <task>`：`docker exec -it -u agent -w <worktree> <ctr> bash`。
- [x] **8.4** `sbx stop <task>`：`docker stop`；然后 `proxy.StopIfIdle()`。
- [x] **8.5** `sbx ls`：显示当前 ws 下的所有 Task，列为 `TASK STATUS BRANCH AHEAD DIFF LAST-ACTIVE`：
  - AHEAD：`git rev-list --count <base>..sbx/<task>`
  - DIFF：`git diff --shortstat <base>...sbx/<task>`
  - LAST-ACTIVE：`status.json` 里的 ts
- [x] **8.6** `sbx done <task>`：确认 worktree 里**没有未提交的改动**（有的话拒绝，提示先提交或加 `--force`）→ 删容器 → `proxy.DetachTask` → 删网络 → 删依赖 volume → `git worktree remove` → 删 state → `proxy.StopIfIdle()`；**保留分支**，并打印分支名和合并提示。
- [x] **8.7** 测试：docker 集成测试走一遍完整流程：`run --detach → ls 显示 idle → stop → ls 显示 stopped → run 能恢复 → done → 资源清理干净`（用 label 查询，确认没有残留）。

**验收**：8.7 的集成测试通过；`sbx done` 之后，`docker ps -a`、`docker network ls`、`docker volume ls` 里都没有这个 Task 的残留。

---

## WP9 端到端验收 · M · 对应 M1-22

- [x] **9.1** `scripts/e2e-m1.sh`（自动化，使用一个夹具仓库）：
  1. 在临时目录里创建一个包含 `package.json` 的小仓库，然后 `git init`、提交。
  2. `sbx run t1 --detach`、`sbx run t2 --detach`。
  3. 通过 `docker exec ... tmux send-keys` 给两个 Task 各发一条指令，比如"在 README 末尾追加一行 `hello from <task>` 并 git commit"。
  4. 轮询 `status.json`，直到两个 Task 都回到 `idle / Stop`（超时 5 分钟）。
  5. 断言主仓库里有 `sbx/t1`、`sbx/t2` 两个分支，且各自领先 base 1 个提交。
  6. 安全断言（见 9.3）。
  7. `sbx done t1 t2`，断言没有资源残留。
- [x] **9.2** **真实仓库验收**（手动，使用 P-4 选定的仓库）：开 2 个并行 Task，各给一个真实的小需求，人离开至少 30 分钟；回来后 `sbx ls` 能看到两个 `idle`，分支上的改动可以 review 并合并。
- [x] **9.3** 安全检查（在容器内执行）。默认模式 2026-10-08 改成 `open` 之后，拦截类断言拆成两组：
  - open（t1、t2，默认）：
    - [x] `curl https://example.com` **成功**，但 squid 日志里依然有这条记录（两种模式都经过代理）
    - [x] `curl https://mcp-proxy.anthropic.com` 被拦截 —— 策略层不受 open 影响（ADR 0015）
  - allowlist（t3，`sbx run t3 --net allowlist`）：
    - [x] `curl https://example.com` 失败，squid 日志里有 `TCP_DENIED/403`，`sbx net denied t3` 列得出来
    - [x] 白名单内的 `api.anthropic.com` 仍然放行
  - [x] 不经过代理直连，`curl --noproxy '*' https://api.anthropic.com` 失败
  - [x] `ls ~/.ssh` 不存在；`/Users/<you>` 下只能看到挂载进来的仓库路径
  - [x] `id -u` ≠ 0
  - [x] 宿主机的 `<repo>/node_modules` 没有被写入
  - [x] 一个 Task 的容器访问另一个 Task 的容器失败（网络互相隔离）
  - [x] 修改 `~/.claude/skills` 时，容器里没有写权限
- [x] **9.4** 把验收结果写到 `docs/private/m1-acceptance.md`：每项的通过或失败、耗时、遇到的问题。

---

## 3. 完成定义（Definition of Done）

- [x] 上面每个 WP 的验收都通过；`make test lint test-docker e2e` 全部通过。
- [x] 9.2 的真实仓库验收完成，并且记录在 `private/m1-acceptance.md`。
- [x] 实现过程中和设计的偏差**回写进 design.md**（必要时补 ADR）。已知的一项：WP7.2 宿主机 Claude 配置的挂载方式。
- [x] 总清单 `implementation-checklist.md` 里的 M1-1 至 M1-22 全部勾选。
- [x] `README.md` 的"首次使用"一节写完：安装、登录、`sbx run`、`sbx done`。

## 4. 实现过程中的已知坑（来自 M0）

| 坑 | 出处 | 做法 |
|---|---|---|
| 单文件 bind mount 会读到截断的内容 | M0-3 | 一律挂载目录，并且原子写入 |
| `ubuntu/squid` 的 entrypoint 会吞掉传入的参数 | M0-4 | 用 `--entrypoint squid` 显式指定 |
| 镜像的默认缓存路径各不相同 | M0-2 | 用环境变量显式指定 |
| 以 root 运行测试时结果和宿主机不一致 | M0-2 | 一律以 agent 用户运行 |
| 交互模式卡在首次启动的对话框上 | M0-5 | 预置状态（7.7）并做冒烟检查（7.9） |
| 缺 `.claude.com` 时交互模式直接退出 | M0-5 | 内置白名单（6.1） |
| 客户端先发不带凭据的请求，日志里出现 407 | M0-4 | 正常现象，M1 不处理（M2 的 `net denied` 再分类） |
| 构建时偶发 TLS EOF | M0-4 | 自动重试 1 次（5.4） |
| macOS 自带的 bash 3.2 遇到空数组加 `set -u` 会报错 | M0-2 | e2e 脚本里避免这种写法，或者显式用 `/usr/bin/env bash` 调用新版 bash |
| 通配符删除命令会被 Claude Code 的安全检查拦截 | M0-5 | 脚本里按完整路径删除具体文件 |
