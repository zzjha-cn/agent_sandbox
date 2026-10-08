# 2026-10-08 · 网络默认放开（ADR 0005 修订）、`sbx net allow`、容器时区

> 承接 [2026-10-06_net-denied-and-r11.md](2026-10-06_net-denied-and-r11.md)。

## 一句话

`network.mode` 默认从 `allowlist` 改成 `open`（使用者决定，ADR 0005 已补修订）；补上 `sbx run --net` 和 `sbx net allow`；修掉容器时区比宿主机差 8 小时导致 `events.log` 没法事后判读的问题。

## 1. 容器时区（真实使用中踩到）

`events.log` 写的是容器时间（UTC），`sbx ls` 的 LAST-ACTIVE 和 `net denied` 的 "36m ago" 走宿主机时间（CST），**差 8 小时**。判读一段无人值守的运行时，这会直接把结论读反——实际发生过一次。

- 容器注入 `TZ`：`agent.HostTZ()` 读 `$TZ`，否则读 `/etc/localtime` 的软链接目标（macOS 指向 `/var/db/timezone/zoneinfo/<zone>`，Linux 指向 `/usr/share/zoneinfo/<zone>`）。镜像里本来就有 tzdata，不用重建镜像。这同时修正了 git 提交时间和构建日志的时间。
- `events.log` 从 `%T` 改成 `%F %T %z`：原来只有 `03:48:31`，既看不出哪天（日志会跨天），也看不出时区。

`TZ` 是容器创建时设的，已有的 Task 要重建容器才生效；`status.sh` 每次 run 重新渲染，下次 run 就带上日期。

## 2. 网络默认改为 open

决定过程记在 ADR 0005 的「修订（2026-10-08）」一节，包括**明确放弃了什么**（design §8.2 "数据外传" 那一行的缓解在默认配置下不再生效）和当时的替代方案。

实现上 open 的渲染本来就有（`task.conf.tmpl` 的 `.Open` 分支），这次只是：

- `config.Default()` 的 `Network.Mode` 改成 `open`；
- `sbx run --net open|allowlist` 覆盖本次配置（会跑 `Validate`）；
- 策略拦截层的 deny 在模板里排在 open 的 allow 之前，所以**云端 MCP 在 open 模式下依然被拦**（ADR 0015 不受影响）。

## 3. `sbx net allow <host>...`（M2-11）

```
sbx net allow repo.mongodb.org fastdl.mongodb.org
```

- 写进 `~/.sbx/config.toml` 的 `[network].allow`。**直接改文本而不是重新序列化**，所以注释和排版都保得住——这个文件是人在手改的。支持三种情况：没有文件、有 `[network]` 没有 `allow`、`allow` 写成多行数组。
- 幂等：已经在名单里就不动文件。
- 写完对当前 Workspace 里所有运行中的 Task 重新下发白名单并 `squid -k reconfigure`，不用重启 Task。
- 当前 `mode = "open"` 时会提示一句"本来就不拦截，白名单只在 allowlist 模式下起作用"。
- `--project` 需要项目层配置（M2-1），还没实现，会明确报错而不是假装成功。

## 文件

| 状态 | 文件 |
|---|---|
| 新增 | `core/internal/config/edit.go`、`core/internal/config/edit_test.go`，以及本文件 |
| 修改 | `core/internal/config/config.go`（默认 open）、`core/internal/agent/agent.go`（`EnvInput.TZ`、`HostTZ`）、`core/internal/cli/run.go`（`--net`、TZ 注入）、`core/internal/cli/net.go`（`net allow`、`reloadAllow`）、`core/assets/agent-layer/hooks/status.sh`、各自的测试 |
| 文档 | `docs/private/adr/0005`（修订一节）、`README.md`、`docs/CONTEXT.md`、`docs/design.md` §6/§10、`docs/implementation-checklist.md`（M2-6、M2-11 勾掉） |

## 验证状态

| 项 | 结果 |
|---|---|
| `make test` | 通过（新增 6 个配置改写测试、1 个 TZ 测试） |
| `sbx net allow` 实跑 | 通过（用 `SBX_HOME` 指向临时目录）：注释和 `upstream` 原样保留、重复执行不改文件、对 1 个运行中的 Task 热加载成功 |
| `TZ=Asia/Shanghai` 在 `sbx/web-go` 镜像里 | 通过（容器里 `date` 输出 CST） |
| open 模式端到端 | **未验证**。需要新建一个 Task，确认 `curl` 一个白名单外的域名能通，且 `mcp-proxy.anthropic.com` 仍被拦 |
| `make test-docker` / `make e2e` | 未跑。e2e 里有"白名单外域名被拒"的断言，**默认改成 open 后这条会失败**，脚本需要加 `--net allowlist` |

## 留下的问题

- **`scripts/e2e-m1.sh` 还没跟着改**：9.3 的安全断言是在默认 allowlist 下写的。
- 给 Agent 注入"你在白名单后面，被拦就报告不要绕过"的上下文——ADR 0005 修订里记为建议，未排期。open 成为默认后优先级下降，但切 allowlist 的项目仍然需要。
- `sbx net allow --project` 待 M2-1。
