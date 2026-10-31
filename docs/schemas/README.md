# v2 机器契约

这些 Draft 2020-12 JSON Schema 由实际 CLI 的 Go wire 类型和输入结构规则生成，使用 `sbx schema <name>` 离线导出。`$id` 是稳定 URN，不表示存在在线 schema 服务。输入只支持 `sbx.execution/v2`；独立的 artifact-retention、team-event、task-list 与 task-maintenance 契约各自使用 v1。

| 名称 | 内容 |
|---|---|
| [task](task.schema.json) | TaskSpec，包含 Source、tools、operations、outputs、criteria、Sink、limits |
| [event](event.schema.json) | JSONL 单行：生命周期事件或无 attempt 的早期失败 |
| [result](result.schema.json) | 完整执行/验证/交付/清理维度和阶段结果 |
| [task-status](task-status.schema.json) | 通用任务历史、产物及业务证据、工具审计与恢复指引 |
| [execution-status](execution-status.schema.json) | Git 会话执行历史与恢复指引 |
| [version](version.schema.json) | 能力、适用条件、schema 名称及兼容边界 |
| [admission](admission.schema.json) | 只读准入响应 |
| [retention](retention.schema.json) | 显式产物清理响应 |
| [task-list](task-list.schema.json) | G9 离线任务列表：持久化状态与恢复门禁，独立 v1 |
| [task-maintenance](task-maintenance.schema.json) | G9 stop/clean/recover 的本地维护结果，独立 v1 |
| [team-event](team-event.schema.json) | G8 汇总事件：独立 v1、严格字段白名单、稳定 ID 和摘要身份 |

Schema 做结构检查；运行时继续检查 UTF-8、1 MiB 输入上限、重复 JSON key、逻辑 ID 去重、路径/glob 语义、操作 value 序列化后 64 KiB 上限和实际策略授权。对象解析后无法用标准 JSON Schema 发现原始重复 key；不能把结构合法等同于获得授权。可选输入省略，拒绝 null；省略 criteria 与显式 [] 的授权语义不同。

执行协议输出允许新增字段；独立的 team-event/v1 为隐私白名单，拒绝额外字段。执行协议调用方须忽略未知字段，遇到未知状态停止自动恢复并查询/人工核对。切勿把未知状态当成功或自动重试依据。旧 v2 记录可能没有 mode/workspace_kind；读取时对应空字符串，不补造历史。数组在部分历史/未请求的结果中可为 null。事件的 type、身份及各响应 schema_version 有固定约束；阶段文本不作为稳定错误分类。

```bash
sbx schema
sbx schema task > task.schema.json
sbx schema task-status > status.schema.json
```

开发者变更 wire 类型后，从仓库根目录重新构建，再更新并检查：

```bash
python3 -m venv /tmp/sbx-schema-venv
/tmp/sbx-schema-venv/bin/pip install jsonschema==4.26.0
/tmp/sbx-schema-venv/bin/python core/scripts/check-schemas.py --sbx /absolute/path/to/sbx --write
/tmp/sbx-schema-venv/bin/python core/scripts/check-schemas.py --sbx /absolute/path/to/sbx
```

Go 测试检测已发布文件与代码的漂移；Python 独立校验器对照真实 CLI 解码，并可用 `--report <验收目录>` 校验实际 stdout 事件/状态/结果。协议和退出码见 [自动化规范](../automation.md)，迁移见 [迁移规则](../migration.md)。
