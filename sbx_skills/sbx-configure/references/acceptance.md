# 配置 skill 验收记录

## HTML 拓扑交付

2026-11-18：增加默认 HTML 交付流程、拓扑参考、离线模板、Python 渲染器和明确标注的演示数据。覆盖配置来源、运行镜像与 agent 层、独立 prep 构建/播种、task 和 volume、临时服务、forward / 内部服务及构建/运行网络。assets 纳入知识包摘要检查。revision 4 沿用本地 `m4-m5-acceptance-14e5391` 二进制，未修改 sbx 运行语义。

演示 HTML 已生成，内联 JavaScript 通过 `node --check`，skill 格式检查通过。浏览器控制工具初始化失败（Node SIGABRT），未完成浏览器视觉和交互验收；静态/脚本检查不代表页面视觉验收通过。没有对真实项目执行配置对话、Docker 构建或内网连接。

原有 17 项配置/知识包测试（含真实本地 CLI 夹具与封包往返）通过。按用户要求，移除独立的拓扑渲染器测试脚本及 CI / release 调用。原有二进制绑定保持不变，刷新本次审核的 skill 文件摘要。另按当前源码补齐自定义运行镜像流程：`image` → `.sbx/Dockerfile` → 内置 profile 的优先级、Debian/Ubuntu 兼容限制、自动 Dockerfile 的临时上下文及与 prep 的分工；这些是源码核对，未实际构建自定义镜像。

revision 5：HTML 语言遵循用户指定、其次当前对话语言。内置渲染器增加 `--lang zh-CN|en` 和输入 `language`，界面文案与内容生成语言分开控制；节点、关系、证据说明和备注须由生成者全部本地化，原始配置值保持不变。临时检查验证语言选择、命令行覆盖优先级、未知语言拒绝、英文示例无中文残留、原始值保留及无模板占位符残留；中英文内联 JavaScript 均通过语法检查。未新增独立测试脚本，未进行浏览器视觉验收。

revision 6：优化为拓扑总览、配置清单、连接关系三个视图，补充节点统计、带编号连线、点击详情、搜索和状态筛选。新增 HTML 的 `display.theme / density` 与命令行覆盖参数，页面支持浅/深色及舒适/紧凑切换；可选 configuration 数组逐项展示脱敏值、来源、状态和证据。旧输入仍可渲染，不推断缺失的配置值。

浏览器控制工具仍初始化失败，随后使用独立临时配置的本地 headless Chrome 完成配置页、深色英文总览和 430px 内容宽度的窄屏截图审阅（桌面浏览器窄屏检查，不是实际手机设备验收）。实际页面内验证标签切换、11 项配置记录、配置搜索、状态过滤、节点详情、Escape 关闭、主题/密度切换与快照数据不变。临时结构检查验证展示参数覆盖、旧格式兼容、无效配置拒绝、HTML/JSON 转义、英文无中文残留及中英文 JavaScript 语法；未重新加入被移除的独立测试脚本。这些是 HTML 产物验收，不证明示例中的 sbx 配置、Docker 或内网服务实际可用。

## M4–M5 收尾复验

2026-11-16：源码已提交到 `14e5391`，本地验收二进制为 `m4-m5-acceptance-14e5391`。知识包 revision 3 采用默认干净 core 源码绑定（`source_dirty: false`），不使用开发例外。95 个帮助入口和 23 份 schema 与实际二进制一致。

项目记忆实际 CLI 与隔离 BusyBox 容器交付验收见仓库 `docs/project-memory-m4-m5-acceptance.md`。另按用户选择使用宿主 Codex 完成真实模型候选生成/恢复，再对已审阅候选复跑 13 项 CLI/容器检查；生产 API 提取器仍未真实调用。这些测试不等于下方 A–I 配置场景的真实模型/私网验收。没有发布远程版本，也没有替换系统安装。revision 2 的 dirty 开发绑定保留为下述历史记录。

## M4–M5 开发构建同步

