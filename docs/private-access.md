# 私有资源接入（Private Access）设计与实施

> 状态：**已被 [boundary.md](boundary.md) / [ADR 0018](private/adr/0018-boundary-and-prep.md) 部分取代**——①放行、③信任、④凭据改由 prep 在宿主机侧固化承担，本文仅 §3.1 的 ②路由（`always_direct`）仍然有效，作为「实时内网可达」的窄逃生口。
> 原状态：设计稿（2026-10-10），尚未实现。决策见 [ADR 0017](private/adr/0017-private-access.md)，涉及 [design.md](design.md) §6（网络）、§9（配置）。

## 1. 问题归类

### 1.1 现象

下面这些都是同一类问题的不同表现：

- 内网 GitLab 上的 Go module 在容器里拉不下来，宿主机上可以。
- 私有 npm registry / PyPI 私服需要 token 才能读。
- 公司有 MITM 代理或自签 CA，容器里 TLS 握手直接失败。
- 内网 Maven 私服、Docker registry 开在 8443 这类非 443 端口。
- 依赖只能通过 SSH 协议访问（`git@host:org/repo.git`）。

### 1.2 共同点

**目标资源在宿主机的信任域内可达，在沙箱的信任域内不可达。** 沙箱为了隔离而切断的东西（直连出网、宿主机 CA、宿主机凭据、非 HTTP 协议），恰好是这些资源所依赖的东西。

这不是某个域名的 bug，是沙箱边界与企业内网边界不重合导致的结构性缺口。只要 sbx 要被公司内部的人用，这类问题就会反复出现，所以它需要一个一等公民的抽象，而不是逐个打补丁。

### 1.3 四个正交维度

任何一个具体案例，都是下面四件事的组合，外加一条贯穿其中的协议约束：

| 维度 | 要回答的问题 | 当前机制 | 现状 |
|---|---|---|---|
| **① 放行** | 允不允许访问这个目标 | `network.allow`、squid `dstdomain` | ✅ 已有 |
| **② 路由** | 流量从哪条路出去（上游代理 / 直连） | squid `never_direct allow all` | ❌ **缺**：一刀切，没有例外 |
| **③ 信任** | TLS 证书认不认 | 容器内只有公共 CA bundle | ❌ **缺**：没有注入点 |
| **④ 凭据** | 用什么身份访问 | 只有 `agents.*.api_key_env/file` | 🟡 没有通用机制 |
| 协议约束 | 走不走得了 squid | 只允许 `CONNECT` 到 443 | 🟡 端口写死 |

现状的两个硬伤值得单独点名：

- `squid.conf.tmpl` / `dedicated.conf.tmpl` 里，配了 `network.upstream` 就会渲染 `never_direct allow all`。**所有**流量被强制交给上游代理，而上游代理通常是科学上网用的，到不了内网。于是"宿主机能拉、容器拉不到"成了必然，而不是偶然。
- `http_access deny CONNECT !SSL_ports` 中 `SSL_ports` 固定是 443。非 443 的 HTTPS 私服、以及 SSH 这类非 HTTP 协议，被整类排除在外。

### 1.4 一个实测案例的定位

`gitlab.cpp32.com`（内网 10.10.16.50，自签 `EternityX Private CA`，仓库匿名可读）同时踩中 ② 和 ③，没踩 ① 和 ④。换一家公司可能是 ①+④（私有 npm + token），也可能四个全中。这正是要按维度建模而不是按案例修补的理由。

## 2. 设计

### 2.1 核心抽象：具名的"私有源"

四个维度不该是四个散落的开关。现实中它们总是绑在一起描述同一个东西——"我公司有个 GitLab，在内网，自签证书，要 token"。所以抽象成一个具名对象，一条配置同时覆盖四个维度：

```toml
[[network.private]]
name   = "corp-gitlab"
hosts  = ["gitlab.cpp32.com", ".corp.internal"]  # ① 放行：域名，. 开头为通配
nets   = ["10.10.0.0/16"]                        # ① 放行：IP 段
direct = true                                    # ② 路由：不走 upstream，直连
ports  = [443, 8443]                             # 协议：允许 CONNECT 的端口
ca     = ".sbx/ca/corp.crt"                      # ③ 信任：追加的私有 CA
netrc  = "~/.sbx/secrets/corp.netrc"             # ④ 凭据：注入容器的 ~/.netrc
```

