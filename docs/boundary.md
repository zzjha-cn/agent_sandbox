# 沙箱边界与前置依赖

> 状态：**P0–P4 已实现**（M4，2026-10-15）。P5 文档表态进行中。决策见 [ADR 0018](private/adr/0018-boundary-and-prep.md)，关联 [ADR 0017](private/adr/0017-private-access.md)（已被部分取代）。涉及 [design.md](design.md) §2、§4、§6、§9。
>
> 命令用法见 [commands.md](commands.md)（`prep` / `forward` / `clean`），端到端的例子见 [walkthrough.md](walkthrough.md) 附三。

沙箱越严，能用的东西越少。这份文档回答的是：**哪些外部东西沙箱确实需要、按什么机制给、以及哪些一律不给。**

## 0. 一条不变的底线

**网络默认全部从 egress proxy 出去，这一点不变。** 下面所有机制都是**显式配置才生效**，不配就什么都不发生；没有任何一个会改变默认行为，也没有任何一个会给 Task 容器一条默认路由。

Task 容器始终跑在 `--internal` 网络上（`NetworkCreate(t.Network(), true, ...)`），**永远没有默认路由**。它能接触外界的途径只有两条，都是显式的、单目的的、可审计的 sidecar：

```
[task 容器]  internal 网络，无默认路由
    ├──→ [squid]      HTTP / HTTPS，受白名单、策略拦截和访问日志约束（ADR 0005）
    └──→ [forwarder]  单一 TCP 端口 → 宿主机的某个端口（本文 §3）
```

## 1. 判据与分类

容器隔离了什么，沙箱就缺什么。判据只有一条：**它能不能被固化成一段不变的字节？**

```
可固化为字节？
├── 能 ──┬── 存储：依赖、权重、夹具、内网文档、私有 CA   → §2 prep
│        └── 环境事实：时区、UID/GID、git 身份           → 已实现（design §4.2）
│
├── 能但不该 ── 凭据 ─┬── build 期 → §2.4 BuildKit secret
│                     └── 运行期   → 暂不提供（§5）
│
└── 不能 ─┬── 伴生服务（postgres / redis…）  → §2.5 services
          ├── 宿主机上的服务                  → §3 forward
          ├── 实时内网访问                    → 暂不提供（ADR 0017 残留）
          └── 设备 / 内核能力                 → 一律不提供（§5）
```

**网络不是一个独立的类。** 它有两个角色，必须拆开看：

- **取**（拉依赖、拉数据集、拉镜像）——请求的*结果*是字节，可以固化。**这是伪装成网络问题的存储问题**，由 prep 吃掉。
- **通**（连数据库、联调 API）——连接本身不是字节，固化不了，只能走 forwarder 或 proxy。

绝大多数"内网依赖拉不到"的抱怨属于「取」，所以 prep 的覆盖面最大。

## 2. prep：把「取」变成「存」

### 2.1 它是什么

一次**宿主机上的 docker build**，产出一个镜像；sbx 从这个镜像里把内容播种进 Task 的 volume，Task 启动时依赖已经就位。

构建跑在宿主机的 Docker 上，走默认 bridge，**有完整的内网可达性和凭据**；Task 容器则一行私有网络配置都不需要。凭据只在 build 期存在，agent 永远碰不到。

```
.sbx/prep.Dockerfile ──build（有网有凭据）──> sbx/prep-<ws>:<hash> ──docker cp──> Task volume
```

### 2.2 使用姿势

```sh
sbx prep init          # 生成带注释的模板，把契约写在注释里
# 编辑 .sbx/prep.Dockerfile（也可以让 AI 照着项目写）
sbx prep build --check # 只构建并报告会播种什么、声明什么，不碰 volume
sbx prep build         # 正式构建
sbx trust              # .sbx/ 变了，确认一次
sbx run feat-x         # 新 Task 的依赖 volume 从 prep 镜像播种
```

`sbx prep build --check` 的输出要具体到能当反馈回路用——这是"让 AI 写 Dockerfile"能闭环的前提：

```
seeded:   node_modules 412MB · cache/go-mod 1.2GB
declared: allow +1 (gitlab.corp.com) · env +1 (GOFLAGS)
warning:  .venv 在 deps.mask 里，但镜像的 /src/.venv 不存在
```

