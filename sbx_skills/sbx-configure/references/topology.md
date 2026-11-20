# HTML 拓扑交付

配置对话结束时读取。交付的是本次方案或最终配置的结构快照，不是实时监控。配置知识不匹配、缺少 Docker 或内网时也可交付，标注阻塞和未知，不运行额外构建来填满图表。

## 收集与表达

与用户已确认的分类清单保持一致：Agent/供应商接入、镜像工具链、prep、服务、本地输入和网络均能找到对应说明；工具版本标注项目依据及是否实测，不沿用示例值。

沿用本次已经调查的文件、`config show`、prep 结果和 task 验证记录；生成时间、项目/工作区、入口、sbx 版本、方案/实际配置及验证范围必须可见。HTML 不替代配置文件，不将其路径写进 sbx 配置。

用以下四列组织节点；必要时同列增加节点，不能把所有内容塞入一个 task 框：

| 列 | 需要呈现的内容 |
|---|---|
| 配置与输入 | 默认/全局/项目/工作区层、prep 声明/组织策略、重要字段的生效值与来源；仓库根、子目录、锁文件、COPY 和 watch 输入 |
| 镜像与 Prep | profile、自定义基础镜像、sbx agent 层和最终运行镜像之间的关系；prep Dockerfile、FROM、WORKDIR、安装步骤、secret ID、构建网络、缓存路径、tag/漂移/构建结果 |
| Task 与存储 | main / 独立 task / TaskSpec 中实际选择的入口，agent 和工具链、资源；仓库挂载或 worktree、deps.mask 对应 volume、共享 cache；新 task 播种与旧 task reseed 的差异 |
| 服务与网络 | 每个临时服务的名字、镜像、端口和生命周期；每个已有内部服务的 task 地址 → forward → 目标地址，DNS/TLS/认证/业务验证；运行期代理、allowlist、上游和外部依赖 |

连线注明“覆盖/合并”“构建”“播种”“挂载”“TCP 端口”“HTTP(S)”等实际关系。prep 镜像与运行镜像分别画；只有 deps.mask 命中的 WORKDIR 目录与 `/sbx/cache/*` 会播种，不能画成整个 prep 文件系统传给 task。构建网络与运行网络分别表示。service、forward、已有内网实例用不同节点，不把宿主机 localhost 当作 task 的 localhost。镜像 tag/digest、工具版本仅在有证据时填真实值。

对未使用的 prep / services / forward 等用节点或备注说明“不适用：原因”；相关但未知的部分使用待确认节点，不能默默省略或用示例补齐。大项目可按子系统拆成多个节点，保留总览和关系清单。

每个节点和连接选择一种状态：`proposed`（方案）、`configured`（配置确认）、`verified`（该项已实测）、`blocked`（失败/阻塞）、`unknown`（未确认）、`na`（不适用）。证据注明文件/字段、命令、task 身份和结果范围；例如 TCP 通过的边写“仅 TCP，认证未执行”，不能让它暗示业务可用。配置来源已确认可以是 configured，不能因 `config show` 成功就把容器标为 verified。

交付前脱敏：只保留 secret ID、环境变量名和必要的路径引用，不写密钥正文、带凭据 URL、真实服务密码、完整 env 或原始命令输出。内联 JSON、页面隐藏内容和临时输入也属于交付数据。工具不自动识别秘密，由生成者审核输入。无需远程图表库、CDN、联网字体或上传数据。

## 使用内置渲染器

### 用户控制语言

语言优先级：用户对本次产物的明确要求 → 当前对话语言。已有产物语言只在用户要求沿用时作为依据，不覆盖本次选择。语言明确时直接生成，不额外追问。

内置界面支持 `zh-CN` 和 `en`：命令行 `--lang` 优先于输入 JSON 的 `language`，均未提供时渲染器兼容旧输入、默认 `zh-CN`。skill 必须根据上述用户偏好主动传语言，不依赖这个兼容默认值。其他语言需自行生成相同交付结构的完整本地化 HTML，不能静默退回中文，也不能为此修改安装包破坏知识摘要。

渲染器只本地化界面，不调用翻译服务。生成者须用所选语言撰写 JSON 的 `title`、`scope`、节点标题/详情/证据说明、边标签/证据和 `notes`。英文产物不应残留中文表头、状态、提示或说明；配置键、ID、镜像/tag/digest、域名、路径、环境变量名、命令和实际原始值不翻译。命令输出如需引用，保留必要原文并用目标语言解释。换语言时应更新全部说明内容，不能只改变 `--lang`。

用 Python 3.9+ 标准库渲染；无需 sbx 或 Docker。先将审核过的结构写为临时 JSON，再执行：

```sh
python3 <skill-dir>/scripts/render-topology.py --input <临时JSON> --lang zh-CN --output <项目>/docs/sbx-topology.html
```

用户选择英文时，先用英文准备 JSON，再传 `--lang en`。产物文件名沿用用户指定路径，语言本身不强制改变文件名。

### 样式与配置清单

