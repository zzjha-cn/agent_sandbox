# sbx 实现现状：设计与流程

> 对应代码：`core/`（M1 完成，含交互验收后的 4 个修正）。本文只写**代码现在实际怎么工作**。
> 目标设计见 [design.md](design.md)，按场景逐步讲解见 [walkthrough.md](walkthrough.md)。决策依据 `private/adr/` 和验收记录 `private/m1-acceptance.md` 不纳入版本控制，只在本地可见。

**阅读路线**：

1. 先看 sbx 要解决什么问题，以及围绕 Task 的核心思路（§1）。
2. 再看一个 Task 由哪些东西组成，以及放到机器上的全景图（§2）。
3. 然后分四个方面看 Task 怎么实现（§3～§6）。
4. 接着看生命周期怎么把四个方面串起来（§7）。
5. 最后用开头的问题检验安全边界（§8），并列出支撑部分和现状（§9～§10）。

---

## 1. 问题与核心思路

**问题**：想让 Claude Code 放开权限（`--dangerously-skip-permissions`）自己干活，人离开半小时，回来收结果。直接在宿主机上这么做有四个隐患：

| 隐患 | 对应的问题 |
|---|---|
| 改乱你正在用的代码目录 | 改动去哪 |
| 读到 `~/.ssh` 等密钥，或装依赖弄脏宿主机 | Agent 在哪跑 |
| 访问任意网站，泄露数据或拉来恶意依赖 | 能访问什么 |
| 回来时不知道它干到哪了 | 人怎么掌握 |

**思路**：把"一次 Agent 工作"抽象成 **Task**，每个 Task 在四个方面各有一道隔离或可见性措施。sbx 本身只是一个编排这些资源的命令行。它是单文件 Go 程序，通过调用 `docker` 和 `git` 命令完成所有操作（ADR 0013）。

```
                       ┌──────────── Task ────────────┐
  改动去哪       →     │ ① 代码    worktree + 分支     │  §3
  在哪跑         →     │ ② 运行    容器 + 镜像         │  §4
  能访问什么     →     │ ③ 网络    internal 网 + 代理  │  §5
  人怎么掌握     →     │ ④ 状态    hooks + ls + 记忆   │  §6
                       └──────────────────────────────┘
          生命周期 run → stop → run（恢复）→ done 把四者一起建立、暂停、拆除   §7
```

人的使用节奏是：开多个 Task 并行，用 `sbx ls` 看进度，review 分支后合并，最后 `sbx done` 清理。

---

## 2. 一个 Task 由什么组成（附全景图）

Task 属于一个 **Workspace**（ws），也就是一个主仓库。在 worktree 里执行 sbx 也会解析到主仓库。ws id 是 `<目录名>-<sha1(仓库根)[:6]>`，Task 的所有资源名都由 ws id 和 Task 名拼出来：

| 方面 | 每个 Task 独占 | 所有 Task 共享 |
|---|---|---|
| ① 代码 | worktree `~/.sbx/worktrees/<ws>/<task>`、分支 `sbx/<task>` | 主仓库的 `.git` |
| ② 运行 | 容器 `sbx-<ws>-<task>`；依赖 volume `sbx-<ws>-<task>-dep-<i>` | 镜像 `sbx/<profile>:<hash>`；`sbx-home`（登录态、claude 配置、记忆）；`sbx-cache`（npm、go 等缓存） |
| ③ 网络 | 网络 `sbx-<ws>-<task>-net`；代理身份 `<ws>.<task>` 加 token | 代理容器 `sbx-proxy`、出网网络 `sbx-egress` |
| ④ 状态 | `~/.sbx/state/<ws>/<task>/`（meta、status、token、生成文件） | `~/.sbx/memory/`（记忆同步基准） |

两点约定：

- `main` 是特殊的 Task，直接使用仓库根，不建 worktree 和分支。
- Docker 资源都打了标签 `sbx.ws`、`sbx.task`、`sbx.kind`。`ls` 和 `done` 按标签查找资源，不靠拼名字。

宿主机上 sbx 自己的目录：

```
~/.sbx/                     （可用 SBX_HOME 覆盖）
├─ config.toml  claude-version
├─ worktrees/<ws>/<task>/   ① 代码
├─ state/<ws>/<task>/       ④ 状态（gen/ 是生成后挂进容器的文件）
├─ proxy/                   ③ squid 配置，整个目录只读挂进 sbx-proxy
└─ memory/<key>.json        ④ 记忆基准
```