所有字段都可省略。只写 `hosts` + `direct` 就是纯路由例外；只写 `ca` 就是纯信任注入。

用户描述的是"我有这么个源"，而不是去四处拼配置——这是这个设计的主要收益。

### 2.2 字段语义

| 字段 | 类型 | 语义 |
|---|---|---|
| `name` | string | 必填，合并时的主键。只用于标识，不影响行为。 |
| `hosts` | []string | 域名。语义与 `network.allow` 一致（`.` 开头为通配）。自动并入白名单。 |
| `nets` | []string | CIDR 或裸 IP。必须用 squid 的 `dst` acl——`dstdomain` 对裸 IP 不生效，这是个容易踩的坑。 |
| `direct` | bool | 配了 `upstream` 时，这些目标仍然直连。默认 `false`。 |
| `ports` | []int | 额外允许 `CONNECT` 的端口，只对本条的 `hosts`/`nets` 生效，不放宽全局。 |
| `ca` | string | PEM 证书路径。追加到容器的 CA 集合，不替换。 |
| `netrc` | string | `.netrc` 格式文件路径，只读注入容器的 `~/.netrc`。 |

### 2.3 分层归属：红线的自然延伸

这是整个设计里最需要推敲的部分，也是它能不能称得上"规范"的地方。四个维度的**信任属性不同**，正好映射到已有的项目层红线（`config/project.go`：禁止凭据类键、执行类键、绝对路径）。

| 字段 | 项目层 `<repo>/.sbx/sandbox.toml` | 理由 |
|---|---|---|
| `name` / `hosts` / `nets` / `ports` / `direct` | ✅ 允许 | 这是"本项目依赖什么"的事实，跟着仓库走、团队共享才合理。不含秘密。 |
| `ca` | ✅ 但只允许仓库内相对路径 | **CA 证书是公开信息，不是秘密。** 放 `.sbx/ca/corp.crt` 进仓库，天然落入信任确认（ADR 0010）的覆盖范围：谁想换掉你的信任根，`sbx run` 会停下来让你看 diff。绝对路径（`~/...`、`/...`）由现有 `absPath` 红线自动拦住。 |
| `netrc` | ❌ 禁止 | 凭据。现有 `secretKey` 红线已经能拦住（键名含 `netrc` 需加进正则），`absPath` 是第二道。 |

于是形成一条清晰的分工：**项目说"我需要什么"，用户说"我的凭据在哪"**。这是结构性保证，不是靠文档提醒。

### 2.4 合并语义

`[[network.private]]` 是 TOML 表数组，是配置系统里第一个非标量、非字符串列表的类型，合并规则必须明确定义：

- **以 `name` 为主键跨层归并。** 同名的条目合并成一条，不同名的并存。
- 条目内：列表字段（`hosts`/`nets`/`ports`）取并集，标量字段（`direct`/`ca`/`netrc`）后层覆盖前层。
- 这与现有规则（列表并集、标量覆盖）一致，只是下沉了一层。

典型用法——项目层声明依赖，全局层补齐环境：

```toml
# <repo>/.sbx/sandbox.toml（进仓库，团队共享）
[[network.private]]
name  = "corp-gitlab"
hosts = ["gitlab.cpp32.com"]
ca    = ".sbx/ca/corp.crt"

# ~/.sbx/config.toml（只在本机）
[[network.private]]
name   = "corp-gitlab"
direct = true
netrc  = "~/.sbx/secrets/corp.netrc"
```

`sbx config show` 需要能展示归并后的结果和每个字段的来源层；表数组的来源标注按 `network.private.<name>.<field>` 这样的点号 key 记录。

### 2.5 与现有机制的关系

- **放行**：`hosts` 自动并入白名单，等价于写进 `network.allow`，不需要用户写两遍。
- **策略拦截层**（ADR 0015）优先级不变，仍然高于一切放行。私有源不能用来绕过 `policy-block`。
- **`network.mode = "open"`**：`direct`、`ca`、`ports`、`netrc` 在 open 模式下照样生效。它们解决的是路由、信任、协议和身份，不是放行——这是和 `network.allow` 的本质区别。
- `sbx net allow` 保持不变，仍然只写 `network.allow`。私有源是手写配置，不提供命令式写入（它有多个字段，命令行表达不清楚）。

## 3. 实现

### 3.1 ① + ②：放行与路由（squid）

两个模板都要改。关键是 **`always_direct` 必须出现在 `never_direct` 之前**，否则不生效：