页面包含拓扑总览、配置清单和连接关系三个视图。节点只展示摘要，点击打开详情；连线编号对应关系清单，节点和连接都可按文本/状态筛选。实线仅用于 verified 连接，其他状态用虚线，仍须阅读证据的验证范围。顶部统计中的“已实测”和“待处理”只计节点，不代表整体环境已验收。

可选 `display` 是本 HTML 的展示参数：`theme` 为 `light` / `dark`，`density` 为 `comfortable` / `compact`。默认浅色、舒适密度；命令行 `--theme` / `--density` 优先于 JSON 同名参数。用户可以在页面内切换，不会修改快照数据或 sbx 配置，也不需要重新连接任何服务。标题使用顶层 `title`。不把 display 或 configuration 写进 sandbox.toml。

例如：`--lang en --theme dark --density compact`。语言控制另见上一节，主题切换不翻译内容。

可选 `configuration` 为只读配置记录数组，推荐覆盖 profile / image、agent、资源、prep、service、forward 和 network 的关键字段。每项包含：

| 字段 | 含义 |
|---|---|
| `key` | 配置字段或列表元素定位，如 `services[name=redis].image`；定位文本不是可直接复制的 TOML 字段 |
| `value` | 脱敏后的展示值；字符串、数字或布尔值，列表/复杂值用原始 JSON 字符串，超出 JavaScript 安全整数范围的数字用字符串保真 |
| `source` | 实际来源文件/配置层；方案值明确标注拟写位置，来源未知则写待确认 |
| `status` | 与节点/边相同的状态枚举；不能把方案标成配置已确认 |
| `evidence` | 该值的采集或验证依据，如 config show 的字段来源；不要粘贴含凭据的原始输出 |

只收录本次已有证据或明确拟定的值，不从示例、描述或 profile 名推断出一套看似完整的生效配置。未提供记录时页面显示未采集提示。对真实配置须展示覆盖后的值及来源，必要时用节点详情解释覆盖前的值；不要让重复字段显得同时生效。所有记录说明沿用产物语言。

工具只读输入和内置模板、写 HTML，不调查项目、不调用 sbx、不验证运行状态。输出存在时拒绝覆盖；确认是本次要更新的生成文件后显式加 `--replace`。不要覆盖用户维护的 HTML。JSON 的 lane 为 0–3，对应上表；节点 `details` 和 `evidence` 是字符串列表；边的 `source` / `target` 引用节点 ID，`evidence` 为非空字符串。`notes` 记录不适用项、风险、未执行和下一步。所有状态由证据决定。

完整功能演示输入见 [topology-example.json](../assets/topology-example.json)，可用同一命令渲染以预览效果。它包含镜像、prep、缓存、临时服务和已有内网服务，全部为明确标注的演示数据，不作为目标项目的配置模板。

以下是格式示例，内容仅演示结构，不能复制为真实调查结果：

```json
{
  "title": "示例项目 · sbx 结构拓扑",
  "language": "zh-CN",
  "display": {"theme":"light", "density":"comfortable"},
  "scope": "方案示例；独立 task；sbx 版本待确认；未启动容器",
  "generated_at": "2026-11-18T10:00:00+08:00",
  "nodes": [
    {"id":"config","lane":0,"title":"项目配置","status":"proposed","details":["profile = web-go"],"evidence":["待写入 .sbx/sandbox.toml"]},
    {"id":"image","lane":1,"title":"运行镜像与 agent 层","status":"unknown","details":["profile 对应基础镜像及最终 tag 待核对"],"evidence":["尚未取得运行证据"]},
    {"id":"task","lane":2,"title":"独立 task","status":"proposed","details":["Git worktree；尚未创建"],"evidence":["用户目标：隔离测试"]},
    {"id":"db","lane":3,"title":"已有内部数据库","status":"blocked","details":["forward 名称、端口及目标待确认"],"evidence":["当前没有内网访问环境"]}
  ],
  "edges": [
    {"source":"config","target":"image","label":"选择 profile","status":"proposed","evidence":"方案字段，未加载"},
    {"source":"image","target":"task","label":"创建运行环境","status":"proposed","evidence":"未启动 task"},
    {"source":"task","target":"db","label":"待配置 forward","status":"blocked","evidence":"DNS/TCP/TLS/认证/业务请求均未执行"}
  ],
  "configuration": [
    {"key":"profile","value":"web-go","source":"拟写 .sbx/sandbox.toml","status":"proposed","evidence":"方案字段，尚未加载"}
  ],
  "notes": ["Prep：是否需要固化依赖待调查", "临时 services：不适用，本例使用已有数据库", "此文件为格式示例，不是可用环境证明"]
}
```

## 交付检查

确认所有节点/边都有状态和证据、引用完整、图中的每个真实值可追溯，没有示例残留或敏感内容。检查 HTML 在浏览器打开后图形、箭头、中文、长镜像名/地址、点击详情、搜索和关系列表可用；无浏览器时说明只完成结构检查。最终回复链接到 HTML 并简述验证边界。配置变化后重新生成快照，不沿用旧验证状态。
