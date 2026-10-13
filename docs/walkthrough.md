# sbx 走一遍：从第一次使用到合并分支

> 本文用一个具体场景，从头到尾按顺序讲一遍：每条命令敲下去之后，sbx、Docker、git、squid、claude 分别做了什么，留下了什么。
> 讲的是 M1 代码的实际行为。想先了解 sbx 是什么、为什么这么设计，见[项目主页](https://zzjha-cn.github.io/agent_sandbox/)；命令和参数的速查见 [commands.md](commands.md)，结构化的说明见 [architecture.md](architecture.md)，设计取舍见 [design.md](design.md) 和 `private/adr/`（未纳入版本控制）。

## 场景

| 设定 | 值 |
|---|---|
| 你的仓库 | `/Users/apple/code/shop`，前端 Next.js、后端 Go，当前在 `main` 分支 |
| 要做的事 | 两个互不相干的需求并行做：`fix-login`（修登录 bug）和 `add-search`（加搜索） |
| 网络 | 本机开着代理 `127.0.0.1:7890`，在容器里访问它的地址是 `host.docker.internal:7890` |
| 由此算出的名字 | Workspace id = `shop-e76272`（目录名 + 仓库根路径 sha1 的前 6 位）<br>记忆 key = `-Users-apple-code-shop` |

全文按时间顺序分 15 幕：前 12 幕是日常回路，后 3 幕是「项目跑起来还缺点什么」——连数据库、装私有依赖、够内网。每一幕先给命令，再按顺序说明发生了什么。

---

## 第 0 幕：准备

```bash
cd ~/workspace/.../agent_sandbox/core && make build    # 得到 bin/sbx，把它放进 PATH
cat > ~/.sbx/config.toml <<'EOF'
[network]
upstream = "http://host.docker.internal:7890"
mode = "allowlist"      # 全文按白名单模式讲；2026-10-08 起默认是 open（不拦截）
EOF
```

- 构建出的 `sbx` 是一个独立的二进制。Dockerfile、白名单、squid 模板都通过 `go:embed` 打包在里面，运行时不需要源码目录。
- 配置文件只写了上游代理，其他项都用默认值：
  - 镜像：`web-go`
  - 资源：2 CPU、3g 内存
  - 网络：这里显式写了 allowlist；不写的话默认是 `open`，下面讲到的拦截都不会发生
  - 依赖遮盖：`node_modules`
  - claude 版本：`latest`

---

## 第 1 幕：第一次 `sbx run fix-login`，卡在登录

```bash
cd ~/code/shop
sbx run fix-login
```

**① 认出仓库。** sbx 执行 `git rev-parse --git-common-dir`，得到主仓库 `/Users/apple/code/shop`，算出 ws id `shop-e76272`。之后所有名字都由 ws id 和 Task 名拼出来：

- 容器：`sbx-shop-e76272-fix-login`
- 网络：`sbx-shop-e76272-fix-login-net`
- 分支：`sbx/fix-login`
- 代理用户名（task-id）：`shop-e76272.fix-login`

**② 判断新建还是恢复。** `docker inspect sbx-shop-e76272-fix-login` 显示容器不存在，所以走新建流程。从这里开始，每新建一样东西，就往回滚栈里登记一个对应的清理动作。

**③ 建 worktree。**

```
git worktree add -b sbx/fix-login ~/.sbx/worktrees/shop-e76272/fix-login <HEAD 的 sha>
```

- 终端输出：`worktree：/Users/apple/.sbx/worktrees/shop-e76272/fix-login（分支 sbx/fix-login）`。
- 这个 HEAD 的 sha 会作为 base 记下来，之后 `sbx ls` 里的"领先几个提交""改了多少行"都相对它计算。
- worktree 里的 `.git` 是一个文件，内容是 `gitdir: /Users/apple/code/shop/.git/worktrees/fix-login`。第 5 幕会用到这一点。

**④ 准备镜像。** sbx 根据两个 Dockerfile、entrypoint、claude 版本、你的 UID/GID 算出 hash，得到 `sbx/web-go:<hash12>`。第一次使用时镜像不存在，于是构建两层：

1. `sbx/profile-web-go:<hash>`：Debian，加 Go、Node 24、pnpm、python3。
2. `sbx/web-go:<hash>`：在上一层之上装 tini、tmux、git、jq、claude-code，并创建 `agent` 用户（UID 和你的一样，挂进去的文件属主才对得上），设置 `DISABLE_AUTOUPDATER=1`。

构建时，`HTTPS_PROXY=http://host.docker.internal:7890` 通过 build-arg 传入。失败会自动重试一次。整个过程几分钟，之后再用直接命中缓存。

> **想用自己的环境？** 上面这两层里，**只有第 1 层（Profile）是可以换掉的**，第 2 层（Agent 层）永远由 sbx 叠加——claude、tmux、mise 和 `agent` 用户都在那一层，不用你操心。换掉第 1 层有两种写法，优先级高于 `profile`：
>
> ```toml
> image = "ghcr.io/me/devbox:2026-10"   # 配置里写；直接拿这个镜像当底，不构建 Profile 层
> ```
> ```bash
> <repo>/.sbx/Dockerfile                # 或者仓库里放一份 Dockerfile，不用写任何配置
> ```
>
> 两个坑：底**必须是 Debian / Ubuntu 系**（Agent 层用 `apt-get`，Alpine 会在构建时挂掉）；另外 `[deps].mask` 的默认值是按 `profile` 字段查的，**换镜像不会连带换掉它**——只写 `image` 的话，`profile` 还是默认的 `web-go`，于是 Rust 镜像也会继承 `node_modules` 和 `.next`，而且 mask 取并集、只能加不能删。所以换镜像时记得把 `profile` 一并设成最接近的内置值（`py-rust` 给的是 `.venv` + `target`）。完整说明见 [commands.md](commands.md)。

**⑤ 写 state 目录。** 创建 `~/.sbx/state/shop-e76272/fix-login/gen/`。

**⑥ 启动共享代理（`Ensure`）。**

1. 渲染 `~/.sbx/proxy/squid.conf`，其中包括上游代理那行 `cache_peer host.docker.internal parent 7890 …`。
2. 写入占位片段 `tasks/00-empty.conf`。
3. 创建网络 `sbx-egress`。这是普通 bridge 网络，可以出网。
4. 启动容器 `sbx-proxy`，镜像是 `ubuntu/squid`，内存上限 128m。整个 `~/.sbx/proxy` 目录只读挂到容器的 `/etc/sbx`。
5. 反复执行 `squid -k check`，直到 squid 就绪。

**⑦ 创建 Task 网络，接入代理。**

1. `docker network create --internal sbx-shop-e76272-fix-login-net`。`--internal` 表示这个网络没有到外网的路由。
2. `AttachTask`：
   - 生成 32 字节随机 token，写入 `state/.../proxy.cred`（权限 0600）；
   - 写白名单 `allow/shop-e76272.fix-login.txt`，由内置、web-go 和配置里的域名合并而成；
   - 写拦截表 `block/…txt`，内容是 `mcp-proxy.anthropic.com`；
   - 写片段 `tasks/shop-e76272.fix-login.conf`；
   - 把 token 的 apr1 哈希写进 `passwd`；
   - `docker network connect --alias proxy` 把 sbx-proxy 接入 Task 网络。从此在 Task 网络里，`proxy` 这个名字就指向它。
3. 执行 `squid -k parse`，通过后执行 `squid -k reconfigure` 热加载。

**⑧ 准备 volume。**

- `sbx-home` 和 `sbx-cache` 是第一次创建。sbx 用一次性 root 容器把它们 chown 成你的 UID，并在 `sbx-cache` 里建好 npm、pip、go 等缓存子目录。
- 依赖 volume `sbx-shop-e76272-fix-login-dep-0`：用来遮住 worktree 里的 `node_modules`。

**⑨ 渲染生成文件，然后创建容器。**

1. gen 目录里写入三样东西：
   - `hooks/status.sh`
   - `settings.sbx.json`：5 个 hooks
   - `host-claude/CLAUDE.md`：你宿主机 `~/.claude/CLAUDE.md` 的快照
2. `docker run -d`，挂载 worktree、主仓库 `.git`、依赖 volume、`sbx-home`、`sbx-cache`、state、gen（只读），以及只读的 `~/.claude/skills`、`agents`、`commands`。
3. 设置环境变量 `HTTPS_PROXY=http://shop-e76272.fix-login:<token>@proxy:3128` 和 git 作者信息。git 作者信息取自你仓库的 `git config`。
4. 容器的主进程是 `tini → sleep infinity`。claude 还没有启动。
5. 写入 `meta.json`。

**⑩ 启动 Agent，发现没登录。**

1. 预置脚本以 agent 用户在容器里跑一遍：
   - 在 `~/.claude.json` 里把"已完成引导""信任这个目录"设为 true；
   - 把 `~/.claude/skills` 等软链接到只读挂载的目录。
2. 宿主机这个仓库的 Claude 记忆被导入沙箱，终端输出：`导入项目记忆：add 3`。
3. 执行 `claude auth status`，返回 `loggedIn: false`。sbx 打印登录命令后报错。

**⑪ 回滚。** 回滚栈按登记的逆序执行：

1. 删 `meta.json`
2. 删容器
3. 删依赖 volume
4. 从 squid 去掉这个 Task 的片段、passwd 条目和网络连接；现在没有别的 Task，sbx-proxy 被停掉
5. 删 Task 网络

以下内容不回滚：

- worktree：留着，以免丢失你可能已经放进去的东西；
- 镜像，以及 `sbx-home`、`sbx-cache`：都是共享资源。

终端提示：`在「启动 Agent」这一步失败，清理本次新建的资源（worktree 保留）…`

---

## 第 2 幕：登录（只需一次）

```bash
docker run -it --rm -e HOME=/home/agent -v sbx-home:/home/agent \
  -e HTTPS_PROXY=http://host.docker.internal:7890 sbx/web-go:<hash> claude auth login
```

- 这条命令就是上一幕 sbx 打印出来的。代理地址取自配置里的 `network.upstream`，没有配置时这一行省略。
- 你在浏览器里完成授权，粘贴授权码。凭据写进 `sbx-home` 这个 volume。
- 之后所有 Task 都挂载同一个 `sbx-home`，所以只需要登录一次。

---

## 第 3 幕：再次 `sbx run fix-login`，进入 Agent

```bash
sbx run fix-login
```

1. 容器不存在，仍然走新建流程。
2. 到 ③ 时 worktree 已经存在，直接复用；base 用分支和 HEAD 的分叉点。
3. 到 ④ 时镜像命中缓存，不需要构建。
4. 之后同第 1 幕，一路到 ⑩，这次 `claude auth status` 返回已登录。
5. 删掉旧的 `status.json`，然后在容器里执行：
   ```
   tmux new-session -d -s agent -x 220 -y 50 \
     'claude --dangerously-skip-permissions --settings /sbx/gen/settings.sbx.json; \
      printf "\n[sbx] claude 已退出…\n"; exec bash -l'
   ```
   工作目录是 worktree 路径，和宿主机上的路径完全一样。
   后面那半截是会话保活：claude 退出后窗口里换成一个 shell，窗口不关，tmux 会话也就不会跟着没掉（第 12 幕）。
6. 冒烟检查最多 30 秒。期间轮询两件事：
   - tmux 画面：如果出现"select login method""trust this folder"之类的对话框，说明预置字段随 claude 版本变了，立即报错，并附上画面的最后 20 行；
   - `status.json`：claude 启动时触发 `SessionStart` hook，`status.sh` 写入 `{"state":"idle","event":"SessionStart",...}`。sbx 读到这一条，就认为启动成功；读到 `exited`（`SessionEnd`）则说明 claude 刚起来就退了，立即报错。
7. 没有加 `--detach`，所以 sbx 执行 `docker exec -it -u agent … tmux attach -t agent`。你看到的就是 claude 的界面。

从敲命令到看到界面大约 20 秒。你输入需求：*"登录页输错密码后没有提示，修一下并加测试，完成后提交。"*

离开 tmux：按 `Ctrl-b`，松手，再按 `d`。claude 继续在容器里运行。

注意 `Ctrl-D` 不是离开，是**退出 claude**（和 `/exit` 一样）。按错了也不要紧，见第 12 幕。

---

## 第 4 幕：并行开第二个 Task

```bash
sbx run add-search --detach
sbx attach add-search      # 输入需求后 Ctrl-b d 离开
```

过程和第 3 幕相同，区别在于以下资源都是新的：

- worktree：`~/.sbx/worktrees/shop-e76272/add-search`，分支 `sbx/add-search`；
- 网络：`sbx-shop-e76272-add-search-net`；
- token 和 squid 片段：用户名是 `shop-e76272.add-search`；
- 依赖 volume。

共享的只有：

- sbx-proxy：已经在运行，主配置没有变化，不会重启；
- `sbx-home`：登录态、claude 设置、项目记忆；
- `sbx-cache`：npm、go 的下载缓存；
- 镜像。

此时 sbx-proxy 同时接在两个 Task 网络上，而两个 agent 容器之间没有任何共同网络。add-search 访问不到 fix-login 起的开发服务器，反过来也一样。

---

## 第 5 幕：Agent 干活时，底下发生了什么

以 fix-login 为例。

**收到需求时。** claude 触发 `UserPromptSubmit` hook，每次调用工具前还会触发 `PreToolUse` hook。`status.sh running` 每次都把 `status.json` 改写成 `running`，并往 `events.log` 追加一行。

**`npm install`。**

1. npm 读到 `HTTPS_PROXY`，向 `proxy:3128` 发起 `CONNECT registry.npmjs.org:443`，带上 basic 认证（用户名 `shop-e76272.fix-login`，密码 token）。
2. squid 用 `passwd` 校验认证，然后匹配到这个 Task 的片段：
   - 先查拦截表，不命中；
   - 再查白名单，命中 `registry.npmjs.org`，放行。
3. 因为配置了 `never_direct`，squid 通过 `cache_peer` 把连接转给 `host.docker.internal:7890`，由你的本机代理出网。
4. 下载的包缓存到 `/sbx/cache/npm`，也就是 `sbx-cache`。add-search 之后安装相同的包会更快。
5. `node_modules` 写进依赖 volume，宿主机 worktree 里只有一个空的挂载点。宿主机的 node（macOS 版本）和容器里的 node（Linux 版本）不会互相污染。
6. access.log 记一行：
   ```
   1759750000.123 shop-e76272.fix-login 172.20.0.3 TCP_TUNNEL/200 CONNECT registry.npmjs.org:443 FIRSTUP_PARENT/…
   ```

**访问白名单外的域名。** 假设测试代码里请求了 `api.some-captcha.com`：

- squid 没有匹配到任何 allow 规则，落到 `http_access deny all`，返回 403。claude 看到的是连接被拒绝。
- access.log 记一条 `TCP_DENIED/403 … api.some-captcha.com:443`。
- 如果 claude 想绕过代理直连，会直接失败，因为 internal 网络没有出网路由。

M1 还没有 `sbx net denied`，只能用下面的命令查看被拒的请求：

```bash
docker exec sbx-proxy grep TCP_DENIED /var/log/squid/access.log
```

要放行某个域名，`sbx net allow api.some-captcha.com` 一条命令就行：写进 `config.toml` 的 `network.allow`，并对运行中的 Task 热加载，不用重启。

**提交。** claude 执行 `git commit`：

1. git 读 worktree 里的 `.git` 文件，找到 `/Users/apple/code/shop/.git/worktrees/fix-login`。
2. 容器在同一个路径挂载了主仓库的 `.git`，所以新的对象和 `refs/heads/sbx/fix-login` 直接写进你的主仓库。
3. 你在宿主机上执行 `git log sbx/fix-login`，马上能看到这个提交，不需要任何同步。
4. 作者是你的名字和邮箱，来自环境变量。

> **第 2 点是有代价的：这个 `.git` 是可写挂载的，它同时是一条逃逸路径。** 不可写就没有第 3 点——沙箱里的提交要出现在你的仓库里，就得能写你的 `.git`。但能写 `.git` 也就能写 `.git/hooks/*` 和 `.git/config`，而 git hook 会在你下次碰这个仓库时**以你的身份、在宿主机上**执行。这是 design.md 里的风险 **R1**，明知而接受，至今没有缓解。跑真正不可信的代码时，请用一个用完就扔的 clone。

**完成时。** claude 停下来等待输入，触发 `Stop` hook，`status.json` 变成 `idle`。

---

## 第 6 幕：离开半小时后回来

```bash
sbx ls
```

```
workspace shop-e76272 (/Users/apple/code/shop)
TASK        STATUS  BRANCH          AHEAD  DIFF        LAST-ACTIVE  PATH
add-search  running sbx/add-search  0      0           8s ago       ~/.sbx/worktrees/shop-e76272/add-search
fix-login   idle    sbx/fix-login   2      4f +86 -7   21m ago      ~/.sbx/worktrees/shop-e76272/fix-login
```

每个 Task 的每一列是这样算出来的：

| 列 | 怎么算 |
|---|---|
| STATUS | 先 `docker inspect` 看容器是否在运行。在运行就读 `status.json` 里的 state（claude 已退出时是 `exited(agent)`）；容器已停止就看退出码，137 和 143 显示 stopped，OOM 显示 exited(oom) |
| AHEAD | `git rev-list --count <base>..sbx/fix-login` |
| DIFF | `git diff --shortstat <base>...sbx/fix-login`，压缩成"文件数 +新增行 -删除行" |
| LAST-ACTIVE | `status.json` 里的时间戳 |

所以从这张表能看出：fix-login 已经完成，做了 2 个提交；add-search 还在干活。

查看 fix-login 的改动：

```bash
git log -p main..sbx/fix-login          # 在主仓库就能看，提交已经在这里
code "$(sbx path fix-login)"            # 打开它的 worktree
```

改动在 worktree 里，不在 `~/code/shop`。这是有意的：两个 Task 各自在自己的目录里改，互不覆盖，合并分支后才回到仓库目录。

想让它再补点东西，就 `sbx attach fix-login` 继续对话。想在容器里自己跑个命令，用 `sbx shell fix-login`，它会在容器里开一个 bash，工作目录是 worktree。

---

## 第 7 幕：下班，停掉

```bash
sbx stop fix-login add-search
```

1. 对每个 Task 执行 `docker stop -t 10`。容器里的所有进程都会结束，包括 tmux 和其中的 claude；10 秒内没退出的会被强杀。
2. 两个 Task 都停了以后，`StopIfIdle` 发现没有运行中的 agent 容器，就把 sbx-proxy 也停掉，但不删除。
3. 以下内容全部保留：
   - 容器（已停止）
   - 网络、worktree、依赖 volume
   - token 和 squid 片段
   - claude 的会话历史（在 `sbx-home`）
4. 终端输出 `已停止 …`，最后输出 `没有运行中的 Task 了，已停止 sbx-proxy`。
5. `sbx ls` 里这两个 Task 显示 `stopped`。

---

## 第 8 幕：第二天恢复

```bash
sbx run fix-login
```

容器已经存在，所以走恢复流程：

1. 读 `meta.json`，取得 task-id。
2. `Ensure`：sbx-proxy 已停止，执行 `docker start`。squid 的启动命令会先删掉残留的 PID 文件，所以即使昨天它是被强杀的，也能正常启动。如果 access.log 超过 20MB，顺便轮转一次。
3. `AttachTask`：用 `proxy.cred` 里原来的 token 重新写入片段和白名单。昨天改过 `network.allow` 的话，这时生效。
4. 重新渲染 gen 目录。宿主机 `CLAUDE.md` 的改动这时同步进来。
5. `docker start` 启动容器。
6. 启动 Agent：预置、导入记忆（如果宿主机又多了记忆）、检查登录、在 tmux 里启动 claude、冒烟检查。

大约 3 秒后进入 claude。它是新会话，昨天的对话历史还在 `sbx-home` 里，可以用 claude 自己的 `/resume` 找回。

---

## 第 9 幕：claude 在沙箱里记下的东西

做 fix-login 的过程中，claude 记下了一条项目记忆，例如"测试要用 `pnpm test:unit`，不是 `pnpm test`"。它写进的是容器里的 `/home/agent/.claude/projects/-Users-apple-code-shop/memory/`，也就是 `sbx-home`，不会自动回到宿主机。

```bash
sbx memory pull
```

1. 优先找本 ws 里正在运行的 Task 容器，用 `tar` 把沙箱里的记忆目录读出来。没有运行中的容器，就起一个一次性容器，挂上 `sbx-home` 来读。
2. 读宿主机的 `~/.claude/projects/-Users-apple-code-shop/memory/`，以及上次同步时记下的基准 `~/.sbx/memory/-Users-apple-code-shop.json`。
3. 三方比较：
   - 沙箱里新增的文件：`add`；
   - 只在沙箱里改过的文件：`update`；
   - 两边都给 `MEMORY.md` 加了条目：`merge`，按行合并；
   - 两边都改了同一个文件：`conflict`，只展示差异，不覆盖。
4. 每个要写入的文件都先展示一段 `diff -u`，你输入 `y` 后才写入宿主机，并更新基准。

反过来，宿主机上的新记忆不需要手动处理：下次 `sbx run` 时会自动导入沙箱。两个方向都不会删除文件。

---

## 第 10 幕：收尾，合并分支

```bash
sbx done fix-login
git merge sbx/fix-login
```

`sbx done` 按顺序做这些事：

1. **提醒导回记忆。** 先计算一遍沙箱到宿主机的记忆差异。如果第 9 幕忘了 pull，这里会提示：`沙箱里有 1 个项目记忆文件还没导回宿主机…`。记忆在 `sbx-home` 里，`done` 不会删除，之后补 pull 也来得及。
2. **检查未提交的改动。** 在 worktree 里执行 `git status`。有未提交的改动就拒绝执行，并列出这些改动；加 `--force` 才会丢弃。
3. **删容器：** `docker rm -f`。
4. **从代理摘除：** 删除片段、白名单、拦截表和 passwd 中的这一条，热加载 squid，再把 sbx-proxy 从这个 Task 的网络断开。
5. **删 Task 网络。**
6. **删依赖 volume：** 按标签 `sbx.task=fix-login,sbx.kind=dep` 查找后删除。
7. **删 worktree：** `git worktree remove`。
8. **删 state 目录**，里面有 meta、status、token、gen。
9. **提示：** `已结束 fix-login，分支 sbx/fix-login 保留。合并：git merge sbx/fix-login；删除分支：git branch -D sbx/fix-login`
10. **检查代理是否还在用：** add-search 还在运行，所以 sbx-proxy 不停。

合并之后，改动才回到 `~/code/shop` 这个目录。add-search 做完后同样处理。最后一个 Task `done` 掉时，sbx-proxy 会被停掉。

---

## 第 11 幕：升级沙箱里的 claude

过了几周，claude-code 发布了新版本。容器里的 claude 不会自己升级：npm 全局目录归 root 所有，镜像里也关掉了自动更新。

```bash
sbx upgrade
```

1. 在 Profile 镜像里执行 `npm view @anthropic-ai/claude-code version`，走上游代理，得到例如 `2.2.0`。
2. 新版本参与镜像 hash 的计算，所以 tag 变了。只重建 Agent 层；Profile 层命中缓存。
3. 把 `2.2.0` 写进 `~/.sbx/claude-version`。之后新建的 Task 都按这个版本取镜像。
4. 已有的 Task 不受影响：容器的镜像不能原地替换，要 `sbx done` 后重新 `run` 才会用上新版本。

如果你在配置里固定了 `agents.claude.version`，`upgrade` 会拒绝执行，提示你改配置。

---

## 第 12 幕：手滑按了 Ctrl-D

你本来想离开，按成了 `Ctrl-D`。claude 收到 EOF，退出了。

屏幕上出现：

```
[sbx] claude 已退出。Ctrl-b d 离开容器；sbx run 可以在这个会话里重新拉起。

agent@sbx:/Users/apple/.sbx/worktrees/shop-e76272/fix-login$
```

发生了什么：

1. claude 退出，但 tmux 窗口跑的命令后面还跟着 `exec bash -l`，所以窗口里换成了一个 shell，**窗口和会话都还在**。你现在可以直接在 worktree 里 `git log`、`git diff`。
2. claude 退出时触发 `SessionEnd` hook，`status.json` 写成 `{"state":"exited",...}`。

```
$ sbx ls
TASK        STATUS         BRANCH          AHEAD  DIFF        LAST-ACTIVE  PATH
fix-login   exited(agent)  sbx/fix-login   2      4f +86 -7   5s ago       ~/.sbx/worktrees/shop-e76272/fix-login
```

`exited(agent)` 的意思是：容器还在跑，但里面的 claude 已经退出了。

恢复：

```bash
sbx run fix-login
```

sbx 看到会话还在、claude 已退出，就用 `tmux respawn-window -k` 在同一个窗口里重开 claude，并且默认带上 `--continue`，接着刚才那段对话。想重新开一段用 `sbx run fix-login --fresh`。

> 这一条是 M1 验收后发现的：原来 tmux 窗口直接跑 claude，claude 一退窗口就关，会话跟着结束，`sbx attach` 报"会话不存在"，而 `sbx ls` 还显示 idle —— 看上去像是 Task 卡死了。

---

## 第 13 幕：后端测试要连数据库

第二天接了个后端的活。`sbx run api-rate-limit` 之后 agent 跑 `go test ./...`，一片红：

```
dial tcp 127.0.0.1:5432: connect: connection refused
```

**这一幕最该注意的不是报错，是不报错的那条路。** 无人值守下 agent 不会停下来问你，它会先去试 `apt-get install postgresql`（没 root，失败），再试 `docker run`（没 docker.sock，失败），然后**开始改测试代码绕过数据库**——最后给你一个「测试通过」的分支。不是报错退出，是悄悄产出垃圾。

所以得让它有东西可连。测试要的是一个空库，不是你那份真实数据。`sbx config init` 会写一份全是注释的项目层模板，这一段就在里面（`--lang zh` 给中文注释）：

```toml
# shop/.sbx/sandbox.toml —— 这一段可以进仓库
[[services]]
name  = "db"
image = "postgres:16"
port  = 5432
env   = { POSTGRES_PASSWORD = "dev", POSTGRES_DB = "shop_test" }
```

`.sbx/` 变了，所以下一次 `sbx run` 会先把 diff 摊给你看，`sbx trust` 确认过再继续（第 0 幕那套信任模型，这里照样生效）。

```console
$ sbx run api-rate-limit
service db: postgres:16, reachable in the task as db:5432
waiting for claude to be ready ...
```

发生了什么：

1. sbx 在 Task 网络（`sbx-shop-e76272-api-rate-limit-net`，`--internal`）上起了一个容器 `sbx-shop-e76272-api-rate-limit-svc-db`，网络别名是 `db`。
2. **它和 Task 的通信根本不需要出网路由**——两个容器在同一张 internal 网络上，谁都没有默认路由。这正是这一类最安全的原因：功能加上了，不变量一点没动。
3. 镜像在宿主机上拉，和容器的网络无关。

**要改一处代码**：连接地址从 `localhost:5432` 改成 `db:5432`。服务是另一个容器，而 `localhost` 在 Task 里指的是 Task 自己——这是落地时唯一的摩擦，`sbx run` 每次都会把实际地址打出来提醒你。

收尾时不用管它：

```console
$ sbx done api-rate-limit
finished api-rate-limit; branch sbx/api-rate-limit is kept. ...
```

服务容器和 Task 网络一起没了。中途 `sbx stop` 也会把它停掉——不然 Task 停了还留着一个 postgres 在那儿吃内存，而没有任何地方会提醒你。

---

## 第 14 幕：依赖在公司私有源上

新接的仓库 `go.mod` 里有 `git.corp.example/platform/sdk`。Task 里 `go mod download` 卡住：

```
dial tcp: lookup git.corp.example: no such host
```

容器只有一条经 squid 的 HTTP 出口，到不了内网，这是设计。**给沙箱开内网权限是最贵的解法**——凭据要进容器，接触面一下子铺开。

换个方向：**把「取」挪到宿主机上做，Task 只负责用。**

```console
$ sbx prep init
wrote /Users/apple/code/shop/.sbx/prep.Dockerfile
the contract is in the comments; edit it (or let the agent fill it in), then:
  sbx prep build --check   # report what would be seeded, without touching any volume
  sbx prep build
```

模板里的契约写成注释，既是给你看的，也是给 agent 看的 ground truth。填完大致是这样：

```dockerfile
FROM golang:1.23
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=netrc,target=/root/.netrc \
    GOFLAGS=-mod=mod GOMODCACHE=/sbx/cache/go-mod go mod download
```

```toml
# shop/.sbx/sandbox.toml —— 只有 secret 的 id，没有值，可以进仓库
[prep]
watch   = ["go.mod", "go.sum"]
secrets = ["netrc"]

# ~/.sbx/config.toml —— 值在这里。项目层整段 [prep.secret] 被红线挡住
[prep.secret]
netrc = "~/.sbx/secrets/corp.netrc"
```

先看一眼会发生什么，不碰任何 volume：

```console
$ sbx prep build --check
building the prep image sbx/prep-shop-e76272:a1b2c3d4e5f6 ...
seeded:   cache/go-mod 184MB
declared: (nothing)
(--check: nothing was seeded)
```

然后真构建：

```console
$ sbx prep build
built sbx/prep-shop-e76272:a1b2c3d4e5f6
new tasks will seed their dependency volumes from it; existing tasks are untouched
to reseed one anyway: sbx prep reseed <task>
```

发生了什么：

1. 构建跑在**宿主机的 Docker 上**，走默认 bridge：有完整的内网可达性，`~/.sbx/secrets/corp.netrc` 通过 BuildKit secret 挂进那一条 `RUN`。
2. **它不落进任何镜像层**——`docker history` 里看不到，镜像里也不存在。凭据只在 build 期活着，agent 永远碰不到。
3. sbx 显式把代理甩掉了。Docker Desktop 会把它设置里的代理注入 `docker build`，不管掉的话这次构建会原样重演「全走代理、到不了内网」。**两条注入路都要堵**：`~/.docker/config.json` 里的 `proxies`（CLI 自己转成 `--build-arg`，要用同名空值盖住）和进程环境里的 `HTTP_PROXY`。
4. tag 是 `hash(prep.Dockerfile + watch 列出的文件内容)`。所以**「这个 tag 的镜像不存在」就等于「漂移了或者从没构建过」**，不需要另记一份状态。

下一个 Task 起来就自带依赖：

```console
$ sbx run add-sdk
seeding cache/go-mod from sbx/prep-shop-e76272:a1b2c3d4e5f6 (184MB) ...
```

容器里 `go build ./...` 直接过，**整个过程 Task 一次内网都没连过**。

过两周有人改了 `go.sum`：

```console
$ sbx run fix-timeout
warning: the prep image sbx/prep-shop-e76272:9f8e7d6c5b4a does not exist (.sbx/prep.Dockerfile
or a watched file changed); run sbx prep build to refresh the seed. This task starts with
empty dependency volumes.
```

**默认只提醒不自动构建**：自动构建意味着每次 `sbx run` 都可能悄悄连一次内网，边界就糊了。想要别的行为就配 `on_drift = "build"` 或 `"fail"`——它是安全开关，只能写在全局层或工作区层。

---

## 第 15 幕：要连内网的 staging 库

有个活必须对着 staging 的只读副本跑。这回不是一次性的空库了，是**别人也在用的、有真实数据的实例**——级别从 🟢 跳到 🔴。

`sbx forward init` 会把模板直接写到这一层——不是写到项目层再提醒你搬走，命令分开本身就是那条规矩：

```toml
# ~/.sbx/workspaces/shop-e76272.toml —— 注意不是 .sbx/sandbox.toml
[[forward]]
name = "staging-db"
port = 5432
to   = "db.corp.internal:5432"
```

**项目层写不了这一段**，写了直接报错：

```
sbx: /Users/apple/code/shop/.sbx/sandbox.toml: the project layer contains settings that are not allowed:
  - forward.* (this whole section can only be set in the global or workspace layer)
```

理由很直接：跟着仓库走的配置不该决定你这台机器往哪儿连。`git pull` 一下就能多一条洞，这事不能成立。真正的中介点是**配置层**——每一条 forward 都是你在自己机器上做的显式决定。

```console
$ sbx run migrate-check
forward staging-db: staging-db:5432 in the task -> 10.10.16.80:5432 (internal network)
note: these forwards reach real internal services: staging-db
      whatever the agent does to them, it does for real:
      use a read-only account or a separate schema; the network layer cannot undo a DROP TABLE.
      network.mode is open, so this task has both real data and unrestricted egress.
      consider network.mode = "allowlist" for this workspace (or sbx run --net allowlist).
```

发生了什么：

1. sbx 起了一个 socat 容器 `sbx-shop-e76272-migrate-check-fwd-staging-db`，**同时接 Task 网络和 `sbx-egress`**：Task 从前者连它，它从后者出去。
2. `db.corp.internal` **在启动时解析一次，IP 固化进 socat 的目标**。好处有三：行为确定、能把实际地址打出来、避免 DNS 在 Task 跑着的时候把目标换掉。
3. **Task 容器自己还是只有一张 internal 网卡**。forwarder 是 Task 网络里的一个普通 peer，不是 Task 的网关——容器仍然没有默认路由，HTTP 的通用出网仍然只能经 squid。
4. 解析结果如果落在公网，这条 forward 会**拒绝启动**：否则它就成了绕过 squid 的通用隧道，不受白名单约束、不进访问日志、`sbx net denied` 也看不见。

那段 note 不是客套。网络层到这里已经不是护着你的那一层了——**「连上去之后能毁掉什么」只能在服务侧解决**：只读账号、独立 schema、权限模型。sbx 能做的是让这件事显式、可见、可追溯：

```console
$ sbx ls
TASK            STATUS  BRANCH                 AHEAD  DIFF       DENIED  LINKS   LAST-ACTIVE  PATH
migrate-check   idle    sbx/migrate-check      2      3f +41 -8  0       fwd:1   4m ago       ~/.sbx/worktrees/...
```

`LINKS` 里有 `fwd` 的分支，是接过真实内部服务跑出来的。**事后 review 它的改动时，你该用不一样的尺度看**——这就是「预期管理」的可操作形态，它还同时记在 `state/<ws>/<task>/meta.json` 里，不只存在于当时那一屏输出。

> 内网 HTTPS 服务可以再加一个 `host = "gitlab.corp.internal"`：sbx 给 Task 容器加一条 `--add-host` 指向 forwarder，容器里仍用真实域名访问，SNI 和证书原样透传。代价是这条流量绕过了 squid 的白名单和访问日志，所以能走 `network.allow` + squid 的就别走它。

---

## 附：资源在整个过程中的变化

| 时刻 | 容器 | 网络 | volume | 宿主机文件 |
|---|---|---|---|---|
| 第 0 幕后 | — | — | — | `~/.sbx/config.toml` |
| 第 1 幕失败回滚后 | sbx-proxy（停止） | sbx-egress | sbx-home、sbx-cache | worktree fix-login、`~/.sbx/proxy/`、镜像 |
| 第 4 幕后 | sbx-proxy、2 个 agent 容器 | sbx-egress、2 个 Task 网络 | 加 2 个依赖 volume | 2 个 worktree、2 个 state 目录、squid 片段 |
| 第 7 幕后 | 同上，全部停止 | 同上 | 同上 | 同上 |
| 两个 Task 都 done 后 | sbx-proxy（停止） | sbx-egress | sbx-home、sbx-cache | 只剩分支 `sbx/fix-login`、`sbx/add-search`（在主仓库里）和共享状态 |

一直保留的东西：

| 资源 | 作用 |
|---|---|
| `sbx-home` | 登录态、项目记忆、会话历史 |
| `sbx-cache` | 依赖下载缓存 |
| 镜像 | 不用每次重新构建 |
| `~/.sbx/proxy` | squid 主配置 |

这些都是有意保留的，下一个 Task 启动时直接复用。

---

## 附二：这一幕里没出现、而且不打算出现的东西

走完一遍之后，最容易冒出来的下一个念头是"那我怎么连数据库"、"怎么跑 testcontainers"。先看能用的：

| 要什么 | 用什么 | 容器里怎么访问 |
|---|---|---|
| 测试要一个 postgres / redis | `[[services]]`（可以进仓库） | `db:5432`——服务名，不是 `localhost` |
| 连你本机已经在跑的实例 | `[[forward]]`，`to = 5432` | `db:5432` |
| 连内网的 staging 库 | `[[forward]]`，`to = "10.10.16.80:5432"` | `staging-db:5432` |
| 装私有源的依赖 | `sbx prep build`（宿主机侧构建，有网有凭据） | 依赖已经在 volume 里 |

模板分别由 `sbx config init`（项目层，含 `[[services]]`）和 `sbx forward init`（工作区层，含 `[[forward]]`）生成；两者都是全注释，`--lang zh` 给中文注释，`--print` 只看不写。

`[[forward]]` 只能写在全局层或工作区层，`to` 必须解析到私有地址——这两条的理由见 [boundary.md](boundary.md) §3.3。

下面这些是**决定不做**，不是还没做。它们每一条都能让上面那套隔离直接失效：

| | 为什么不 | 替代做法 |
|---|---|---|
| `/var/run/docker.sock` | 等于把宿主机 Docker 的完全控制权交给 Agent：它能起特权容器挂你的根目录。比 R1 严重一个量级——R1 要等你执行 git 命令才触发，这个是立即、直接、完全的 | `[[services]]` 声明测试依赖，替代 testcontainers 自起容器 |
| 云厂商凭据、kubeconfig | 爆炸半径最大的一项：花钱、删资源、拖数据；kubeconfig 等于生产集群的操作权 | 需要云资源的操作在宿主机做 |
| SSH key 进容器 | Agent 获得任意仓库的推送权，与 R1 同级 | 分支本来就在宿主机（worktree + `.git` 是 bind mount），`git push` 在宿主机做 |
| `host.docker.internal` 直通 | 一旦可达，容器能连宿主机**每一个**监听端口：你其他项目的 dev server、所有本机数据库。没有"只开 5432"这回事 | `[[forward]]`，一条一个端口 |
| `forward` 指向公网地址 | forwarder 会变成绕过 squid 的通用出网隧道：不受白名单约束、不进访问日志、`sbx net denied` 看不见 | 公网访问走 `network.allow` + squid；内部用公网 IP 段的环境可用 `allow_public` 单条解除 |
| 给 Task 容器加非 internal 网卡 | 那一刻它就有了默认路由，HTTP 流量可以绕开 squid，白名单 / 策略拦截 / 访问日志全部失效 | forwarder sidecar |
| 运行期凭据注入 | 暂不提供（不是永不）。最常见的 `git push` 场景已被架构绕过，剩余需求面小 | 中途需要新私有依赖时，明确失败并提示重跑 `sbx prep build` |
| 设备 / `--gpus` / `--cap-add` | 暂不提供。GPU 本身风险低，但需要先有一套统一的能力声明机制 | — |

**Task 容器始终没有默认路由**，是这一切的支点：`[[services]]` 和 `[[forward]]` 都是往 Task 网络里加 peer，没有一条给 Task 自己多插一张网卡。这条不变量有自动化回归测试盯着（`go test -tags docker ./internal/cli/ -run NoDefaultRoute`）。