### 2.3 契约：Dockerfile 的 WORKDIR 要长得像你的仓库

不发明新 DSL，复用已有的 `deps.mask`：

> **`WORKDIR` 下凡是命中 `deps.mask` 的目录（`node_modules` / `.venv` / `target`），播种到对应的 Task volume；镜像里的 `/sbx/cache/*` 播种到共享的 `sbx-cache`。**

契约简单是有意的：AI 只看 `go.mod` + `package.json` 就能写出合格的 Dockerfile，不需要知道 sbx 内部。

### 2.4 场景

**场景 A：内网 GitLab 上的私有 Go 模块**

```dockerfile
# .sbx/prep.Dockerfile
FROM golang:1.23

# 私有 CA：就是普通的一行 COPY，sbx 不提供专门机制
COPY .sbx/ca/corp.crt /usr/local/share/ca-certificates/
RUN update-ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
ENV GOPRIVATE=gitlab.corp.com
# 凭据用 BuildKit secret，不落层；agent 永远看不到它
RUN --mount=type=secret,id=netrc,target=/root/.netrc \
    go mod download
```

```toml
# <repo>/.sbx/sandbox.toml —— 进仓库，团队共享。只有 id，没有值
[prep]
file    = ".sbx/prep.Dockerfile"
watch   = ["go.mod", "go.sum"]
secrets = ["netrc"]

# ~/.sbx/config.toml —— 只在你本机
[prep.secret]
netrc = "~/.sbx/secrets/corp.netrc"
```

Go module cache 落在 `/sbx/cache/go-mod`（`GOMODCACHE` 已指向这里），播种进共享的 `sbx-cache`。

**场景 B：私有 npm registry**

```dockerfile
WORKDIR /src
COPY package.json package-lock.json ./
# 构建期有网有凭据，默认不跑安装脚本
RUN --mount=type=secret,id=npmrc,target=/root/.npmrc \
    npm ci --ignore-scripts
```

`/src/node_modules` 命中 `deps.mask`，播种进该 Task 的 `node_modules` volume。

**场景 C：模型权重 / 测试夹具 / 内网文档**

```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
RUN mkdir -p /sbx/cache/models \
    && curl -fsSL http://models.corp.example/bge-small.tar.gz | tar -xz -C /sbx/cache/models
```

任何能被固化成字节的东西都适用：权重、脱敏 DB dump、OpenAPI spec、内网 wiki 导出、编码规范。**这一整类的安全代价接近零**——它们是只读的不变字节，而 agent 本来就能看到同等敏感度的东西（你的源码）。

### 2.5 prep 也能声明，但权限受限

镜像里可以放一个 `/sbx/prep.json`，在播种时顺带读出来：

```json
{ "allow": ["gitlab.corp.com"],
  "env":   {"GOFLAGS": "-mod=mod"},
  "ca":    ["/usr/local/share/ca-certificates/corp.crt"] }
```

**这份 manifest 按「项目层权限」并入配置**：走现有的 `checkProject` 红线（禁凭据键、禁执行键、禁绝对路径），字段是白名单而非任意配置，`sbx config show` 里来源显示为 `prep`。理由是它和 `.sbx/sandbox.toml` 一样跟着仓库走、一样受信任确认覆盖，不该有更高权限。

注意 `allow` 只是**放行**，不等于**可达**：配了 `network.upstream` 时，squid 的 `never_direct allow all` 仍会把它交给上游代理。prep 的声明解决的是「取」之后的零星 HTTP 访问，不解决实时内网可达性。

### 2.6 新鲜度

tag = hash(prep.Dockerfile + `watch` 列出的文件内容)。解析 Dockerfile 猜不出它 `COPY` 了什么，所以让项目层明说（相对路径，是事实陈述，红线放行）。

```toml
[prep]
on_drift = "warn"   # warn（默认）| build | fail
```

默认 `warn` 不自动构建：自动构建意味着每次 `sbx run` 都可能悄悄连内网，边界就糊了。

**播种只发生在 Task 新建时**（`ensureVolumes` 里 `created == true` 的分支）。对运行中的 Task 重新播种会抹掉它自己装的东西，必须显式 `sbx prep reseed <task>` 并确认。

