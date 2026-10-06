# 2026-10-06 · M1 实现完成

> 命名规则：`docs/changelog/<YYYY-MM-DD>_<主题>.md`，一天一个文件。写给接手开发的人或 agent：今天改了什么、为什么改、现在处于什么状态、下一步从哪开始。

## 一句话

`sandbox/core` 从零实现了 M1。内容包括：`run`、`attach`、`shell`、`stop`、`ls`、`path`、`done`、`memory pull`、`upgrade` 共 9 个命令，shared squid 代理，web-go 镜像，项目记忆同步。自动化测试和 e2e（32 项）全部通过，交互模式已在真实项目上使用。**全部改动尚未提交**，当前在 `sand` 分支。

## 先读这些

| 文档 | 用途 |
|---|---|
| `docs/architecture.md` | 现在的代码怎么工作，按"问题 → Task → 四个方面 → 生命周期"组织 |
| `docs/walkthrough.md` | 用一个具体场景按顺序讲每条命令背后发生了什么 |
| `docs/m1-acceptance.md` | 测试结果、实现中遇到的问题、和设计的偏差 |
| `docs/implementation-checklist.md` | 总清单：M1 全部完成（M1-22 除外），M3-4 提前完成 |
| `core/README.md` | 安装、配置、登录、日常用法 |

## 今天的变更

### 1. M1 主体（`core/`，新增）

- 工程：Go module `sandx`（go 1.27），依赖 cobra 和 BurntSushi/toml；`make build/test/test-docker/lint/e2e`。
- 各包：

  | 包 | 职责 |
  |---|---|
  | `workspace` | git |
  | `task` | 命名、state、状态推导 |
  | `image` | 镜像 hash 与构建 |
  | `proxy` | squid 渲染、apr1、代理生命周期 |
  | `agent` | 挂载、预置、tmux、冒烟检查 |
  | `docker` | docker CLI 封装 |
  | `config` | 配置 |
  | `fsutil` | 原子写 |
  | `cli` | 命令 |

- 资源：Dockerfile、entrypoint、hooks、白名单、squid 模板都通过 `assets/` 用 go:embed 打包进二进制。
- 默认资源配额按方案 B：每个 Task 2 CPU、3g 内存、1024 个进程；`max_running = 3`；Docker VM 10GB。

### 2. 项目记忆同步（新增 ADR 0016）

- **问题**：设计时漏掉了 claude 的项目自动记忆。沙箱和宿主机里的记忆是两份。
- **方案**：用户选了方案 A。
  - 每次启动 Agent 前，把宿主机的记忆导入沙箱；
  - 用 `sbx memory pull` 手动导回宿主机，先展示 diff，确认后才写入。
- **同步规则**：三方比较，以 `~/.sbx/memory/<key>.json` 记录的上次同步 hash 为基准。`MEMORY.md` 两边都改了时按行合并；其他文件两边都改了时标记冲突、不覆盖；两个方向都不删除文件。
- **代码**：`internal/memory/`、`internal/cli/memory.go`。

### 3. `sbx path` 和 `ls` 的 PATH 列

- **起因**：用户反馈"在沙箱里新建的文件，在仓库目录里看不到"。这符合设计：改动在 Task 的 worktree 里，合并分支后才回到仓库目录。
- **方案**：用户选了方案 C，`ls` 加 PATH 列，同时新增 `sbx path` 命令，两样都做。

### 4. 交互验收后的 4 个修正

| 问题 | 改动 | 位置 |
|---|---|---|
| 容器里的 claude 无法自更新（npm 全局目录归 root） | 镜像设置 `DISABLE_AUTOUPDATER=1`；新增 `sbx upgrade`：查出最新版本记到 `~/.sbx/claude-version`，再重建 Agent 层。只对新建的 Task 生效 | `assets/agent-layer/Dockerfile`、`internal/cli/upgrade.go`、`image.Builder.EnsureProfile` |
| 登录提示里的代理地址写死成 7890 | 改为读取 `network.upstream`，没配置就不带代理参数 | `internal/cli/run.go` 的 `loginHelp` |
| `done` 不提醒导回记忆 | `done` 之前计算沙箱 → 宿主机的差异，有未导回的就提示 | `internal/cli/memory.go` 的 `remindPull`、`cmds.go` |
| squid 的 access.log 无限增长 | 设置 `logfile_rotate 1`；每次 run 时超过 20MB 就执行 `squid -k rotate`；proxy 容器的 docker 日志限制为 10MB × 2 | `assets/proxy/squid.conf.tmpl`、`proxy.Shared.rotateIfLarge`、`docker.RunSpec.LogMaxSize` |

### 5. 文档

| 状态 | 文件 |
|---|---|
| 新增 | `docs/architecture.md`、`docs/walkthrough.md`、`docs/m1-acceptance.md`、`docs/adr/0016-project-memory-sync.md`、`core/README.md`，以及本文件 |
| 修改 | `CONTEXT.md`（状态改为 M1 已实现，加了链接）、`docs/design.md`（回写实现中的偏差）、`docs/implementation-checklist.md`、`docs/m1-checklist.md` |

## 和设计的偏差（已回写 design.md）

