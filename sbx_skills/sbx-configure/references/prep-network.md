# 依赖、服务、网络和文件

## Prep：构建与播种

只有配置的 Dockerfile 实际存在才启用 prep，默认路径 `.sbx/prep.Dockerfile`。构建上下文是 Git 根；COPY 使用相对根路径，monorepo 注意根锁文件、子模块和本地依赖。`.gitignore` 不是 Docker 的排除规则；避免 COPY 整个仓库，若需修改 Docker 忽略规则，应保留已有构建行为。

播种只覆盖：

- 镜像 WORKDIR 下命中 deps.mask 的目录 → 对应 task 的依赖 volume。
- `/sbx/cache/*` 下的一级目录 → 共享 sbx-cache。

普通语言基础镜像未必有 sbx 缓存布局；显式设置 GOMODCACHE、npm_config_cache 等。安装在 `/usr/local` 的工具不会自动交付。镜像必须有 WORKDIR，当前检查使用 `/bin/sh`。执行结束码成功也要核对播种计划及 warnings。

示例：项目 go.mod 确认 Go 1.27，私有包仅用于依赖下载，采用 netrc 认证。以下是项目层与 Dockerfile，路径和认证方式需调查后决定：

```toml
[prep]
watch = ["go.mod", "go.sum"]
secrets = ["netrc"]
```

```dockerfile
FROM golang:1.27
WORKDIR /src
ENV GOMODCACHE=/sbx/cache/go-mod
ENV GOPRIVATE=gitlab.corp.example
COPY go.mod go.sum ./
RUN --mount=type=secret,id=netrc,target=/root/.netrc go mod download
```

本机层映射 `[prep.secret]` 下 `netrc = "~/.sbx/secrets/corp.netrc"`。先落实映射再加载包含 required secrets 的配置，否则最终校验会报缺失。BuildKit secret 不写入 COPY、ARG 或永久 ENV；构建工具仍可能把认证信息写入输出，应检查实际安装方法。SSH 认证不能直接套 netrc 模板。

watch 参与 tag 哈希，应覆盖锁文件、COPY 输入、证书和安装辅助文件。Dockerfile 自身参与哈希。构建环境和工具输出还可能随远端变化，tag 相同不意味着所有远端输入可复现。

`sbx prep build --check` 实际构建镜像、启动探测容器并报告，不播种。正常 build 也不更新已有任务：镜像供新 task 播种使用，已有任务需明确 reseed。`prep show` 用于漂移和播种状态。reseed 覆盖依赖 volume，先明确授权及数据影响；验证优先新 task。

可选 `/sbx/prep.json` 仅支持 allow、env、ca，allow / env 按项目权限检查；不是任意配置文件。CA 是镜像内证书路径，PEM、`.crt` 扩展和真实安装结果需要核对。私钥不作为共享 CA 提交。

## 服务还是 Forward

services 仅支持 name、image、port、env，无宿主机挂载、command、healthcheck 或特权字段。实例和 task 生命周期关联。服务名是容器内地址，例如 db:5432，localhost 指 task 自己。应用初始化与服务 readiness 另行验证；不自动运行有数据写入风险的迁移。改配置不会自动重建已存在的同名服务容器；需要用新 task 或明确安排重建，不能以 config show 为运行镜像已变更的证明。

连接已存在实例使用本机层 forward。示例：

```toml
[[forward]]
name = "dev-db"
port = 5432
to = 5432
```

to 为裸端口表示宿主机，也支持 `"host:port"`。Task 侧按 forward 名及端口访问。HTTPS 场景可用 host 保留真实域名与 SNI，按目标版本帮助及配置契约核对。allow_public 默认不扩大；已有实例的连通、认证、读写测试分别验证，用户仅授权连接不等于授权迁移或写入真实数据。

## 网络排障

当前运行期默认 open，仍通过代理出网，不是任意路由。allowlist 模式的 allow 为追加域名规则，放行不代表可达。network.upstream 只支持带端口 HTTP 上游，可选省略 scheme；本机代理一般配置于本机层。

prep 在宿主机 Docker 上通过构建网络下载，不等于 task 获得相同内网能力；公司 VPN、Docker bridge、DNS 和代理是否可达要实际检查。prep 构建清除隐式宿主机代理参数，确需构建代理时明确安排，并检查 NO_PROXY。不要自动放开所有域名或关闭 TLS 校验。

排障顺序：确认构建/运行阶段 → 实际地址与域名 → DNS / TCP → TLS → 认证 → 最小业务请求。日志与响应脱敏。

## 本地文件

main 的原仓库 bind mount 会包括被 Git 忽略的文件；独立 task 的 Git worktree 不自动携带未跟踪的忽略文件。deps.mask 在仓库挂载之上用 volume 覆盖路径，会使宿主机同路径内容不可见；不是通用文件排除列表。

TaskSpec git Source 默认来自提交，working_tree / untracked 需要按协议选择，忽略文件不因启用 untracked 就自动导入。files Source 有自己的 include / exclude 与保留路径限制，不按 `.gitignore` 解释。

先明确 models / data / config 的用途、敏感性、体积、读写需求与 task 入口。当前 sandbox.toml 无任意挂载字段。缺乏适用机制时给出能力缺口，不把文件加入 Git 或暴露敏感目录作为默认解决方案。