```squid
# squid.conf.tmpl / dedicated.conf.tmpl
acl SSL_ports port 443{{range .ExtraPorts}} {{.}}{{end}}
acl CONNECT method CONNECT
http_access deny CONNECT !SSL_ports
include /etc/sbx/tasks/*.conf
http_access deny all
{{- if .UpstreamHost}}

cache_peer {{.UpstreamHost}} parent {{.UpstreamPort}} 0 no-query no-digest default
{{- if .HasDirect}}
acl sbx_direct dstdomain "/etc/sbx/direct/domains.txt"
acl sbx_direct_net dst "/etc/sbx/direct/nets.txt"
always_direct allow sbx_direct
always_direct allow sbx_direct_net
{{- end}}
never_direct allow all
{{- end}}
```

文件布局沿用 `allow/` `block/` 的既有做法（`internal/proxy/shared.go` 的 `s.path(...)` + `fsutil.AtomicWrite`）：

```
~/.sbx/proxy/
  direct/domains.txt    # 一行一个域名
  direct/nets.txt       # 一行一个 CIDR
```

- **粒度**：`allow`/`block` 是按 TaskID 分文件的，`direct` 是**全局一份**。路由是网络层属性，不区分 Task 身份；按 Task 分会让 `always_direct` 的 acl 组合爆炸，收益为零。dedicated 模式下每个 sidecar 自己一份，同理。
- **热加载**：与 `allow` 走同一条路径（`AttachTask` 后 `squid -k reconfigure`）。
- **shared 模式的副作用**：`direct` 是全局生效的，A 项目配的直连目标，B 项目的 Task 也会直连。但直连本身不放行——B 要访问还得过 ①。可接受，但要在文档里写明。

### 3.2 协议：CONNECT 端口

`ports` 并集后渲染进 `acl SSL_ports`。两点约束：

- 端口只放宽 `SSL_ports`，具体目标仍由 ① 的白名单控制，不会变成"任意主机的 8443 都能连"。
- 校验时拒绝 `ports` 里出现 22：**sbx 不通过 squid 隧道 SSH**，理由见 §5。

### 3.3 ③：私有 CA 注入

**不进镜像。** 镜像 tag 是输入哈希（`image.Inputs`），把 CA 烤进去会让每个有私有 CA 的用户都得到一个独立镜像，而且换证书就要重建。改成运行时注入：

1. `sbx run` 把各条 `ca` 指向的 PEM 只读挂进容器的 `/sbx/ca/<name>.crt`。
2. `entrypoint.sh` 把系统 bundle 和这些 PEM 拼成一份合成 bundle：
   ```sh
   if ls /sbx/ca/*.crt >/dev/null 2>&1; then
     cat /etc/ssl/certs/ca-certificates.crt /sbx/ca/*.crt > /sbx/gen/ca-bundle.crt
     export SSL_CERT_FILE=/sbx/gen/ca-bundle.crt
     export GIT_SSL_CAINFO=/sbx/gen/ca-bundle.crt
     export CURL_CA_BUNDLE=/sbx/gen/ca-bundle.crt
     export REQUESTS_CA_BUNDLE=/sbx/gen/ca-bundle.crt
     export CARGO_HTTP_CAINFO=/sbx/gen/ca-bundle.crt
     export NODE_EXTRA_CA_CERTS=/sbx/gen/ca-bundle.crt
   fi
   ```
3. 这些变量同时写进 tmux 会话的环境，保证容器里新开的 shell 也带着。

**为什么不用 `update-ca-certificates`**：entrypoint 以 `agent` 身份运行（Dockerfile 末尾 `USER agent`），没有 root，写不了 `/usr/local/share/ca-certificates`。合成 bundle 写在 `/sbx/gen`，这个目录在镜像里已经 `chown` 给 agent 了，不需要任何权限调整。

`SSL_CERT_FILE` 是替换语义，所以必须拼上系统 bundle，否则公网全挂——这是最容易出错的一点，要有测试覆盖。

### 3.4 ④：凭据

`netrc` 指向的文件以 `0400` 只读挂到容器的 `~/.netrc`。`.netrc` 是覆盖面最广的凭据格式：git、curl、Go（`GOPROXY` 走 HTTPS 时）、Python 都原生认它，不需要 per-生态的适配。

只支持 HTTPS 场景。token 放在宿主机文件里，容器只读，不进镜像、不进仓库、不进 volume。