把两个并行的 Task 放到一台机器上，就是下面这张全景图。①～④ 对应前面四个方面：

```
┌─ 宿主机 macOS ──────────────────────────────────────────────────────────────┐
│                                                                             │
│  你 ──▶ sbx CLI ──(调用 docker / git 命令)──────────────────────┐           │
│                                                                 │           │
│  ① 主仓库 ~/code/app        ① ~/.sbx/worktrees/<ws>/<task>      │           │
│     └─ .git ◀── 提交写回 ──── worktree（每个 Task 一个）            │           │
│                                                                 │           │
│  ④ ~/.sbx/state/<ws>/<task>   ③ ~/.sbx/proxy   ~/.claude（只读）│           │
│                                                                 │           │
│  上游代理 127.0.0.1:7890 ◀───────────────────────────────┐       │           │
└─────────────────────────────────────────────────────────┼───────┼───────────┘
                                                          │       ▼ docker run / exec
┌─ Docker Desktop VM ─────────────────────────────────────┼───────────────────┐
│                                                         │                   │
│  ┌─ Task 网络 t1（internal）───────┐                      │                   │
│  │ ② sbx-<ws>-t1                   │                    │                   │
│  │   tini → sleep infinity         │                    │                   │
│  │   tmux → claude ───────────────────▶┐                │                   │
│  │   挂载 ①worktree+.git ④state   │   │                │                   │
│  └─────────────────────────────────┘   │  ③ sbx-proxy   │                   │
│                                        ├─▶ squid :3128 ─┘ sbx-egress        │
│  ┌─ Task 网络 t2（internal）───────┐     │  按 task-id    （可出网）          │
│  │ ② sbx-<ws>-t2  …  ─────────────────▶┘  套白名单                          │
│  └─────────────────────────────────┘                                        │
│                                                                             │
│  共享 volume：sbx-home（登录态、记忆）  sbx-cache（依赖缓存）               │
│  每个 Task：依赖 volume（node_modules）                                     │
└─────────────────────────────────────────────────────────────────────────────┘
```

图中的三条主线：

- **控制面**：sbx 在宿主机上运行，只通过 `docker` 和 `git` 命令管理容器、网络、volume 和 worktree。它本身不常驻：命令执行完就退出，Agent 在容器里继续运行。
- **代码**：worktree 同路径挂进容器。Agent 的提交经由主仓库的 `.git` 直接写回宿主机。
- **网络**：容器只能访问同一个 internal 网络里的 sbx-proxy。squid 按 task-id 套用白名单，放行的请求经 `sbx-egress` 发往上游代理或直接出网。两个 Task 之间没有共同网络。

---

## 3. 代码：改动去哪

**做法**：每个 Task 在自己的 git worktree 里工作，提交到自己的分支。你的仓库目录不受影响。

- **新建**：`git worktree add -b sbx/<task> <路径> <base>`，base 默认是当前 HEAD，记录到 `meta.json`。分支已经存在时直接复用，base 取分叉点。
- **同路径挂载**：worktree 在容器里的路径和宿主机完全相同。worktree 的 `.git` 文件指向主仓库的 `.git/worktrees/<task>`，所以主仓库的 `.git` 也要在同一路径**可写**挂载（ADR 0003）。这样 Agent 的提交直接写进主仓库，宿主机上 `git log sbx/<task>` 马上能看到。
- **提交身份**：git 作者取自仓库的 `git config`，通过 `GIT_AUTHOR_*` 和 `GIT_COMMITTER_*` 环境变量传入。
- **改动在哪看**：改动在 worktree 里，不在仓库目录。用 `sbx path <task>` 找到 worktree，合并分支后改动才回到仓库目录。
- **结束**：`done` 删除 worktree，**保留分支**。worktree 有未提交的改动时拒绝执行，除非加 `--force`。

---

## 4. 运行：Agent 在哪跑

**做法**：Agent 跑在容器里，以非 root 用户运行，只能看到明确挂进去的东西。

### 4.1 镜像：Profile 加 Agent 层（ADR 0007）

