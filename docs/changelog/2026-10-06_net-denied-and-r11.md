# 2026-10-06 · 关掉云端 MCP 重试风暴（R11），实现 `sbx net denied`（M2-10）

> 承接 [2026-10-06_session-keepalive.md](2026-10-06_session-keepalive.md)。起因是用户反馈"沙箱里网络不太稳定"，想要一个能看情况的界面。

## 一句话

先查了日志：4.7 小时里 88% 的出网请求是被拦掉的云端 MCP 在重试，一分钟最多 321 次。R11 的官方开关找到了（`disableClaudeAiConnectors`），已经注入；顺手把 M2-10 `sbx net denied` 做了，以后这类问题一条命令就能看。没有做管理界面。

## 1. 定位：所谓"网络不稳定"

`sbx-proxy` 的 access.log（约 4.7 小时，1595 行）：

| 结果 | 目标 | 次数 |
|---|---|---|
| DENIED | `mcp-proxy.anthropic.com` | 1408 |
| OK | `api.anthropic.com` | 178 |
| DENIED | `example.com`（e2e 测试） | 6 |
| OK | `downloads.claude.ai`、`platform.claude.com` | 3 |

重试不是均匀的：只出现在 12 个分钟里，其中一分钟 **321 次**，常常同一毫秒连发三条。ADR 0015 拦得没错，但客户端那头不知道该放弃，于是表现成突发卡顿。

## 2. R11：从源头关掉

官方开关是 settings 里的 **`disableClaudeAiConnectors`**（[settings-reference](https://code.claude.com/docs/en/settings-reference#disableclaudeaiconnectors)）：

- 类型 Boolean，默认未设置（会去取 claude.ai 的连接器）；
- 文档明确写了它是"安全键"：**`true` 从任何一层生效**，即使受管设置写了 `false`。所以写在 sbx 注入的 `--settings` 里就够；
- 对应的环境变量 `ENABLE_CLAUDEAI_MCP_SERVERS` 优先级更高，能把它重新打开。sbx 不设这个变量。

改动：`agent.SettingsJSON(blockCloudMCP bool)` 在 `network.cloud_mcp = false`（默认）时写入 `"disableClaudeAiConnectors": true`；配置里打开 `cloud_mcp = true` 时不写，和代理那边的放行保持一致。`RenderGen` 多带一个参数，`run` 的新建和恢复两条路径都传。

> 还没做的：`--cloud-mcp` 命令行开关（M2-10a 剩下的部分）。

## 3. M2-10：`sbx net denied`

```
sbx net denied [task] [--all] [--since 2h]
```

- 从 `sbx-proxy` 里读 `access.log` 和轮转出来的 `access.log.0`，按 squid 的 `logformat sbx` 解析。
- 按 design §6.5 分三类：**不在白名单**（默认只显示这一类，因为只有它需要你做决定）、**策略拦截/遥测**（云端 MCP、`.datadoghq.com` 等，默认折叠成一行计数）、**认证失败**（407）。
- 省略 task 时，按日志里的用户名前缀过滤出当前 Workspace 的全部 Task；指定 task 时只看它。
- 被折叠的类别仍然会提示次数，例如 `另有 策略拦截/遥测 200 次（--all 查看）`。
- 只有出现"不在白名单"的域名时才打印放行建议（`sbx net allow` 是 M2-11，还没做，所以建议里写的是手改配置）。

新增 `assets/allowlist/telemetry.txt`，只用于分类，不放行。

## 4. 没有做管理界面

理由写在对话里，简述：数据本来就在 access.log，缺的是暴露方式，一个命令就够；实时看网络的页面需要常驻进程，和 ADR 0011"不做常驻守护进程"冲突，要做得先补 ADR；真正缺的是异常主动冒出来（`sbx ls` 的 DENIED 列 M3-9、`sbx doctor` M3-13），不是有个地方可以看。

## 文件

| 状态 | 文件 |
|---|---|
| 新增 | `core/internal/proxy/accesslog.go`、`core/internal/proxy/accesslog_test.go`、`core/internal/cli/net.go`、`core/assets/allowlist/telemetry.txt`，以及本文件 |
| 修改 | `core/internal/agent/agent.go`（`SettingsJSON`、`RenderGen` 签名）、`core/internal/cli/run.go`、`core/internal/cli/root.go`、`core/internal/agent/agent_test.go`、`core/internal/proxy/render_test.go`（挪走重复的 `contains`） |
| 文档 | `README.md`、`docs/CONTEXT.md`、`docs/architecture.md`（§6.1、§7.5）、`docs/design.md`（§6.5、§7.2、R11）、`docs/implementation-checklist.md`（M2-10 勾掉，M2-10a 更新） |

## 验证状态

| 项 | 结果 |
|---|---|
| `make test` | 通过（新增 4 个解析和聚合的测试） |
| `sbx net denied` 在真实代理上实跑 | 通过：默认输出"没有被拒的请求"加一行"另有 策略拦截/遥测 200 次"；`--all` 列出 `mcp-proxy.anthropic.com`；`--since 1h` 和指定 task 都生效 |
| `disableClaudeAiConnectors` 的实际效果 | **未验证**。需要重启一个 Task（settings 是每次 run 重新生成的），然后看 `sbx net denied --all` 里 `mcp-proxy.anthropic.com` 的计数是否不再增长 |
| `make test-docker` / `make e2e` | 未跑 |

## 留下的问题

- `tabwriter` 对中文列宽按字节算，`KIND` 那列的对齐会偏。`sbx ls` 也有同样的问题，要修就一起修。
- 遥测名单是手写的（`.datadoghq.com`、`.statsig.com`、`statsig.anthropic.com`、`.sentry.io`），只影响分类展示，漏了的会落到"不在白名单"里，看到了再补。
- `sbx net allow`（M2-11）还没做，放行仍然要手改 `~/.sbx/config.toml` 再重新 run。