### 3.5 诊断

- `sbx doctor` 新增一项 `private-access`：对每条私有源，从容器内探一次连通性和 TLS（复用 `checkUpstream` 的 `docker run --rm` 做法），把"没配 direct 但配了 upstream"这种典型错配直接报出来。
- `sbx net denied` 在列出被拒目标时，若该目标命中某条私有源的 `hosts`，提示"已声明为私有源但仍被拒"，指向 mode / 拼写问题。

## 4. 分期与验收

| 期 | 范围 | 改动 | 验收 |
|---|---|---|---|
| **P1** | ② 路由 | `config`（结构体 + 合并 + 校验 + 红线）、两个 `.conf.tmpl`、`proxy/shared.go` + `dedicated.go` 渲染 `direct/` | 配了 `upstream` 时，`direct` 目标走直连、其余走上游；`sbx config show` 正确显示来源层；项目层写 `netrc` 被拒 |
| **P2** | ③ 信任 | `agent/agent.go` 挂载、`entrypoint.sh`、tmux 环境 | 容器内 `curl https://<私有源>` 成功；**同时** `curl https://api.anthropic.com` 仍然成功（防 `SSL_CERT_FILE` 覆盖） |
| **P3** | 协议 | `SSL_ports` 模板化 + 校验拒绝 22 | 非 443 私服可达；未声明端口仍被拒 |
| **P4** | ④ 凭据 + 诊断 | `netrc` 挂载、`doctor` 新检查、`net denied` 提示 | 需要认证的私服可达；`doctor` 能识别"配了 upstream 没配 direct" |

P1 是其余各期的前提：没有 `always_direct`，连宿主机上的缓存代理（`host.docker.internal`）都会被塞给上游代理。

## 5. 非目标

- **不内置 SSH key 注入，也不做 ssh-agent 转发。** 技术上可行（squid `CONNECT` 22 + `ProxyCommand`，实测网络层能到认证阶段）。但凭据一旦进容器，无人值守的 Agent 就拥有了你的推送权限，与 R1（`.git` 可写挂载）同级。开源工具应当提供 HTTPS + `.netrc` 这条干净的路并说明理由，而不是提供一个危险的便利开关。需要 SSH 的，用 `url.<base>.insteadOf` 改写成 HTTPS。
- **不内置各生态的缓存代理**（Athens / verdaccio / devpi / Nexus）。但文档里要把"宿主机或 sidecar 跑一个缓存代理，容器只跟它说 HTTP"列为推荐模式：它一次性解决 ③④ 和协议问题，凭据根本不进容器。sbx 要做的只是让这条路走得通，也就是 P1。
- **不做 TLS 终止、不做证书固定。** squid 只做 `CONNECT` 隧道，不解密。

## 6. 风险

| # | 风险 | 缓解 |
|---|---|---|
| R-PA1 | `direct` 让部分流量绕过上游代理，可能绕过公司侧审计 | 只对显式声明的目标生效；访问日志照常记录；`sbx config show` 可见 |
| R-PA2 | 注入 CA 扩大容器的信任面，恶意 CA 可对任意站点做 MITM | 项目层只能用仓库内相对路径，受信任确认（ADR 0010）保护；追加而非替换 |
| R-PA3 | `.netrc` 以明文存在于容器内，Agent 可读 | 只读挂载、`0400`、仅限显式声明；不进 volume、不进镜像 |
| R-PA4 | 表数组是配置系统第一个复杂类型，`bindings()` 的标量/列表模型容纳不下 | 单独的归并路径，不复用 `bindings()`；合并语义需要独立的单元测试 |
| R-PA5 | shared 模式下 `direct` 全局生效，跨 Workspace 可见 | 直连不等于放行，访问仍受 ① 约束；文档写明 |

## 7. 开放问题

- `sbx config show` 如何展示表数组？按 `network.private.<name>.<field>` 逐行展开最直观，但会把表撑长。
- 是否提供 `sbx net private add` 这样的命令式写入？目前倾向不提供——字段太多，手写 TOML 更清楚。
- `nets` 是否应该同时自动写进白名单？`dstdomain` 对裸 IP 不生效，白名单那侧也需要对应的 `dst` acl，否则 allowlist 模式下声明了 `nets` 仍然会被拒。倾向于一并实现，但要注意不要把 `network.allow` 的语义也改掉。