| 层 | 内容 |
|---|---|
| Profile `web-go` | Debian、Go、Node 24、pnpm、python3 |
| Agent 层 | tini、tmux、git、jq、claude-code、`agent` 用户（UID/GID 和宿主机一致，挂载文件的属主才对得上）、缓存目录环境变量、`DISABLE_AUTOUPDATER=1` |

- 镜像 tag 是输入内容的 hash：两个 Dockerfile、entrypoint、claude 版本、UID/GID。输入不变就复用已有镜像，变了就自动重建。
- 构建时通过 build-arg 传入上游代理，失败重试一次。
- claude 版本按以下顺序取，前面的优先：
  1. 配置里固定的版本；
  2. `sbx upgrade` 记录的版本；
  3. `latest`。

### 4.2 容器能看到什么

| 容器内 | 来源 | 模式 |
|---|---|---|
| worktree 路径、主仓库 `.git` 路径 | 宿主机同路径 | 读写（§3） |
| `<worktree>/node_modules` 等 | 依赖 volume | 读写，不落到宿主机 |
| `/home/agent`、`/sbx/cache` | `sbx-home`、`sbx-cache` | 读写 |
| `/sbx/state` | `state/<ws>/<task>` | 读写（hooks 写状态，§6） |
| `/sbx/gen` | `state/.../gen`（hooks、settings、宿主机 CLAUDE.md 快照） | 只读 |
| `/sbx/host-claude/{skills,agents,commands}` | 宿主机 `~/.claude/*` | 只读 |

除此之外宿主机的任何目录都不挂载，`~/.ssh` 也不例外。资源上限是每个 Task 2 CPU、3g 内存、1024 个进程。

### 4.3 Agent 怎么跑起来

- 容器的主进程是 `tini → sleep infinity`，只负责让容器保持运行。
- claude 由 sbx 用 `docker exec` 在 tmux 会话 `agent` 里启动，所以人可以随时 attach 进去，离开后它继续运行（ADR 0004）。
- 启动前先运行预置脚本，跳过首次启动的各种对话框；启动后做冒烟检查。具体步骤见 §7.3。

---

## 5. 网络：能访问什么

**做法**：容器所在的网络没有出网路由，唯一能访问的是代理。代理按 Task 的身份套用白名单（ADR 0005、0014）。

```
agent 容器 ──(Task 网络 --internal)──▶ sbx-proxy:3128 ──(sbx-egress)──▶ 上游代理 / 直连
             HTTPS_PROXY=http://<ws>.<task>:<token>@proxy:3128
```

- **隔离**：每个 Task 一个 internal 网络。sbx-proxy 接入每个 Task 网络，网络别名是 `proxy`。不同 Task 之间没有共同网络，互相访问不到。
- **身份**：代理用 basic 认证，用户名是 task-id，密码是随机 token。token 存在 `proxy.cred`（0600），squid 的 `passwd` 里存它的 apr1 哈希。冒用其他 Task 的身份会返回 407。
- **白名单**：内置名单（Anthropic、OpenAI、GitHub）+ Profile 名单（npm、pypi、Go 代理等）+ 配置里的 `network.allow`。另有策略拦截表，目前只有 `mcp-proxy.anthropic.com`（ADR 0015），拦截先于放行生效。
- **squid 配置**：主配置 `include` 每个 Task 的片段，最后 `deny all`；配置了上游代理时，用 `cache_peer` 把流量转给它。每个 Task 的片段如下：

  ```
  acl u_<id> proxy_auth <task-id>
  http_access deny  u_<id> blk_<id>                   # 策略拦截
  http_access allow u_<id> CONNECT SSL_ports a_<id>   # 白名单
  http_access allow u_<id> a_<id>
  ```

- **代理生命周期**：

  | 操作 | 时机 | 做什么 |
  |---|---|---|
  | `EnsureShared` | 每次 run | 代理容器不存在就创建，停止了就启动；等待就绪；access.log 超过 20MB 就轮转 |
  | `AttachTask` | 每次 run | 写入片段、名单和密码；把代理接入 Task 网络；热加载 |
  | `DetachTask` | done | 反向操作 |
  | `StopIfIdle` | stop、done | 没有运行中的 Task 时停掉代理 |

  - 热加载先用 `squid -k parse` 检查配置，通过后再 `reconfigure`。
  - 修改代理配置时用文件锁串行化，多个 sbx 进程同时操作不会冲突。

---

## 6. 状态：人怎么掌握

