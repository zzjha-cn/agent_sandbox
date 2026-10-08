# sbx

在容器沙箱里运行 Claude Code 的 Task 运行时。设计见 [docs/design.md](docs/design.md)，术语见 [docs/CONTEXT.md](docs/CONTEXT.md)。

当前进度：M1（MVP）加 M2 的一部分。支持 `run / attach / shell / stop / ls / path / done / memory / upgrade / net`，内置 Profile 只有 `web-go`，网络只有 shared proxy。

**网络默认不拦截**（`network.mode = "open"`，2026-10-08 改，ADR 0005 修订）。要按白名单放行就在配置里写 `mode = "allowlist"`，或者 `sbx run <task> --net allowlist`。两种模式都走 Squid，所以 `sbx net denied` 的日志一直有；云端 MCP 的策略拦截在两种模式下都生效。

## 首次使用

### 1. 安装

```bash
cd core
make build                     # 生成 bin/sbx
ln -sf "$PWD/bin/sbx" /usr/local/bin/sbx   # 可选
```

前提：Docker Desktop 已启动，内存建议 10GB（ADR 0012）。

### 2. 个人配置（需要宿主机代理时）

`~/.sbx/config.toml`：

```toml
[network]
upstream = "http://host.docker.internal:7890"   # 宿主机代理；留空表示直连
```

其他可配字段和默认值见 design §9.2：`resources`（默认 2 CPU / 3g / 1024 pids）、`deps.mask`（默认 `node_modules`）、`network.cloud_mcp`（默认 false）。

### 3. 登录 Claude（只需一次，所有 Task 共享）

先在任意仓库里执行一次 `sbx run`，构建镜像并创建 `sbx-home`。没有登录时 sbx 会打印下面这条命令，照着执行：

```bash
docker run -it --rm -e HOME=/home/agent -v sbx-home:/home/agent \
  -e HTTPS_PROXY=http://host.docker.internal:7890 <镜像，如 sbx/web-go:8c00ab2b56bf> claude auth login
```

按提示打开链接，把授权码粘贴回来。凭据保存在 volume `sbx-home` 里。

### 4. 日常使用

```bash
cd <你的仓库>
sbx run fix-login            # 新建 Task：worktree ~/.sbx/worktrees/<ws>/fix-login，分支 sbx/fix-login，进入 claude
                             # 离开：Ctrl-b 松手再按 d（Agent 继续运行）
                             # 注意 Ctrl-D 是退出 claude，不是离开；退出后会话还在，sbx run 可重新拉起
sbx run t2 --detach          # 只启动不进入
sbx run fix-login            # claude 退出后再跑一次 = 重新拉起，并接上上次对话（--fresh 则开新对话）
sbx ls                       # 状态（running/idle/exited(agent)/stopped…）、ahead 提交数、diff、最后活动时间、工作目录
code "$(sbx path fix-login)"  # Task 的改动在它自己的 worktree 里，不在仓库目录；合并分支后才回到仓库
sbx attach fix-login         # 回到 Agent 会话
sbx shell fix-login          # 在容器里开一个 bash
sbx stop fix-login           # 停止容器，保留一切；sbx run 可恢复
sbx done fix-login           # 结束：清理容器/网络/依赖 volume/worktree/state，保留分支
git merge sbx/fix-login      # 在主仓库里合并
sbx net denied               # Agent 被代理拦了什么（--all 看全部分类，--since 2h 限时间）
sbx net allow <host>         # 放行一个域名，并对运行中的 Task 热加载
sbx memory pull              # 把沙箱里新记的项目记忆导回宿主机（先看差异再确认）
sbx upgrade                  # 升级沙箱里的 claude（容器里不会自动更新）；新建的 Task 生效
```

**项目记忆**：每次 `sbx run` 会把宿主机这个仓库的 Claude 自动记忆（`~/.claude/projects/<key>/memory/`）导入沙箱；沙箱里新记的不会自动回到宿主机，需要 `sbx memory pull`（ADR 0016）。

省略 task 名时用 `main`：直接使用仓库根，不建 worktree。

## 开发

```bash
make test          # 单元测试
make test-docker   # 带 docker 的集成测试（首次会构建镜像，需要几分钟）
make lint          # golangci-lint；未安装时退回 go vet + gofmt
make e2e           # 端到端验收：scripts/e2e-m1.sh（需要已登录）
```

`-v / --verbose` 打印执行的每一条 docker 和 git 命令。