### 2.7 顺带的收益

没有私有依赖的人也用得上：每个新 Task 的依赖 volume 本来是空的，第一件事是重装依赖。有了种子，**`sbx run <新 task>` 从冷启动变热启动。**

### 2.8 两个坑

- **Docker Desktop 会把它设置里的代理注入 `docker build`。** `sbx prep build` 必须显式清掉 `HTTP_PROXY` / `HTTPS_PROXY`，否则构建会原样踩一次"全走代理、到不了内网"。
- **prep 镜像是信任放大器。** 它带着凭据、连着内网、还能往配置里塞声明。trust 覆盖的是 `.sbx/prep.Dockerfile` 的文本，不是它 `FROM` 的基础镜像。只能靠白名单字段 + 红线 + `sbx run` 时打印生效的 prep 声明来缓解，消除不掉。

## 3. services 与 forward：连「活的」东西

### 3.1 先看清一件事：数据库有三个安全等级

"连数据库"这个说法掩盖了三个完全不同的东西：

| 形态 | agent 被完全控制时的最坏情况 | 级别 | 机制 |
|---|---|---|---|
| 容器内伴生实例（空库 / 种子库） | 删掉一个本来就一次性的库 | 🟢 | `[[services]]` |
| 宿主机上的实例 | 删你本机的开发数据 | 🟡 | `[[forward]]` |
| 内网共享实例（staging / dev） | 删别人的数据、读真实用户数据、写操作不可逆 | 🔴 | `[[forward]]` + 只读账号 |

redis、kafka、MinIO、内网微服务同构。**判据：共享的、有真实数据的、可写的 = 红。**

取舍原则：**能用容器内实例就用容器内实例**（🟢 不需要任何洞）；够不着才用 `[[forward]]` 逐条开洞。等级的差别不在用什么机制，在**连上去之后能毁掉什么**——所以 🔴 的缓解手段是数据库账号权限，不是网络配置。

### 3.2 services：容器内伴生实例（🟢 首选）

**场景**：Go + Postgres 的后端项目，agent 接到任务第一件事是 `go test ./...`，测试连 `localhost:5432`。

不解决会怎样：全红。agent 会去试 `apt-get install postgresql`（没 root）、试 `docker run`（没 docker.sock），然后**开始改测试代码绕过数据库**。无人值守场景下它不会停下来问你——这是最糟的失败模式：不是报错退出，是悄悄产出垃圾。

```toml
# <repo>/.sbx/sandbox.toml —— 可以进仓库：没有地址、没有凭据，是"本项目要什么"的事实
[[services]]
name  = "db"
image = "postgres:16"
env   = { POSTGRES_PASSWORD = "dev", POSTGRES_DB = "app" }
port  = 5432              # 容器内以 localhost:5432 访问
```

- 服务容器接在 `t.Network()` 上。**那是个 internal 网络，所以它和 Task 之间的通信根本不需要出网路由**——这正是这一类最安全的原因。
- 生命周期跟着 Task：`sbx run` 起、`sbx done` / `sbx drop` 一并回收，走同一套 `undo` 和 label 清理。
- 镜像拉取属于「取」，可以由 prep 顺带完成，离线也能起。

### 3.3 forward：单目标 TCP 洞（🟡 / 🔴）

一个只做一件事的 sidecar，同时接 `t.Network()`（internal）和 `sbx-egress`，把容器内的一个端口定向转发到一个声明好的目标：

```
[task 容器] --localhost:5432--> [forwarder] --10.42.0.80:5432--> 内网数据库
```

**每条 `forward` 一个独立容器**，各自在 task network 上有自己的 IP。这样多条规则不会在监听端口上打架，也便于逐条回收。

**场景 A：宿主机上已经在跑的 postgres**

```toml
# ~/.sbx/config.toml 或 ~/.sbx/workspaces/<ws>.toml
[[forward]]
name = "db"
port = 5432     # 容器内 localhost:5432
to   = 5432     # 只写端口号 = 宿主机
```

`to` 写成裸端口号是宿主机的简写，对应 `socat TCP-LISTEN:5432,fork TCP:host.docker.internal:5432` 加 `host.docker.internal:host-gateway`（和 `proxy/shared.go:162` 同一写法）。