**做法**：Agent 的状态通过 hooks 写到宿主机上的文件里，sbx 读这些文件和 git 信息汇总给人看。claude 在沙箱里记下的项目记忆可以导回宿主机。

### 6.1 Agent 状态

`settings.sbx.json` 注册了 6 个 hooks，都调用 `status.sh <state>`。脚本原子写入 `/sbx/state/status.json`，也就是宿主机上的 `state/<ws>/<task>/status.json`，并追加一行到 `events.log`。

| 事件 | state |
|---|---|
| UserPromptSubmit、PreToolUse | running |
| SessionStart、Stop、Notification | idle |
| SessionEnd | exited |

### 6.2 `sbx ls`

| 列 | 来源 |
|---|---|
| STATUS | 容器在运行时，取 `status.json` 里的 running 或 idle（还没有就显示 starting）；`SessionEnd` 写过 exited 的显示 **exited(agent)**，表示容器还在但 claude 已经退出。<br>容器已停止时：退出码 0、137、143 显示 stopped；OOM 显示 exited(oom)；其他显示 exited(n)。<br>容器不存在显示 absent。 |
| AHEAD / DIFF | `git rev-list --count base..分支` 和 `git diff --shortstat`，DIFF 显示成 `3f +10 -2` 的形式 |
| LAST-ACTIVE | `status.json` 的时间 |
| PATH | worktree 路径 |

### 6.3 项目记忆（ADR 0016）

claude 的自动记忆在宿主机上存于 `~/.claude/projects/<key>/memory/`，key 是仓库路径中非字母数字的字符换成 `-`。沙箱里对应的目录在 `sbx-home` 里，两边不共享。

| 方向 | 时机 | 方式 |
|---|---|---|
| 宿主机 → 沙箱 | 每次启动 Agent 前，自动 | tar 经 `docker exec` 写入容器 |
| 沙箱 → 宿主机 | `sbx memory pull`，手动 | 逐个文件展示 diff，确认后写入；`done` 时如果有没导回的会提醒 |

两个方向都用三方比较，以上次同步的 hash 为基准：

- 只有一边改了：同步过去；
- `MEMORY.md` 两边都改了：按行合并；
- 其他文件两边都改了：标记为冲突，不覆盖；
- 任何情况下都不删除文件。

---

## 7. 生命周期：把四个方面串起来

### 7.1 总览

```
           run（新建）            stop              run（恢复）
  (无) ───────────────▶ 运行中 ─────────▶ 已停止 ─────────────▶ 运行中
                          │                  │
                          └──── done ────────┴──▶ (无，只留分支)
```

每个操作对四个方面的影响：

| 操作 | ① 代码 | ② 运行 | ③ 网络 | ④ 状态 |
|---|---|---|---|---|
| run 新建 | 建 worktree 和分支 | 确保镜像；建依赖 volume 和容器；启动 Agent | 确保代理；建 Task 网络；接入代理 | 写 meta 和生成文件；导入记忆 |
| stop | 不变 | 停容器 | 没有运行中的 Task 时停代理 | 不变 |
| run 恢复 | 不变 | 启动容器；启动 Agent | 启动代理；重新接入（token 不变） | 重新生成文件；导入记忆 |
| done | 删 worktree，留分支 | 删容器和依赖 volume | 摘除；删网络；没有运行中的 Task 时停代理 | 删 state；提醒导回记忆 |

### 7.2 run 新建

按下面的顺序建立资源，最后启动 Agent。容器要放在最后一步创建，因为它需要代理地址和所有挂载源都已就绪。每一步新建的资源都登记到回滚栈。任何一步失败，就逆序清理本次新建的资源，worktree 保留，并在报错里说明失败在哪一步。

1. **worktree**（§3）
2. **镜像**：hash 命中就跳过，否则构建（§4.1）
3. **state 目录**
4. **代理**：`EnsureShared`，创建 internal 网络，`AttachTask`，拿到代理地址（§5）
5. **volume**：共享 volume 第一次创建时 chown 成 agent 用户；每个依赖遮盖目录建一个 volume
6. **生成文件**：hooks、settings、CLAUDE.md 快照
7. **容器**：`docker run -d`，带上挂载表（§4.2）、代理地址、git 身份和资源上限
8. **meta.json**：在启动 Agent 之前写，这样 Agent 启动失败时还能走恢复流程
9. **启动 Agent**（§7.3）

