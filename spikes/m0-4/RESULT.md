# M0-4 结果：shared 模式（proxy_auth）和工具兼容性

- 日期：2026-10-04
- 环境：同 M0-3；测试镜像是 `Dockerfile.tools`（node:22-bookworm，加上 claude 2.1.289、codex、pnpm、pip、uv、go 1.19、rustup stable）
- 复现：`./run.sh`（输出在 `result.log`）

## 结论：✅ 通过，shared 模式可以作为默认

### 身份隔离

| 场景 | 结果 |
|---|---|
| task-a 正确凭据 → 自己白名单内的 github.com | ✅ 200 |
| task-b 正确凭据 → 不在自己白名单里的 github.com | ✅ `TCP_DENIED/403` |
| task-b 正确凭据 → 自己白名单内的 api.anthropic.com | ✅ 放行 |
| task-b 用 task-a 的用户名加错误 token | ✅ `TCP_DENIED/407` |
| 不带凭据 | ✅ `TCP_DENIED/407` |
| 共享 proxy 用 `network connect --alias proxy` 接入两个 internal 网络 | ✅ 每个网络里都能用 `proxy:3128` 访问到 |

配置目录整体挂载（`conf/` → `/etc/sbx`），通过 `include /etc/sbx/tasks/*.conf` 加载每个 Task 的片段，工作正常。

### 工具兼容矩阵（`HTTPS_PROXY=http://task-a:<token>@proxy:3128`）

| 工具 | 结果 | 说明 |
|---|---|---|
| curl | ✅ | |
| git（https） | ✅ | `ls-remote` 成功 |
| npm | ✅ | |
| pnpm | ✅ | |
| pip | ✅ | |
| uv | ✅ | |
| go mod | ✅ | 经过 proxy.golang.org 和 sum.golang.org |
| cargo | ✅ | 经过 index.crates.io 和 static.crates.io（sparse 协议） |
| codex | ✅ | 能连到 api.openai.com，用假 key 收到 401，说明网络是通的；也会访问 chatgpt.com 和 github.com |
| claude | ✅（网络层面） | 能连到 api.anthropic.com；用假 key 时会持续重试直到超时，**和不经过 Squid 的对照组表现一致**，属于 CLI 自身行为。用真实凭据的验证放到 M0-1 和 M0-5 |

**R6（部分工具不支持带认证的代理地址）的风险基本解除**：被测的工具都兼容。

### 对设计的补充

1. **407 不算"被拒请求"**：部分客户端（这次是 codex 访问 github.com）会先不带凭据发一次请求，收到 407 后再带上凭据重发。日志里会出现 `用户名为 - 的 TCP_DENIED/407`。`sbx net denied` **只统计 `TCP_DENIED/403`**，407 另外统计成"认证失败"。
2. **Claude 的遥测域名**：`http-intake.logs.us5.datadoghq.com` 被拒了。建议在 Agent 层默认设置 `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`（或 `DISABLE_TELEMETRY=1`），**不把它加进内置白名单**。这样减少了对外流量，也不会因为它产生"被拒请求"的噪音。
3. **codex 的附带域名**：`chatgpt.com`（订阅登录时会用到）和 `github.com`（推测是更新检查）需要放进内置白名单。
4. **白名单书写规则**：`.github.com` 本身已经包含 `github.com`，两条同时写会让 Squid 报警告。生成白名单时要去重。
5. **构建期网络**：镜像构建时用 `--build-arg HTTP(S)_PROXY=http://host.docker.internal:7890` 走宿主机代理。期间 rustup 访问 `static.rust-lang.org` 出现过一次 TLS 握手 EOF，重试就好了。构建要**允许重试**，必要时可以配置镜像源（比如 rsproxy.cn）。
