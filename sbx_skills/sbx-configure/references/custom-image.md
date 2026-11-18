# 定制运行镜像与 Dockerfile

用户不需要 web-go、希望精简工具链或需要特殊语言/系统库时读取。sbx 支持定制运行基础层，skill 应调查项目后帮助生成 Dockerfile、配置选择及验证步骤，而不是强制内置 profile。以下语义已核对 `core/internal/cli/upgrade.go` 的 `imageInputs`、`core/internal/image/image.go` 和 agent-layer Dockerfile；目标版本仍以配套知识 probe 为前提。

## 选择路径

镜像底层选择优先级为：**非空生效 `image` → Git 根 `.sbx/Dockerfile` → 内置 `profile`**。三者之后 sbx 都会叠加自己的 agent 层；`image` 不是直接作为最终 task 镜像启动。

| 用户需求 | 交付 |
|---|---|
| 已有符合要求的公司/公共镜像 | 配置 `image = "实际镜像引用"`；检查本机/工作区是否再次覆盖 |
| 用 Dockerfile 安装 Python、Java、系统库等，不需要复制仓库文件 | 生成或增量修改 `.sbx/Dockerfile`；检查生效 image 未遮蔽它；由 sbx 构建基础层及 agent 层 |
| Dockerfile 需要 COPY 项目文件或其他上下文输入 | 按项目实际上下文准备单独 Dockerfile 和 `docker build -f <文件> -t <标签> <上下文>`，再配置 image 引用该镜像；仅方案请求不执行构建 |
| 只需预下载项目依赖，播种依赖 volume / cache | 使用 `.sbx/prep.Dockerfile`；它不替代运行镜像 |

不要为使用自定义 Dockerfile 发明 `profile = "custom"` 或 `dockerfile` 配置字段；保留有效 profile，核对它贡献的默认 deps.mask 和网络规则是否仍适合项目。`image` 来自高优先级层时，删除项目字段不能使它失效，需指出真实来源并在已授权层修正。

## 兼容性和上下文

- 当前 agent 层要求 **Debian/Ubuntu 系基础镜像**，使用 apt-get、useradd/groupadd、bash 等能力。Alpine、distroless、scratch 不在当前支持范围，不能承诺任意 Docker Hub 镜像都能用。
- sbx 自动构建 `.sbx/Dockerfile` 时使用临时上下文，只放入该 Dockerfile，**不是 Git 根**。不能在其中直接 `COPY requirements.txt` 或仓库目录。需要项目上下文时走上表的单独构建路径。prep 的上下文则是 Git 根，两者不要混淆。
- agent 层负责安装 Codex/Claude、必要的 Node/npm、mise 和 sbx 入口，设置 agent 用户与 WORKDIR。用户 Dockerfile 主要负责工具链/系统库；不要依赖自己设置的 USER、ENTRYPOINT、WORKDIR 或 PATH 最终保持原样，也不需重复安装 agent。即使不用 web-go，agent 层仍可能安装 Node/npm。
- 工具链应能被最终非 root agent 用户访问，不能只装入 `/root`。核对最终 PATH、版本、可写缓存/依赖目录与宿主 CPU 架构；安装时临时可用不代表 task 内可用。
- Dockerfile 及其 FROM 引用可审阅；凭据不放 ARG/ENV/COPY 或镜像层。现有镜像名 tag 相同也不保证内容不变，是否需要 digest 或新 tag 由项目要求决定。

## 示例与验证

示例需求是 Python 3.12 + PostgreSQL 编译依赖，不包含 Go；版本与库必须依据真实项目调整：

```dockerfile
# <Git根>/.sbx/Dockerfile
FROM python:3.12-slim-bookworm
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential libpq-dev \
    && rm -rf /var/lib/apt/lists/*
```

该路径下不用为它设置 image；已有非空 image 时先处理覆盖关系。项目的 Python 包如需预下载，可另用 prep，并安排与运行镜像兼容的 Python 版本/ABI、系统库及播种目录。

修改前说明拟改 Dockerfile、配置字段、镜像来源及构建网络。遵守 `.sbx` 的现有 trust 流程。需要构建时核对当前 CLI 支持的镜像构建/启动入口，不编造 `sbx image build`。用新临时 task 检查最终工具版本、原生库、用户/路径和一条项目命令；未有 Docker 时交付 Dockerfile 与待执行步骤，状态保持方案/未验证。不将成功生成 Dockerfile、构建基础层或 prep 通过等同于最终 task 运行通过。

HTML 同步展示选中的路径（image / 自定义 Dockerfile / 内置 profile）、被覆盖的来源、基础层 → agent 层 → task；若单独构建，再画出构建上下文与产出的 image 引用。prep 仍是独立分支。