2026-11-13：配置参考补充宿主侧 `memory.query / facts / watch` 的使用边界，知识包 revision 2 绑定本地 `memory-m4-m5-dev` 二进制及当前工作区源码摘要，清单标记 `source_dirty: true`。这是维护者显式复核后的开发绑定；正式发布仍要求干净源码，配置 agent 不可自行重封以绕过版本不匹配。

CLI 参考已核对 95 个实际帮助入口，23 份发布 schema 与本地二进制一致，skill 格式校验及 17 项受控测试通过（包括显式开发绑定、源码漂移和封包往返）。记忆模块实现与验收范围见仓库 `docs/project-memory-m4-m5-acceptance.md`；真实 Docker、私网及模型对话仍未验收。以下首版记录是历史结果，不代表本次重跑了所有跨平台场景。

## 首版验收

记录日期：2026-11-09。语义基线为知识清单中的 source_commit 与 source_files_sha256；二进制身份见 builds。安装和使用方式见 maintenance.md。

## 自动化范围

`core/scripts/test-configure-skill.py` 验证知识包身份、完整性、未知二进制、帮助变化、源码漂移与缺失工具的行为。设置 `SBX_SKILL_TEST_BINARY=<实际二进制>` 时，还使用临时 Git 项目和真实 sbx CLI 校验代表性配置。Docker 用不可用 stub 隔离，未启动真实 task 或联网。

这些是脚本行为与配置夹具测试，不证明真实模型会遵循全部对话指令。没有执行独立模型对话评测，不将手工准备的配置夹具称作端到端 agent 验收。

本次执行结果：16 项受控测试通过；skill 格式校验通过；CLI 参考与 91 个实际帮助入口一致；源码依赖及知识包完整性检查通过。验收二进制为从当前源码构建的 `skill-c79c9b5`，本地位于 `core/bin/sbx`，未替换 `/usr/local/bin/sbx`。

另外完成 darwin / linux × amd64 / arm64 四种真实交叉编译，并成功封包、检查四份归档摘要绑定。只执行了本机平台的 CLI，没有运行其他平台二进制。Docker daemon 连通探测报告无法连接本机 socket，因此真实容器场景保持未执行。现有 `/usr/local/bin/sbx` 的较早 dirty 构建被正确识别为知识不匹配，没有自动放行。

## 场景矩阵

| 场景 | 本次验证范围 | 真实运行范围 |
|---|---|---|
| A 公开 Go | agent / profile 配置接受与字段来源 | task 内 Go 与单元测试未执行 |
| B 私有依赖 | secret ID / 映射分层及缺失校验 | 私有下载、prep 构建和播种未执行 |
| C 临时数据库 / Redis | Redis 配置接受、保留已有内容的增量示例 | 服务就绪与集成测试未执行 |
| D 已有数据库 | 项目层拒绝 forward，工作区层接受 | 真实数据库连通、认证、读写未执行 |
| E 内网 API / CA | 参考规则已按源码核对 | 网络、TLS 与业务请求未执行 |
| F 忽略文件 | 挂载和 worktree 规则按源码核对 | 目标 task 内文件读取未执行 |
| G 增量修改 | 重复应用代表性补丁、列表并集及工作区覆盖 | 模型对话与 agent 启动未执行 |
| H prep 排障 | 构建/播种/旧 task 排障指引按源码核对 | 实际故障注入与恢复未执行 |
| I monorepo | 子目录查找根配置、嵌套 mask 接受 | 子项目安装、漂移和测试未执行 |

## 复验

维护者在仓库根执行：

```sh
SBX_SKILL_TEST_BINARY=<binary> python3 core/scripts/test-configure-skill.py
python3 sbx_skills/sbx-configure/scripts/knowledge.py check --repo .
python3 sbx_skills/sbx-configure/scripts/knowledge.py probe --sbx <binary>
python3 core/scripts/check-wiki-cli.py --sbx <binary>
```

真实 Docker / 私网测试需在具备环境并获授权时进行，记录 task 身份、输入、命令、结果和清理范围，再更新此矩阵。当前不声明 A–I 已完成完整运行验收。
