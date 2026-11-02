# 发布与维护

[Wiki 首页](index.html) 是静态站点入口，核心内容仍以 Markdown 为唯一正文来源。首页内置阅读器、目录筛选、深色模式和响应式布局；页面使用 Google Fonts，并提供系统字体回退。

## 发布二进制到 GitHub Releases

仓库的 [release workflow](https://github.com/zzjha-cn/agent_sandbox/blob/main/.github/workflows/release.yml) 由 GitHub Actions **手动触发**。推送 tag 不会自动创建 Release。workflow 默认创建草稿，发布者核对后再公开；`draft=false` 会直接发布。

### 1. 准备并验证发布提交

将需要发布的变更提交并推送到目标分支，确认提交内容和 CI 结果：

```sh
git status
git add path/to/changed-file  # 替换为实际需要提交的文件
git commit -m "Prepare release"
git push origin main
```

在 GitHub 的 **Actions → ci** 查看对应提交。当前 [CI workflow](https://github.com/zzjha-cn/agent_sandbox/blob/main/.github/workflows/ci.yml) 在 `main` 的 push 和 pull request 时运行 `make lint` 与 `make test`。Release workflow 也会在 tag 对应的代码上再次执行这两项。

CI 和 Release workflow **不自动运行** `make test-docker`、真实 Agent 端到端测试或生产业务接口验收。需要这些证据的版本，应在发布前按实际变更另行验证。

### 2. 创建并推送版本 tag

确认当前提交就是要发布的版本，然后创建 annotated tag，例如：

```sh
git log -1 --oneline
git tag -a v0.4.0 -m "sbx v0.4.0" -m "Describe the changes in this release."
git show v0.4.0
git push origin v0.4.0
```

示例中的版本号与说明应替换为本次内容。workflow 从 tag 内容提取标题与正文；annotated tag 的首段作为标题、后续内容作为正文。尽量在创建 tag 前写好说明，并确认该版本的 Release 尚不存在。

### 3. 手动触发 Release workflow

1. 在 GitHub 仓库打开 **Actions → release → Run workflow**。
2. `tag` 填写已经推送到 origin 的 tag，例如 `v0.4.0`。
3. `draft` 保持默认 `true`，创建待检查的草稿；选择 `false` 则直接创建公开 Release。
4. 点击 **Run workflow**，查看任务日志与最终状态。

该 workflow 会检出输入的 tag，验证 checkout 与 tag 精确匹配，按 `core/go.mod` 配置 Go，再运行 `make lint`、`make test` 和 `make release`。最后使用仓库的 `GITHUB_TOKEN` 创建 Release；失败时先查看失败步骤，不要把未成功的出包视为已发布。

### 4. 检查草稿并发布

在 GitHub 的 **Releases** 页面打开新草稿，检查 tag、标题、正文以及五个附件。以 `v0.4.0` 为例：

```text
sbx_v0.4.0_darwin_amd64.tar.gz
sbx_v0.4.0_darwin_arm64.tar.gz
sbx_v0.4.0_linux_amd64.tar.gz
sbx_v0.4.0_linux_arm64.tar.gz
SHA256SUMS
```

四个归档分别对应 macOS/Linux 和 amd64/arm64，每个归档内是 `sbx` 二进制；版本由构建时的 `git describe` 注入。下载附件与 `SHA256SUMS` 到同一目录后，可在 macOS 使用 `shasum -a 256 -c SHA256SUMS`，在 Linux 使用 `sha256sum -c SHA256SUMS` 校验。根据目标平台解压并运行 `./sbx --version` 做安装检查。

核对无误后，在草稿页面点击 **Publish release**。如果手动触发时选择了 `draft=false`，workflow 成功后无需再发布草稿。

### 本地出包

需要本地检查构建产物时，在 `core/` 运行：

```sh
cd core
make release
ls dist
```

`make release` 会**先删除并重建 `core/dist/`**，再交叉编译四个平台并生成 `SHA256SUMS`。本地出包不会创建 GitHub Release；未处于 tag 的提交上时，版本名由 `git describe` 决定，不一定是正式 tag 名。

## Wiki 静态站点

下面的流程维护项目 Wiki，与 GitHub Releases 的二进制附件是两条独立发布路径。

## 本地预览

在仓库根目录运行：

```sh
python3 -m http.server 8000 --directory docs
```

访问 `http://localhost:8000`。请使用 HTTP 预览，直接 file:// 打开时浏览器可能禁止 fetch Markdown；原文始终可直接阅读。

## 静态托管

推荐先导出经过白名单筛选的独立站点目录，再交给 GitHub Pages 或其他静态托管流程：

```sh
python3 core/scripts/build-wiki.py --output /tmp/sbx-public-wiki
python3 -m http.server 8000 --directory /tmp/sbx-public-wiki
```

输出目录须尚不存在，导出器不覆盖旧目录。它只复制公开手册和 schema，不复制 `private/`、实施清单、changelog 或个人文件。部署这个导出目录即可保留页面所需的相对路径。不要直接把含私有资料的整个 docs 目录上传；阅读器白名单是导航约束，不是静态服务器访问控制。

可直接分享 `?doc=commands.md` 或 `?doc=walkthrough.md`，也可附加标题锚点。Markdown 的同站文档链接会进入阅读器；schema JSON 保持原始机器文件。

## 内容分工

- walkthrough：场景、准备条件、操作顺序、成功判断与失败恢复。
- design：当前职责、流程、实现范围及安全边界。
- commands：按场景查命令、参数含义及操作后果。
- glossary：定义术语，明确同名概念与尚未实现的能力。
- cli-reference：当前二进制逐级 --help 的参数快照；更新 CLI 后重新导出。
- general-tasks / dynamic-tools / automation / boundary：配置、工具、协议与风险专题。

新增公开文档时，同步更新 index.html 的导航与 allowed 白名单。正文采用标题、段落、列表、表格、链接和 fenced code 等基础 Markdown；不要依赖内嵌脚本或远程渲染组件。

CLI 更新后，可在仓库根目录执行：

```sh
go -C core build -o /tmp/sbx-wiki ./cmd/sbx
python3 core/scripts/check-wiki-cli.py --sbx /tmp/sbx-wiki
# 确认接口变更后更新参数快照，并人工同步 commands.md 的场景说明：
python3 core/scripts/check-wiki-cli.py --sbx /tmp/sbx-wiki --write
```

发布前检查桌面/手机、浅色/深色、所有文档入口、标题锚点、schema 链接和浏览器控制台；核对示例命令与当前 --help。发布版说明当前行为，历史实施清单和 changelog 不替代用户规范。
