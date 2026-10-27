# 2026-10-26 · 导入本机 Codex 认证

已有本机 Codex 登录状态时，可显式指定配置目录或认证文件：

```sh
sbx login codex --import ~/.codex
sbx login codex --import /path/to/auth.json
sbx login codex --import ~/.codex --force
sbx login codex --status
```

只读取 `auth.json`，不导入整个目录，也不修改源文件。没有文件认证缓存时给出钥匙串/文件存储说明，不自动访问系统钥匙串。支持 ChatGPT 登录缓存和 API key 缓存；不改变其原有认证类型。

导入文件先做大小及 JSON 凭据结构检查，再通过 stdin 送入挂载 `sbx-home` 的无网络临时容器。容器内 Codex 在独立暂存目录识别凭据，成功后原子安装为 `.codex/auth.json`，权限 `0600`。凭据不进入命令参数、verbose trace 或状态输出。Claude 文件、Codex 配置与会话均保留。

默认保护已有认证文件；需要替换时加 `--force`。非强制安装使用原子硬链接，两个并发导入也不会悄悄覆盖彼此。验证失败保留旧文件，暂存目录在正常退出时清理。导入是独立拷贝，后续 token 刷新不在宿主机与沙箱之间同步。

检查只能证明容器 CLI 能识别凭据，不证明服务端仍接受它或模型请求成功；后续按 E2E 方案验证真实任务。

开发测试覆盖目录/文件输入、缺失/无效/过大缓存、选项冲突、凭据不进入日志、强制替换、失败保留、并发安装、文件权限及其他 provider 文件保留。另用真实 Codex 镜像、独立临时 Docker 卷与合成 API key 缓存验证首次导入、覆盖拒绝和强制替换；网络关闭，不读取真实用户凭据或调用模型。

```sh
cd core
GOCACHE=/private/tmp/sbx-go-build-cache go test ./...
SBX_REAL_DOCKER_E2E=1 GOCACHE=/private/tmp/sbx-go-build-cache go test ./internal/cli -run '^TestRealDockerCodexCredentialImport$' -count=1 -v
```

Docker 测试默认使用已构建的 `sbx/p0-provider-check:20261026`；可通过 `SBX_CODEX_TEST_IMAGE` 指定包含 Codex 的其他 Agent 镜像。测试只创建并清理自身卷和临时容器。

凭据缓存导入依据：[Codex 官方认证说明](https://developers.openai.com/codex/auth)。