### 7.3 启动 Agent（新建和恢复共用）

1. tmux 会话已经存在，并且 claude 还在跑（`status.json` 不是 exited）：直接返回。
2. 预置脚本（以 agent 用户执行，幂等）：
   - 把"已完成引导""信任此目录""跳过危险模式确认"写进 claude 配置；
   - 把 `~/.claude/{CLAUDE.md,skills,agents,commands}` 软链接到只读挂载的目录。
3. 导入项目记忆（§6.3）。失败只警告，不中断。
4. `claude auth status`：没登录就打印登录命令后退出。命令里的代理参数取自 `network.upstream`。
5. 在 tmux 里启动 claude。会话不存在就 `new-session`，会话还在但 claude 已退出就 `respawn-window -k`（复用同一个窗口，已经 attach 的人不用重进）。
   - 实际跑的是 `claude … ; printf '[sbx] claude 已退出…'; exec bash -l`。**claude 退出后窗口里留一个 login shell**，否则窗口关闭会连带结束 tmux 会话，Task 就再也 attach 不回去（§7.6）。
   - 这个 Task 以前跑过 claude（`events.log` 存在）时默认带 `--continue`，接上上次对话；`sbx run --fresh` 可以开新的一段。
6. 冒烟检查，最多 30 秒：
   - 画面上出现已知对话框：报错，附上最后 20 行画面。这说明预置字段可能随 claude 版本变了。
   - `status.json` 变成 exited，或者画面出现退出提示：报错，说明 claude 启动后立刻退出了。
   - `status.json` 出现 `SessionStart`：成功。

### 7.4 run 恢复

容器已经存在时走这条路径：

1. 读 `meta.json`。
2. 确保代理在运行，用原来的 token 重新接入。代理被重建过也能恢复；配置里的白名单改动这时生效。
3. 重新生成 gen 文件。
4. 容器停止了就启动。
5. 启动 Agent（§7.3）。

整个过程约 3 秒。

### 7.5 其他命令

| 命令 | 做什么 |
|---|---|
| `attach <task>` | `docker exec -it` 进入 tmux 会话；按 `Ctrl-b` 松手再按 `d` 离开，Agent 继续运行。claude 已退出时会先提示一句，进去看到的是 shell |
| `shell <task>` | 在容器里开一个 bash，工作目录是 worktree |
| `path [task]` | 打印工作目录 |
| `stop <task>...` | `docker stop -t 10`；之后 `StopIfIdle` |
| `done <task>...` | 1. 提醒导回记忆<br>2. 检查 worktree 是否有未提交的改动<br>3. 删容器，从代理摘除，删网络，删依赖 volume<br>4. 删 worktree 和 state<br>5. 提示合并命令<br>共享 volume 不删，所以登录态、缓存和记忆都保留 |

### 7.6 claude 退出和会话保活

交互模式下，`Ctrl-D` 和 `/exit` 是 **claude 自己的退出**，不是 tmux 的 detach（detach 是 `Ctrl-b` 松手再按 `d`）。两者很容易混淆，所以 claude 退出必须是可恢复的：

| 层 | 做法 |
|---|---|
| tmux 窗口 | 窗口跑的是 `claude … ; printf 提示 ; exec bash -l`。claude 退出后窗口里换成一个 login shell，**窗口不关、会话不死**。如果直接把 claude 当窗口命令，它一退窗口就关，最后一个窗口关掉会话就结束，`attach` 再也进不去 |
| 状态 | `SessionEnd` hook 把 `status.json` 写成 `exited`，`sbx ls` 显示 `exited(agent)`。否则状态会一直停在最后一次写下的 idle，看上去像还在工作 |
| 恢复 | `sbx run <task>` 发现会话还在但 claude 已退出，就 `respawn-window -k` 在同一个窗口里重开，默认带 `--continue` 接上这个 Task 的上次对话（`--fresh` 开新的一段） |
| attach | 会话还在就照常进去；claude 已退出时先提示一句，进去看到的是 shell，可以直接在 worktree 里跑 git |

---

## 8. 回到开头：安全边界