| 偏差 | design.md 位置 |
|---|---|
| 宿主机 `CLAUDE.md` 改为快照复制；skills、agents、commands 只读挂到 `/sbx/host-claude/`，再建软链接 | §4.2 |
| entrypoint 带参数时直接 exec 参数；Profile 里的工具要放进 `/usr/local/bin`，否则 login shell 找不到 | §5.3 |
| squid 启动前删除 PID 文件，并加 `shutdown_lifetime 1 seconds`；用占位片段 `00-empty.conf` 保证 include 的通配符至少匹配一个文件；加日志轮转 | §6.4 |
| `sbx upgrade` 只对新建的 Task 生效，不是原来写的"已有 Task 下次启动生效"：容器的镜像不能原地替换 | §5.3、§10 |
| 新增 `sbx path`、`sbx memory pull` 两个命令 | §10 |

## 验证状态

| 项 | 结果 | 怎么复现 |
|---|---|---|
| 单元 + golden | 通过 | `make test`（更新 golden 文件：`go test ./internal/proxy -update`） |
| lint | 通过（golangci-lint 未安装，改用 `go vet` 加 gofmt） | `make lint` |
| Docker 集成 | 通过（代理隔离矩阵、热加载、日志轮转、镜像内容） | `make test-docker`，第一次运行要构建镜像 |
| e2e | 32/32 通过 | `make e2e`，前提是 `sbx-home` 已登录 |
| `sbx upgrade` 实跑 | 通过，构建出 `sbx/web-go:dc6755ad157f`（claude 2.1.291）；再跑一次提示"已是最新" | `bin/sbx upgrade` |
| 真实仓库交互 | 通过（`wande-applet-zs` 的 Task `wd-ts`） | — |
| 真实仓库无人值守（M1-22 / 9.2） | **未做** | 2 个 Task 并行，离开 30 分钟以上，回来 review 并合并 |

## 当前环境（接手前务必知道）

- `~/.sbx/config.toml` 里只配置了 `[network] upstream = "http://host.docker.internal:7890"`，也就是宿主机上的代理。
- `~/.sbx/claude-version` 的内容是 `2.1.291`。
- **正在运行**：`sbx-wande-applet-zs-3cc061-wd-ts`（用户的真实项目，用的是旧镜像）和 `sbx-proxy`。不要动它们。
  - 现有的 sbx-proxy 是修正之前创建的，还没有 docker 日志上限，要等它下次重建才会生效。
- **不要删除**：volume `m01-home`、`m05-home` 里存着 M0 阶段的 Claude 凭据；`sbx-home` 里是当前的登录态。
- 可以清理但要先问用户：M0 留下的 `sbx-m04-tools`、`sbx-m05` 镜像，以及上面两个 M0 volume。

## 已知问题

- claude 画面会提示 `Remote managed settings failed to load (401)`。暂时不影响使用，可能和凭据是从 M0 的 volume 复制过来的有关，继续观察。
- 已有的 Task 换镜像只能 `done` 后重新 `run`。
- 还没有项目级配置，各项目的白名单、遮盖目录只能写在全局配置里。

## 踩过的坑

- 改了 `assets/` 之后必须重新 `make build`。资源是 embed 进二进制的，不重新构建就会用旧内容，还会算出旧的镜像 hash。
- 在 login shell（tmux、`bash -l`）里，`/etc/profile` 会重置 PATH，所以 Profile 里的工具要软链接到 `/usr/local/bin`。
- squid 被 SIGKILL 后会残留 PID 文件，下次启动时报 "already running"。现在的启动命令已经处理了这种情况。
- 记忆合并后，基准要记成**源端**的 hash，不能记合并结果的 hash，否则下一次反方向同步时会出错。有测试覆盖。
- squid 的错误信息输出到 stderr，查问题要用 `docker logs`，sbx 里对应 `docker.Client.Logs`。

## 下一步（建议顺序）

1. **用户确认后提交**：M1 代码、4 个修正和文档一起提交。不要擅自提交。
2. **M1-22 无人值守验收**：需要用户配合离开一段时间。结果记到 `m1-acceptance.md` 的 9.2 节。
3. **M2**，按日常使用频率排序：

   | 顺序 | 内容 | 清单编号 |
   |---|---|---|
   | 1 | `sbx login` | M2-12 |
   | 2 | 项目配置 `.sbx/sandbox.toml` 和信任确认 | M2-1～5、M2-9 |
   | 3 | `sbx net denied/allow` | M2-10、M2-11 |
   | 4 | `max_running` 和内存预算检查 | M2-14、M2-15 |
   | 5 | open 模式、dedicated 代理、API key | M2-6、M2-7、M2-13 |

   M2-8（上游代理）在 M1 里已经基本完成。
4. Codex 支持放到 M3。

## 协作约定

- 只有用户要求时才提交。
- 删除东西要写完整路径，不用通配符。
- 不要绕过安全检查。
- 仓库根目录的 `CLAUDE.md`（mind-park）要求每轮回复：
  - 开头写意图卡，结尾写停车卡；
  - 有实质产出时，把人话版追加到 `notes/2026-10-04_1937_sandbox-m0.md`。