**场景 B：内网数据库——直接写地址，不需要在宿主机上打洞**

```toml
[[forward]]
name = "staging-db"
port = 5432
to   = "db.corp.example:5432"   # 或 "10.42.0.80:5432"
```

只要宿主机在公司网里（直连 LAN 或 VPN 已连），sbx-egress 网络就能到——forwarder 跑在那上面，所以容器也就能到。**不需要先 `ssh -L`**：中介点是 forwarder 本身，不是宿主机，强制走一道隧道不增加任何安全性。

> 例外：如果数据库在跳板机后面、宿主机自己也到不了，那才需要你先建隧道，然后按场景 A 的写法指向本地端口。这是网络可达性的问题，不是 sbx 的规定。

### 3.3.1 约束一：`to` 必须解析到私有地址

这是 forwarder 唯一的硬限制，理由很具体：**如果 `to` 能写任意地址，forwarder 就成了绕过 squid 的通用出网隧道**——有人写 `to = "api.example.com:443"`，这条 HTTP 流量就不再受白名单约束、不进访问日志、`sbx net denied` 也看不见。

所以 `to` 解析后必须落在私有地址空间：

- `10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`（RFC 1918）
- `127.0.0.0/8` 与宿主机网关（裸端口号的简写走这条）
- 解析到公网地址 → **拒绝启动**，并提示走 `network.allow` + squid

**在 Task 启动时解析一次，把 IP 固化进 socat 的目标**，而不是让 socat 每次连接重新解析。好处有三：行为确定、`sbx run` 能把实际 IP 打出来、避免 DNS 重绑定在运行中把目标换掉。

少数公司在内部使用公网 IP 段。这种情况下全局/工作区层可以写 `allow_public = true` 单条解除，`sbx run` 会醒目警告。不推荐，但不该让这类环境完全没法用。

### 3.3.2 约束二：只能写在全局 / 工作区层

`[[forward]]` 开的是真实的洞，**项目层不能声明**——跟着仓库走的配置不该决定你的机器往哪儿连。这和 `[[services]]` 正好相反：后者只有 image/env/port，没有地址，所以可以进仓库。

于是真正的中介点是**配置层**：每一条 forward 都是你在自己机器上做的显式决定。

### 3.3.3 可选：保留主机名与 TLS

数据库用 `localhost:5432` 就够了，但内网 HTTPS 服务改成 `localhost:8443` 会让证书 CN 对不上，应用配置也得改。加一个可选的 `host` 字段可以避免：

```toml
[[forward]]
name = "gitlab"
host = "gitlab.corp.example"   # 容器内仍用真实域名访问
port = 443
to   = "10.42.0.50:443"
```

实现是给 Task 容器加 `--add-host gitlab.corp.example:<forwarder 在 task 网络上的 IP>`。socat 是纯 TCP 中继，SNI 和证书原样透传，TLS 握手不受影响。

**但这条路绕过了 squid 的白名单和访问日志。** 所以对内网 HTTP/HTTPS 的选择顺序是：

| 需求 | 首选 | 为什么 |
|---|---|---|
| 内网 HTTP/HTTPS 服务 | `always_direct`（ADR 0017 ②） | 流量仍经 squid，白名单、策略拦截、访问日志一个不少 |
| 内网非 HTTP（数据库、redis、kafka） | `forward` | squid 根本管不了裸 TCP |
| 内网 HTTPS 且不想动 proxy 配置 | `forward` + `host` | 简单，但失去审计 |

这也是 ADR 0017 的 ② 路由没有被完全取代的原因：它和 forwarder 不是替代关系，是审计程度不同的两条路。

### 3.3.4 不变量没有改变

forwarder 不给 Task 容器默认路由。它是 Task 网络里的一个**普通 peer**，Task 连的是它，不是通过它路由出去。所以：

- Task 容器仍然没有默认路由；
- HTTP/HTTPS 的通用出网仍然只能走 squid，白名单、策略拦截、访问日志全部照旧；
- agent 能通过 forwarder 接触到的，只有你逐条声明过的那几个 `地址:端口`。

> **对内网共享库的强烈建议：用只读账号，或单独的 schema。** 无人值守 agent 对着真实 staging 库跑 migration，不是会不会发生的问题，是什么时候发生的问题。

