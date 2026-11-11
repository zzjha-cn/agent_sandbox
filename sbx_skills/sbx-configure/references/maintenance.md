# 加载、版本与维护

## 使用

本目录是完整 skill 包，需要 Python 3.9+。将整个 sbx-configure 文件夹交给支持 SKILL.md 的 agent 加载，保持 references、scripts、manifest 的相对位置。

Codex 的本次会话可直接指向文件，例如：“使用 `/绝对路径/sbx_skills/sbx-configure/SKILL.md` 为当前项目配置 sbx。”持久安装由用户将整个目录安装到其 skills 目录，后续用 `$sbx-configure` 调用；不要只复制 SKILL.md。不同 agent 的安装与重新加载方式按其实际能力核对，不在本 skill 内自动修改全局安装。

本机 `sbx` 不在匹配列表时，配置 agent 报告不匹配并继续调查，不运行下面的维护命令。知识清单并非数字签名，不是对不可信下载的真实性保证；从可信发布渠道取得整个包。

## 生命周期

probe 只运行 version --json 和固定列表的 --help，不读取项目或用户配置、不调用 Docker，不留持久缓存；知识文件完整性、二进制摘要、版本、协议和帮助摘要均匹配才返回 compatible。未绑定的二进制即使版本字符串相同也不会自动通过。

check 检查包内知识文件。维护者在源码仓库使用 `check --repo <root>`，还会检查知识依赖源文件摘要；不会因为修改审核日期而消除源码漂移。

项目锁文件、CI 和配置变化时重新调查相关部分。本机路径、认证与网络的实际状态不能从工具版本推断。用户选择以最新指令和现有配置为准。skill 升级不重写项目；第一版无后台刷新或探测数据库。凭据内容不进入清单、参考文件或验证记录。

## 审核和重新绑定（维护者）

以下用于维护发布，不是运行期的兼容性绕过：

1. 审核目标提交中影响配置的变化，更新 references、验收夹具和限制说明。相关 core 变更需先提交。
2. 从该提交构建目标二进制，核对来源，运行脚本测试和对应行为验收。seal 记录维护者声明，不能自动证明任意传入二进制来自该源码。
3. 使用 `python3 <skill-dir>/scripts/knowledge.py seal --sbx <binary> --repo <repo-root> --revision <知识修订号> --reviewed` 更新知识清单。
4. 执行 `check --repo <repo-root>`、`probe --sbx <binary>`、skill 格式检查及 CLI 文档一致性检查。
5. 发布整个 skill 包及对应二进制。摘要保证内容一致性，不替代语义审核；不要把 check 通过当作真实 Docker 测试通过。

封包前更新 [验收记录](acceptance.md)。seal 在当前清单中将真实 Docker 和私网状态保守记录为 not_executed，不根据交叉编译或脚本测试推断；未来真实执行后应扩展成可追溯的验收记录格式。

## CI 和发布

仓库 CI 检查知识依赖漂移、脚本行为和 skill 链接。源码依赖改变后检查失败，需要维护者更新知识并 reseal。

release 工作流在本机平台构建同版本 sbx，跑受控测试与 CLI 文档检查，再为四个平台的二进制归档绑定摘要，生成配套 skill tar.gz。交叉编译的二进制只标记摘要绑定，不标记 execution_checked；使用时仍须比对本机帮助。此流程不证明 macOS 和 ARM 平台上的运行行为已经测试。

本地封包命令：`python3 core/scripts/package-configure-skill.py --sbx <已验证binary> --release-dir <归档目录> --version <同一版本> --output <新的tar.gz路径>`。工具复制到临时目录再更新清单，不修改源码目录中的 manifest。版本或知识依赖漂移时拒绝封包，先按上述流程审核。
