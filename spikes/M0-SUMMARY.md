# M0 验证汇总

- 日期：2026-10-04
- 环境：macOS 13.0 arm64（8C/16GB），Docker Desktop 27.4.0（VirtioFS，**VM 8C / 8GB**），`ubuntu/squid` 6.13，claude 2.1.289，codex-cli 0.160.0
- 结论：**设计不需要推翻**；有 1 项新决策（ADR 0015），10 处设计修订，1 项待定（R10）。

## 各项结果

| 项 | 结果 | 详情 |
|---|---|---|
| M0-1 容器内的订阅登录 | ✅ Claude；⏸ Codex 推迟到 M3-6 | [m0-1/RESULT.md](m0-1/RESULT.md) |
| M0-2 macOS bind mount 的读写性能 | ✅ 依赖放在 volume 时只比宿主机慢 6% | [m0-2/RESULT.md](m0-2/RESULT.md) |
| M0-3 Squid 串联宿主机代理 | ✅ 混合端口可以当 HTTP 上游 | [m0-3/RESULT.md](m0-3/RESULT.md) |
| M0-4 shared 模式和工具兼容性 | ✅ 身份隔离成立，10 个工具全部兼容 | [m0-4/RESULT.md](m0-4/RESULT.md) |
| M0-5 状态 hooks | ✅ headless 和交互模式都能可靠区分 running/idle | [m0-5/RESULT.md](m0-5/RESULT.md) |

## 风险变化

| 风险 | 变化 |
|---|---|
| R2 容器里不能完成 OAuth | Claude 已解除；Codex 待验证 |
| R3 macOS 读写性能 | 已解除 |
| R4 只有 SOCKS 上游 | 已解除 |
| R6 带认证的代理 URL 兼容性 | 已解除 |
| **R9（新）** Claude 内部状态字段随版本变化 | 升级时做冒烟测试 |
| **R10（新）** Docker VM 内存 8GB < `max_running × memory` | 已决定 B：VM 10GB，每个 Task 3g × 3 |
| **R11（新）** 云端 MCP 被拦后持续重试带来噪音 | 分类隐藏 |

## 新决策
- **ADR 0015**：默认拦截 claude.ai 云端 MCP 连接器（你选择了 A）。

## 回写到设计的修订（design.md v1.1）

1. **挂载规则**：sbx 生成的文件放在目录里挂载，更新时先写临时文件再 rename（单文件 bind mount 会读到截断的内容）→ §4.2
2. **包缓存路径**用环境变量显式指定，不依赖各镜像的默认值 → §4.2
3. **必须以非 root 运行**；sbx-home 和 sbx-cache 按 Agent 的 UID/GID 初始化属主 → §5.3
4. 默认设置 `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`；镜像构建允许重试 → §5.3
5. 内置白名单增加 **`.claude.com`**（不通时交互模式直接退出）和 `github.com`；生成白名单时去重 → §6.3
6. 新增**策略拦截层**（云端 MCP），写在放行规则之前 → §6.3、§6.4
7. `net denied` 分三类：不在白名单 / 策略拦截和遥测 / 407 认证失败 → §6.5
8. 登录方式：Claude 用 `claude auth login`（粘贴授权码，不需要回调端口）→ §7.1
9. hooks 通过 `--settings` 注入；**`on_idle` 挂在 Notification 上**（空闲约 60 秒触发）→ §7.2
10. **Claude 首次启动状态的预置**：`hasCompletedOnboarding`、`hasTrustDialogAccepted`、`theme`、`skipDangerousModePermissionPrompt` → §7.2

## 遗留
- 测试 volume `m01-home`、`m05-home` 里有你的 Claude 登录凭据，留着方便以后复测。不需要时执行 `docker volume rm m01-home m05-home`。
- 测试镜像 `sbx-m04-tools`、`sbx-m05` 可以删除，也可以留作 M1 开发参考。
