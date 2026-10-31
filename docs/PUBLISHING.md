# Wiki 发布与维护

[Wiki 首页](index.html) 是静态站点入口，核心内容仍以 Markdown 为唯一正文来源。首页内置阅读器、目录筛选、深色模式和响应式布局，不依赖 CDN、外部字体、构建服务或在线 schema 服务。

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
