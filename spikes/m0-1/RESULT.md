# M0-1 结果：容器内的订阅登录

- 日期：2026-10-04
- 环境：同 M0-4 的测试镜像（claude 2.1.289、codex-cli 0.160.0），用 volume `m01-home` 挂到 `/root`，模拟 sbx-home
- 复现：`./login.sh claude|claude-token|codex` → `./verify.sh`（输出在 `result.log`）

## 结论

| Agent | 登录方式 | 结果 |
|---|---|---|
| Claude | `claude auth login`（交互，不需要回调端口） | ✅ 在容器里完成；**全新容器**读取 volume 后 `loggedIn: true / authMethod: claude.ai`，`claude -p` 正常返回 |
| Claude | 同上，然后经过 shared squid（internal 网络 + 带认证代理） | ✅ `claude -p` 正常返回（补完了 M0-4 里 claude 用真实凭据的验证） |
| Claude | `claude setup-token`（长期 token，可以作为环境变量注入） | 未测；作为 `sbx login` 的备选方式 |
| Codex | `codex login --device-auth`（设备码） | ⏸ 你决定暂缓，推迟到 M3-6（支持 Codex）时再验证 |

- **R2（容器里不能完成 OAuth）对 Claude 解除**：不需要映射回调端口，ADR 0006 的"登录一次全局生效"成立。
- `sbx login claude` 就是 `docker run -it -v sbx-home:... <镜像> claude auth login`。

## 重要发现：claude.ai 云端 MCP 连接器会进入沙箱 ⚠️

shared squid 的日志里，`mcp-proxy.anthropic.com` 有 **35 次** `TCP_TUNNEL/200`。原因是订阅账号在 claude.ai 上配置的云端连接器（Gmail、Drive、Notion 等）会被容器里的 claude **自动加载**。

- 这意味着沙箱里的 Agent 能通过这些连接器读写你的云端数据，**范围远超"只挂代码"的边界**。
- 这个域名属于内置白名单里的 `.anthropic.com`，会被直接放行，**绕开了白名单的初衷**。
- **决策（2026-10-04，你确认）：A 默认禁止**。在 proxy 层拦截 `mcp-proxy.anthropic.com`，按项目或 Task 配置打开。拦截之后 claude 能否正常工作，由 M0-5 验证。

## 其他观察

- `http-intake.logs.us5.datadoghq.com`（Claude 的遥测）在设置了 `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` 之后**仍然出现过 1 次**被拒。被拒是无害的，但 `sbx net denied` 应该内置一个"已知遥测"的忽略列表，避免噪音。
- `claude auth status` 显示 `analyticsDisabled: false`。
