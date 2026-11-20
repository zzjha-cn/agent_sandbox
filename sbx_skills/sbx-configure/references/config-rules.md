# 配置规则

本文是随知识包绑定到源码版本的语义契约；先运行 knowledge.py probe。命令语法仍核对目标二进制帮助，冲突时停止依赖冲突部分的操作。

## 配置来源

基础层顺序：默认值 → `$SBX_HOME/config.toml` → `<Git根>/.sbx/sandbox.toml` → `$SBX_HOME/workspaces/<ws-id>.toml`。SBX_HOME 未设置时为 `~/.sbx`。此外 prep 声明及组织策略可能影响实际结果。不要把本机层复制进项目。

`sbx config show` 打印各层路径和字段来源，可以据此取得真实工作区配置路径。该命令可能进行 daemon 隔离检查或尝试读取 prep 声明；不要忽略缺失提示。没有独立的 `sbx config check` 命令。未知字段可能只报警并忽略，必须处理这些警告。

标量由后层覆盖，列表取并集，env / prep.secret / agents 按键合并；services / forward 按 name 合并，同名后层整条替换。profile 还会贡献默认 mask。删除项目列表元素不保证消除全局来源，空列表不能重置并集。不通过扩大范围绕过配置冲突。

## 字段与限制

| 需求 | 项目层可表达 | 本机全局层或工作区层 |
|---|---|---|
| Agent | `default_agent = "claude"` 或 `"codex"`；`agents.<name>.version / disabled` | `api_key_env` / `api_key_file` 二选一，或复用现有登录 |
| 工具链 | `profile = "web-go"` / `"py-rust"`；`image` 覆盖 profile | 本机差异按需覆盖 |
| 资源 | resources.cpus / memory / pids，max_running | 本机差异按需覆盖 |
| 依赖 | deps.mask；prep.file / watch / secrets | prep.secret ID 到文件路径映射 |
| 网络 | network.mode / proxy / allow / cloud_mcp；非敏感 env | 本机 upstream 建议在此表达，forward 必须在此 |
| 安全和执行 | 不设置 | git.protect、prep.on_drift、isolation、memory、on_idle / on_exit |

当前 managed 项目记忆还支持本机 `memory.query`、`memory.facts` 和 `memory.watch`。query 只做本地词法检索；facts 是操作者声明的版本等非敏感等值条件，watch 是显式输入文件摘要。修改选择条件或文件摘要可能阻止旧会话恢复，需新会话；不要把凭据放进 facts，不通过经验条目决定宿主机读取哪些文件。

项目层拒绝凭据形状的键、宿主机绝对路径、执行命令类键。`prep.secrets` 是 ID 列表的明确例外。临时 services.env 允许开发实例密码，但不得因此写入已有真实服务的凭据。不要为规避规则改写敏感键名称。

当前默认：agent claude，profile web-go，网络 open / shared，资源 2 CPU / 3g / 1024 pids，max_running 3，git.protect=true，prep.on_drift=warn。只写用户有意确定的字段；不将所有默认值冻结进项目。

## 工具链

当前源码的 web-go 提供 Go 1.27.0、Node 24、pnpm 和系统 Python；py-rust 使用 uv 和 Rust stable。实际已构建镜像可能不同，容器内版本才是运行证据。安装 agent 版本或登录与工具链版本是不同事项。

`default_agent` 选择运行 Agent，不会自动排除另一 Agent 的安装；当前版本可用 `agents.<name>.disabled = true` 从新运行镜像中省略该 Agent，默认 Agent 不可禁用。沿用有效配置中的禁用选择，不因使用模板重新安装。供应商端点/模型接入按对应 Agent 实际支持的配置安排，不把任意供应商名当作 sbx 的 default_agent，也不发明通用 provider 字段。

image 是运行基础镜像选择，sbx 仍会构建自己的 agent 层。需要系统库、特殊编译器或固定 Python 时，选择符合项目的镜像；不要把所有定制都放进 prep。

不使用内置工具链时，可用已有 image，也可生成 Git 根 `.sbx/Dockerfile`。优先级、Debian/Ubuntu 限制、自动构建上下文和示例见 [定制运行镜像](custom-image.md)。

## 最小示例

以下只是项目层片段，需要按目标项目调整，不是自动生成的默认值：

```toml
default_agent = "codex"
profile = "web-go"

[resources]
memory = "4g"

[[services]]
name = "db"
image = "postgres:16"
port = 5432
env = { POSTGRES_PASSWORD = "dev" }
```

先确定应用实际需要的数据库名、用户和连接变量，补齐对应设置；不能仅凭创建了服务就称测试环境完成。

## 信任与执行

`.sbx` 变更可能要求重新 `sbx trust`；展示差异并遵守现有信任流程，不替用户静默绕过。`sbx run` 默认操作 main 原仓库；`sbx run <name>` 使用独立 worktree。新 worktree 不会自动包含尚未提交的项目配置/源码，具体配置加载位置与执行输入都要检查，不能用修改尚未提交的原仓库就推断隔离任务会看到相同文件。

`sbx run --spec` / `sbx task run --spec` 是另一套显式 TaskSpec 执行入口，不把它的 Source / Grant 字段写入 sandbox.toml。需要协议输入时只读取当前 `sbx schema` 提供的名称。

M6 可通过可信全局/工作区 `[memory] recall = true` 启用运行时 `memory_search`。要求 managed、dedicated proxy、固定兼容版本和同步 headless；组织运行暂拒绝。agent 只传 key/query，项目、事实和范围由宿主固定。新会话/恢复沿用独立召回账本和累计预算，撤销已召回条目会阻止恢复。检查 `sbx memory status <task> --recalls`，具体限制和状态以当前 `docs/project-memory.md` 第 13 节及 `sbx schema memory-recalls` 为准；Claude 真实模型验收保持未完成。