| 开头的问题 | 措施 | 验证 |
|---|---|---|
| 改乱代码目录 | 在 worktree 和独立分支里改，合并由人决定 | e2e |
| 读到密钥、弄脏宿主机 | 只挂载 worktree、`.git` 和只读的 claude 配置；以非 root 运行；`node_modules` 放进 volume | e2e |
| 随意访问网络 | internal 网络，直连失败；白名单代理；拦截云端 MCP | 隔离矩阵、e2e |
| Task 之间互相干扰 | 网络互不相通；代理身份不能冒用（407） | 隔离矩阵、e2e |
| 资源耗尽 | 每个 Task 2 CPU、3g 内存、1024 个进程 | 手动 |

**残余风险**（已接受）：

- 可以通过白名单里的域名外传数据，例如 github.com。
- 主仓库的 `.git` 是可写的。
- `sbx-home` 里的登录凭据所有 Task 都能读到。

---

## 9. 支撑部分

### 9.1 配置

M1 只有两层：内置默认值，加 `~/.sbx/config.toml`。未知字段只警告，不报错。

```toml
profile = "web-go"
max_running = 3                                   # M1 尚未检查
[network]
proxy = "shared"                                  # M1 只支持 shared
mode = "allowlist"
upstream = "http://host.docker.internal:7890"     # 只支持 http；空 = 直连
cloud_mcp = false
allow = []                                        # 追加到白名单
[resources]
cpus = 2
memory = "3g"
pids = 1024
[deps]
mask = ["node_modules"]
[agents.claude]
version = "latest"
```

### 9.2 登录与升级

- **登录**：所有 Task 共用 `sbx-home` 里的登录态，只需登录一次。M1 要手动执行下面的命令，M2 会做成 `sbx login`：

  ```
  docker run -it --rm -e HOME=/home/agent -v sbx-home:/home/agent \
    [-e HTTPS_PROXY=<upstream>] sbx/web-go:<hash> claude auth login
  ```

- **升级**：容器里的 claude 不会自更新。`sbx upgrade` 的过程：
  1. 用 npm 查询最新版本，记到 `~/.sbx/claude-version`；
  2. 重建 Agent 层。

  只有新建的 Task 会用新版本，已有 Task 要 done 后重新 run。

### 9.3 代码结构

```
core/
├─ cmd/sbx/          入口
├─ assets/           go:embed：Dockerfile、entrypoint、hooks、白名单、squid 模板
└─ internal/
    ├─ cli/          命令；把下面各包串成 §7 的流程
    ├─ workspace/    ① git：解析 ws、worktree 增删、dirty 检查
    ├─ image/        ② 镜像 hash 与构建
    ├─ agent/        ② 挂载、环境变量、预置、tmux、冒烟检查
    ├─ proxy/        ③ squid 渲染、apr1、代理生命周期
    ├─ task/         ④ 命名、state、状态推导
    ├─ memory/       ④ 记忆三方比较、tar 进出容器
    ├─ config/       配置
    ├─ docker/       docker CLI 封装；RunSpec → 参数（纯函数）
    └─ fsutil/       原子写
```

依赖方向：`cli` → 各业务包 → `docker` / `fsutil`。命令参数和配置文件的渲染都写成纯函数，用 golden 文件测试。

---

## 10. 现状

### 10.1 测试

| 层 | 命令 | 覆盖 |
|---|---|---|
| 单元 + golden | `make test` | 命名、配置、状态推导、docker 参数、squid 渲染、apr1、预置幂等、记忆比较 |
| Docker 集成 | `make test-docker` | 镜像内容；代理隔离矩阵（200/403/407）、热加载、日志轮转 |
| 端到端 | `make e2e` | 两个并行 Task 由真实的 claude 各提交一次，覆盖 §7 全流程和 §8 的检查，共 32 项 |
| 真实仓库 | 手动 | 交互模式已通过；无人值守（离开 30 分钟以上）待做 |

### 10.2 还没做

| 项 | 计划 |
|---|---|
| 项目级配置 `.sbx/sandbox.toml` 和信任确认 | M2-1～5、M2-9 |
| `sbx login` | M2-12 |
| 查看和放行被拒请求：`sbx net denied/allow` | M2-10、M2-11 |
| `max_running` 和内存预算检查 | M2-14、M2-15 |
| open 模式、dedicated 代理、API key | M2 |
| Codex、`port`、`doctor`、通知 | M3 |

已知现象：claude 画面会提示 `Remote managed settings failed to load (401)`，暂不影响使用，继续观察。
