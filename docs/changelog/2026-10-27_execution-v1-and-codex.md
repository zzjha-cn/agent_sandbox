# Claude/Codex 与执行协议：当前源码版使用变更

日期：2026-10-27。这是当前源码实现的使用说明，不代表已经发布了对应版本的二进制。正式测试先重新构建并固定实际使用的路径，见 [测试指南](../testing.md)。

- **两种 provider 均可执行**：`sbx run --agent codex <task>` 临时覆盖本次，顶层 `default_agent = "codex"` 设置默认值；已有 Task 不能切换 provider，恢复时要选回原 provider。
- **登录独立选择 provider**：`sbx login` 省略参数始终是 Claude。Codex 可设备码登录，或 `sbx login codex --import ~/.codex` 导入单个 auth.json；不自动复制配置、skills 或 sessions。替换已有导入凭据需 --force。
- **Codex 模式边界**：支持交互/headless、准确 session 恢复及 fresh；API key 仅 headless。Claude hooks、项目记忆与 on_idle 不自动变成 Codex 功能。sbx upgrade 仍只升级 Claude，Codex 通过 agents.codex.version 更新镜像。
- **自动化用公开 v1**：version --json 发现 schema；admit --envelope 只读且不预留；run --envelope 执行并输出 v1 JSONL。provider 来自配置，不能混用 --agent 等运行覆盖。字段、超时和进程码见 [自动化手册](../automation.md)。
- **退出码不透传 agent 原码**：普通 headless 的 agent 非零是一般错误 1；v1/JSONL 为分类码 5，原码另存。v1 的环境/启动失败 4、超时 124、取消 130 等必须结合 result 判断。退出 0 不能代替测试、提交审查或自动合并。
- **daemon 归属门禁**：每位可信委托人独占 daemon；旧共享卷未登记时，先 status，再确认所有权后 adopt。owner/namespace 不符会拒绝。不同 SBX_HOME、Task 名或标签不能提供多用户隔离；isolation 仅个人全局可设。
- **失败与回滚**：创建前按本次 token 登记所有权，部分创建也可回收；恢复失败保护旧 Task 状态、依赖及 proxy/sidecar。主容器已丢失但旧 forwarder 仍在时拒绝破坏性重建，检查旧数据后 done/run。done 保留分支、共享身份/cache/mise、daemon 标记及空 workspace 父目录。
- **产物仍由使用者验收**：v1 不提供 artifact delivery，也无自动 merge/push 或内置 master-worker。JSONL stdout 与诊断 stderr 分开保存，结果缺失或交付失败不能当成成功。

完整命令与配置见 [commands.md](../commands.md)，连续使用场景见 [walkthrough.md](../walkthrough.md)。