### 3.4 预期管理要落成机制，不靠自觉

"连上去之后能毁掉什么"网络层管不了，缓解只能在服务侧（只读账号、独立 schema、权限模型、流量控制）。但**用户的预期不该只靠自觉**，要有东西托住：

- **`sbx run` 逐条打印**生效的 forward 和解析出的实际 IP，🔴 级的更显眼。
- **记进 Task meta**，让 `sbx ls` 能标出哪些 Task 接过内部服务。事后 review 分支时你就知道该用什么尺度看——这是"预期管理"的可操作形态。
- **有 🔴 级 forward 的 Workspace，建议同时切 `network.mode = "allowlist"`。** 理由见下。

### 3.5 forward 与默认 `open` 的组合

默认 `network.mode = "open"` 的风险之所以可控，前提是**沙箱里没什么值得偷的**：容器用完即弃，源码 agent 按设计本来就在读。

`[[forward]]` 接入 🔴 级资源之后，这个前提被拆掉了——沙箱里有了真实数据，而且那些数据不随 Task 销毁。**`open` + 🔴 `forward` 比两者单独存在都差一个量级。**

这不是说 forward 不该做，而是：**接触面一旦扩大，出口控制的价值就回来了。** 守住接触面比守住出口更划算，这也正是 `[[forward]]` 的约束要比 `network.allow` 严的原因——它是唯一一个在扩大接触面的机制。

## 4. 两条出口的分工

| | squid | forwarder |
|---|---|---|
| 协议 | HTTP / HTTPS（CONNECT 隧道） | 任意 TCP |
| 目标 | 白名单内的任意主机 | **仅私有地址，一条一个 `地址:端口`** |
| 粒度 | 按域名 | 按目标，一条一个 |
| 审计 | 白名单 + 策略拦截 + 访问日志 | **无**（所以必须逐条显式声明） |
| 可写层 | 任意层 | **仅全局 / 工作区层** |
| 可观测 | 访问日志、`sbx net denied` | `sbx run` 打印、`sbx ls` 可见 |
| 默认 | 始终存在 | **不配就不存在** |

两者都是桥接 internal ↔ 外部的 sidecar，Task 容器自身永远不具备出网能力。这是整个安全模型的支点，所有机制都不得破坏它。

## 5. 明确不提供

这些不是"还没做"，是**决定不做**。开源发布前必须有立场，否则用户会自己绕过去，而且意识不到代价。

| | 为什么不 | 替代做法 |
|---|---|---|
| **`/var/run/docker.sock`** | 等于把宿主机 Docker 的完全控制权交给 agent：它能起特权容器挂宿主机根目录。**比 R1 严重一个量级**——R1 要等你执行 git 命令才触发，这个是立即、直接、完全的 | 用 `[[services]]` 声明测试依赖，替代 testcontainers 的自起容器 |
| **云厂商凭据（AWS / 阿里云）** | 爆炸半径最大的一项：花钱、删资源、拖数据 | 需要云资源的操作在宿主机做 |
| **kubeconfig** | 等于生产集群的操作权 | 同上 |
| **SSH key 进容器** | agent 获得任意仓库的推送权，与 R1 同级 | 分支本来就在宿主机（worktree + `.git` 是 bind mount），`git push` 在宿主机做 |
| **`host.docker.internal` 直通** | 一旦可达，容器能连宿主机**每一个**监听端口：你其他项目的 dev server、所有本机数据库，Docker 若开了 TCP 端口就是宿主机 root。没有"只开 5432"这回事 | `[[forward]]`，一条一个端口 |
| **`forward` 指向公网地址** | forwarder 会变成绕过 squid 的通用出网隧道：不受白名单约束、不进访问日志、`sbx net denied` 看不见 | 公网访问走 `network.allow` + squid；内部用公网 IP 段的环境可用 `allow_public` 单条解除 |
| **给 Task 容器加非 internal 网卡** | 那一刻它就有了默认路由，HTTP 流量可以绕开 squid，白名单 / 策略拦截 / 访问日志全部失效 | forwarder sidecar |
| **运行期凭据注入** | 暂不提供（不是永不）。最常见的 `git push` 场景已被架构绕过，剩余需求面小 | 中途需要新私有依赖时，明确失败并提示重跑 `sbx prep build` |
| **设备 / `--gpus` / `--cap-add`** | 暂不提供。GPU 本身风险低，但需要先有一套统一的能力声明机制 | — |

