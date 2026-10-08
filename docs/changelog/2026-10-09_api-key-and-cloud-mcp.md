# 2026-10-09 · API key 注入与 `--cloud-mcp`（M2-13、M2-10a）

> 承接 [2026-10-08_concurrency-and-memory.md](2026-10-08_concurrency-and-memory.md)。动手前先把 M1 的 e2e 重跑了一遍（36 项全过），确认前三轮改动没有破坏 `sbx run` 的主路径。

## M2-13：用 API key 代替订阅登录

```toml
# ~/.sbx/config.toml —— 不能写在项目层
[agents.claude]
api_key_env = "ANTHROPIC_API_KEY"     # 或 api_key_file = "~/.keys/anthropic"
```

```
$ sbx run api
用 API key 启动（来自环境变量 ANTHROPIC_API_KEY），不走订阅登录
```

### 一个设计稿里没有的坑

实测时第一次就卡住了：

```
sbx: 启动 Agent：30s 内没有等到 SessionStart，画面最后 20 行：
  Detected a custom API key in your environment
  ANTHROPIC_API_KEY: sk-ant-...-real-key-0000000000
  Do you want to use this API key?
    Yes
  ❯ No (recommended)
```

claude 看到环境里有自定义 key 会弹确认框，**而且默认停在 No 上**。无人值守的场景下这等于整个特性不可用。

它记在 `~/.claude.json` 的 `customApiKeyResponses.approved` 里，值是 **key 的后 20 个字符**（弹窗里显示的也正是这一段）。所以 preseed 阶段顺带写进去，并从 `rejected` 里移除同一个值——之前手动点过 No 的 key 不然还是不生效。

这和 R9 记的"Claude Code 的内部状态字段随版本变化"是同一类脆弱点，升级时要冒烟。

验证方式：拿一个假 key 真跑了一遍容器。修之前复现了弹窗，修之后 claude 正常起到提示符，`.claude.json` 里是 `{"approved":["-real-key-0000000000"],"rejected":[]}`，容器 `Config.Env` 里有 `ANTHROPIC_API_KEY`。之后清掉了这条假记录和探针 Task。

### 其他决定

**配了但取不到值直接报错**，不静默退回订阅登录。环境变量没设、文件不存在、文件是空的——都报错。静默退回的后果是你以为在用 API key，账单却记到订阅账号上，而且不会有任何提示。

**两个键二选一**，同时配报错，不去定一个"谁优先"的隐含规则。

**环境变量名表驱动**：`authCLIs` 里加了 `keyEnv` 字段（claude → `ANTHROPIC_API_KEY`），和登录流程共用同一张表，加 Agent 还是只改一处。

**容器建好之后改配置不会生效**——环境变量只能在建容器时注入。所以 `sbx run` 会 `docker inspect` 核一下容器里到底有没有那个变量，没有就明说：

```
sbx: 配置里有 API key，但容器 sbx-shop-e76272-api 是在那之前建的，里面没有 ANTHROPIC_API_KEY。
环境变量只能在建容器时注入：sbx done api 之后重新 run
```

比让 claude 自己抛一个认证错误清楚得多。

**key 会留在 `docker inspect` 里。** 这是环境变量注入的固有代价，design §7.1 选的就是这条路，没有绕。文档里写明了。

**项目层写不了。** M2-2 的红线按键名拦，`api_key_env` / `api_key_file` 都含 `key`，自动就被挡住了——顺手补了一个测试把这件事钉住。

## M2-10a：`--cloud-mcp`

配置项早就有了，补上命令行开关：

```bash
sbx run api --cloud-mcp
```

只能打开不能关——关是默认值，不加就是关。给 flag 加一个 `--no-cloud-mcp` 没有意义。

## 顺带

`agents.<name>` 的合并从"只认 version"改成了按字段表走（`agentFields`），和顶层字段一个写法；`sbx config show` 里 `api_key_*` 只在配过时才占一行。`docker.Client` 多了 `HasEnv`。

## 验证

新增 `internal/cli/apikey_test.go`（8 个：环境变量、文件、两种缺失、`~` 展开、Source 不泄露 key、二选一校验）、`internal/agent` 两个（后 20 字符、preseed 的批准/去重/不误伤）、`internal/config` 两个（api_key 按层覆盖、项目层拒绝）。

`go build` / `vet` / `test ./...` 全绿；M1 e2e 36/36。