## 6. 风险

| # | 风险 | 缓解 |
|---|---|---|
| R-B1 | prep 镜像带凭据连内网，又能向配置塞声明 | 声明字段白名单 + 项目层红线；`.sbx/` 受信任确认；`sbx run` 打印生效声明 |
| R-B2 | `SSL_CERT_FILE` 是替换语义，CA 处理不当会让公网全挂 | CA 由用户 Dockerfile 里 `update-ca-certificates` 处理，是追加语义；验收必须同时验私有源和公网 |
| R-B3 | forwarder 让容器能连宿主机或内网的某个端口 | `to` 必须解析到私有地址，公网拒绝启动；只能写在全局/工作区层，项目层不可声明；一条一个目标；启动时解析一次并固化 IP；`sbx run` 逐条打印实际 IP |
| R-B7 | `forward` + `host` 的内网 HTTPS 绕过了 squid 的白名单与访问日志 | 文档给出选择顺序：内网 HTTP/HTTPS 首选 `always_direct`（仍受审计），`forward` 仅用于裸 TCP 或明确接受失去审计 |
| R-B4 | `[[services]]` 可进项目层，等于项目能决定起哪些容器 | 只能声明 image/env/port，不能挂宿主机路径、不能提权；受信任确认 |
| R-B5 | 内网共享库被 agent 写坏 | 缓解在服务侧：只读账号 / 独立 schema / 权限模型。sbx 侧只负责显式、可见、可追溯（打印 + 记入 meta） |
| R-B8 | `open` + 🔴 `forward` 的组合：接触面扩大后出口不设防 | 文档规则：有 🔴 forward 的 Workspace 建议切 `allowlist`；`sbx run` 在这个组合下给出提示 |
| R-B6 | 播种覆盖 Task 已装的依赖 | 只在 volume 新建时播种；`reseed` 必须显式并确认 |

## 7. 实施分期

| 期 | 范围 | 主要改动 | 验收 |
|---|---|---|---|
| **P0** ✅ | R1 缓解（ADR 0019） | `agent.Mounts()` 叠 `hooks/` 与 `config` 只读挂载；`git.protect` 开关默认开；`checkProject` 新增"安全开关键"红线 | 容器内写 `.git/hooks/pre-commit` 和 `git config core.hooksPath` 均失败；commit 正常；项目层写 `git.protect = false` 被拒 |
| **P1** ✅ | prep 内容播种 | `cli/prep.go` 新命令；`ensureVolumes` 的 `created` 分支改为从 prep 镜像 `docker cp`；`[prep]` 配置 + 红线 | 新 Task 启动即带依赖；`--check` 报告准确；构建不继承 Docker Desktop 代理 |
| **P2** ✅ | `[[services]]` | 容器生命周期 + `undo` + `sbx done` 回收 + `sbx ls` 展示 | `go test` 能连 `localhost:5432`；`sbx done` 后无残留容器 |
| **P3** ✅ | `[[forward]]` | 每条一个 socat sidecar（镜像 pin digest），双网络，`host-gateway`；启动时解析并校验私有地址；可选 `--add-host`；`sbx run` 逐条打印；记入 Task meta | 容器内 `localhost:5432` 分别通宿主机与内网目标；`to` 指向公网时拒绝启动；`sbx ls` 能标出接过内部服务的 Task；**同时验证 Task 仍无默认路由**（HTTP 仍必须经 squid） |
| **P4** ✅ | prep 声明层 | `/sbx/prep.json` 解析 + 作为 prep 配置层 + `config show` 来源 | 越权字段被红线拒绝 |
| **P5** 🚧 | 文档表态 | §5 写进 README / walkthrough 的已知限制，与 R1 并列 | — |

P0 独立于其余各期：它修的是今天就存在的 R1，不依赖任何新机制，应当先做。P3 的验收里"Task 仍无默认路由"这一条是整份设计的底线，必须有测试，不能只靠人工确认。
