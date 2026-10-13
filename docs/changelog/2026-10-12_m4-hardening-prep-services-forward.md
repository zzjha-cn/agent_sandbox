# 2026-10-12 · M4：把隔离补严，再把沙箱从「能跑 agent」推到「能跑项目」（M4-1…M4-17）

> 承接 [2026-10-09_unattended.md](2026-10-09_unattended.md)。M3 解决的是「整夜跑完，第二天知道结果」；M4 解决的是**真实项目根本跑不起来**的那几件事——测试要连数据库、依赖在私有源上、服务在内网——同时把 R1 这个一直挂着的缺口补上。
>
> 完成标准：在一个带私有依赖和数据库的真实仓库上，`sbx prep build` 之后 `sbx run` 能直接跑通 `go test ./...`；同时容器内无法写 `.git/hooks`，Task 仍然没有默认路由。

## 一条贯穿全篇的不变量

M4 加的每一样东西都在往 Task 的网络里塞 peer，所以先把话说死：

> **Task 容器始终没有默认路由。** HTTP/HTTPS 的通用出网只能经 squid。`[[services]]`、`[[forward]]` 没有一条给 Task 自己多插网卡——它们都是 Task 网络上的普通 peer。

这条现在有自动化回归测试盯着（`go test -tags docker ./core/internal/cli/ -run NoDefaultRoute`）：搭出 service + forwarder + 上游的完整拓扑，断言 Task 的 `/proc/net/route` 里没有默认路由、够不着公网、而两个声明过的 peer 照常可达。**做了反向验证**——给 Task 接上第二张非 internal 网卡，两条断言都会失败，证明它真的在测东西。

## 1. 加固（M4-1…M4-5）

| 项 | 改了什么 |
|---|---|
| M4-1 | `.git/hooks` 和 `.git/config` 以**只读**盖在可写的 `.git` 之上，submodule（含嵌套）一并处理 |
| M4-2 | `git.protect` 开关，并引入**安全开关键**这个新的红线类别 |
| M4-3 | agent 容器补 `--log-opt max-size=10m`——proxy 全有，唯独跑最久、输出最多的那个漏了 |
| M4-4 | 补 `--memory-swap`，和 `--memory` 取同值 |
| M4-5 | `doctor` 增加 `disk` 一项，只报告不自动删 |

### R1 从「已知缺口」变成「已缓解」

README 里那段写了很久的话——"`.git` 可写意味着 agent 能埋一个 git hook，之后以你的身份在宿主机上执行"——现在只剩一半成立。仓库必须可写（沙箱里的提交能直接出现在宿主机的 `git log sbx/<task>` 里，靠的就是这个），但"写一下就能在宿主机执行代码"的两个口子堵上了：

- 埋 `.git/hooks/pre-commit` → `Read-only file system`
- `git config core.hooksPath /tmp` → 同上

身份走 `GIT_AUTHOR_*` 环境变量，所以 `git commit` 完全不受影响（实测 worktree Task 和 main Task 各跑一遍）。剩余风险是改写 `sbx/<task>` 的历史，以及你自己的工具从 `.git` 读的、hooks 和 config 以外的东西——**是已缓解，不是已消除**。

### 安全开关键：红线的第四类

原有三条红线（`secretKey` / `execKey` / `absPath`）都是**按键名的形状猜**。`git.protect` 它们一条都匹配不上，而项目层显然不该有权关掉一个保护机制。所以加了一份**显式登记的清单**，按完整的点号 key 精确匹配：

```go
var safetyKeys = map[string]bool{
	"git.protect":   true,
	"prep.on_drift": true,
}
```

`prep.on_drift` 被登记进来是**对 boundary.md 的一处有意偏离**：文档把它放在项目层，但 `on_drift = "build"` 会让 `sbx run` 在依赖漂移时自动构建——也就是自动连一次内网。跟着仓库走的配置不该有这个决定权。

### `--memory-swap` 挖出一个真回归（而且是我自己埋的）

Docker 不传 `--memory-swap` 时默认给 2×，所以 `memory = "3g"` 的 Task 实际能用 6g，`doctor` 的内存预算差了一倍。补上之后 agent 容器对了，但**squid 启动即 OOM**：

```
--memory 128m --memory-swap 128m  →  exited exit=137 oom=true
--memory 128m                     →  running
```

squid 默认 `cache_mem` 就要 ~110MB，128m 禁掉 swap 根本活不下来。我自己机器上没发作，是因为 `sbx-proxy` 是改动之前建的——**任何人只要它被重建一次，`sbx run` 就直接起不来**。是集成测试逮到的，不是人眼。

修法是把 shared / dedicated 两处改成 256m，并写清楚这不是放宽：改之前实际能用的一直是 128m 内存 + 128m swap，总量 256m。现在是 256m 内存、零 swap——**总量没变，只是从算不准的变成算得准的**，而 `doctor` 的预算本来就该按这个数算。

教训记在这儿：M4-4 的完成标准只写了"agent 容器的 `MemorySwap == Memory`"，没写"其他带内存上限的容器还活不活得下来"。改一个**全局生效**的 spec 时，验收标准不能只盯着触发这次改动的那个容器。

## 2. prep：把「取」变成「存」（M4-6…M4-9、M4-15）

依赖在公司私有源上，而 Task 只有一条经 squid 的 HTTP 出口。**给沙箱开内网权限是最贵的解法**——凭据要进容器，接触面一下子铺开。

换个方向：把「取」挪到宿主机上做。

```
.sbx/prep.Dockerfile ──build（有网有凭据）──> sbx/prep-<ws>:<hash> ──cp──> Task volume
```

```bash
sbx prep init             # 生成带注释的模板，契约写在注释里（也是给 agent 的 ground truth）
sbx prep build --check    # 只构建 + 报告，不碰任何 volume
sbx prep build
```

- **凭据走 BuildKit secret**：只在那一条 `RUN` 的挂载期间可见，不落进任何镜像层（实测 `docker history` 里也没有）。
- **tag = `hash(prep.Dockerfile + watch 列出的文件内容)`**，所以"这个 tag 的镜像不存在"就等于"漂移了或者从没构建过"，不需要另记状态。
- **默认 `on_drift = "warn"`**，不自动构建：自动构建意味着每次 `sbx run` 都可能悄悄连一次内网，边界就糊了。

### 清代理要堵两条路，只清环境变量是无效的

Docker Desktop 会把设置里的代理注入 `docker build`，不管掉的话构建会原样重演"全走代理、到不了内网"。关键是它**不是通过进程环境注入的**：

| 注入路径 | 怎么堵 |
|---|---|
| `~/.docker/config.json` 的 `proxies`——**CLI 自己转成 `--build-arg`** | 显式传同名的空 `--build-arg` 盖住 |
| 进程环境里的 `HTTP_PROXY` 等——影响 CLI 自己的出站连接 | 置空后再执行 |

我第一次"验证通过"是假的：那台机器两条注入路都没开，测了个寂寞。用隔离的 `DOCKER_CONFIG` 重测才有意义——同一份配置下 `docker build` 拿到 `http://desktop-injected:7890`，`sbx prep build` 拿到空。

### 共享缓存不能照搬「只在 volume 新建时播种」

依赖 volume 的规则很清楚：只在新建时播种，不覆盖 Task 已经装好的东西。但 `sbx-cache` 是**全局的**——装过一次 sbx 的人它早就存在了，照搬的话 go-mod / npm 这类缓存种子对老用户**永远不生效**，而这恰好是 M4 完成标准里那条 `go test ./...` 依赖的东西。

缓存和依赖目录性质也不同：`cp -a` 进子目录是**合并**不是覆盖，重播不会抹掉谁装的东西。所以它按 tag 变化重播，标记写在 `sbx-cache/.sbx-prep/<ws>`——放 volume 里而不是宿主机 state 目录，volume 被删时标记跟着没，不会错判。

这条是观察出来的，不在清单里。

### prep 的声明层（M4-15）

镜像里可以放一份 `/sbx/prep.json`，字段只有 `allow` / `env` / `ca`，**按项目层权限并入配置**——走同一套红线，`sbx config show` 里来源显示为 `prep`。

两个决定：

- **排在最后合并，但不覆盖任何文件层写过的键。** 顺序在后不等于优先级更高：你在 `~/.sbx` 里亲手写下的值，不该被一个镜像里的 JSON 顶掉。
- **`allow` 记进 Task meta。** 否则 `resume` 一次就得再起一个探测容器把镜像读一遍，而镜像万一被删掉，那几条白名单会悄悄消失。

实现上 `prepRun` 拆成了 `prepLoad` / `prepSeed`：`allow` 必须赶在配 squid **之前**生效，而播种必须在建 volume **之后**，两件事中间夹着好几步。

## 3. services 与 forward（M4-10…M4-14）

先把"连数据库"这句话拆成三个安全等级：

| 形态 | agent 被完全控制时的最坏情况 | 级别 | 机制 |
|---|---|---|---|
| 容器内伴生实例 | 删掉一个本来就一次性的库 | 🟢 | `[[services]]` |
| 宿主机上的实例 | 删你本机的开发数据 | 🟡 | `[[forward]]` |
| 内网共享实例 | 删别人的数据、读真实用户数据、写操作不可逆 | 🔴 | `[[forward]]` + 只读账号 |

**`[[services]]` 可以进仓库**（只有 image/env/port，没有地址），**`[[forward]]` 只能写在全局 / 工作区层**（它决定你这台机器往哪儿连）。真正的中介点是**配置层**。

### 不是 `localhost`，是服务名

boundary.md 写的是"容器内以 `localhost:5432` 访问"，实现出来是 **`db:5432`**。这是有意偏离，理由是另外两条路更糟：

- 让服务共享 Task 的 netns（`--network container:`）—— 对 forward 直接死掉，forwarder 那样就出不了网了；
- 在 Task 里再跑一个 socat —— 往 agent 容器里塞进程。

统一成按名字访问，`sbx run` 每次把实际地址打出来。落地时唯一的摩擦就是测试里那行连接串。

### `to` 必须解析到私有地址

forwarder 唯一的硬限制，理由很具体：**`to` 能写任意地址的话，它就成了绕过 squid 的通用出网隧道**——不受白名单约束、不进访问日志、`sbx net denied` 也看不见。

```console
$ sbx run demo
sbx: opening the declared forwards: forward tunnel: example.com resolves to 104.20.23.154,
which is a public address. ...
```

两个实现时才想清楚的点：

- **`to = "127.0.0.1:..."` 要改指宿主机。** 在 forwarder 容器里 `127.0.0.1` 指的是 forwarder 自己，照字面转发会得到一个连不上的洞。
- **resume 时不重建已经在跑的 forwarder。** `--add-host` 只能在建容器时给，重建会换 IP，Task 的 `/etc/hosts` 当场变陈。真被重建过时核对一次再提示。

### 预期管理落成机制

"连上去之后能毁掉什么"网络层管不了，缓解只能在服务侧（只读账号、独立 schema）。sbx 能做的是让它显式、可见、可追溯：`sbx run` 逐条打印实际地址、`sbx ls` 多一列 `LINKS`、记进 `meta.json`。`fwd` 非 0 的分支是接过真实内部服务跑出来的——**事后 review 该用不一样的尺度看**。

## 4. 顺手挖出一个休眠的红线旁路

`walkTOML` 只认 `[]any`，而 BurntSushi 把数组表解成 `[]map[string]any`——整棵 `[[...]]` 会掉进 default 分支当标量跳过，**里面的键一条红线都走不到**。

M4 之前没有任何配置用数组表，所以这个洞一直是休眠的。`[[forward]]` 一落地它就会直接让"项目层不可声明"失效——而且不会报错，只会悄悄放行。补了分支并加了回归测试。

另外 `services.env.*` 要按前缀豁免形状红线：`POSTGRES_PASSWORD = "dev"` 是在给一个用完即弃的空库设密码，不是从宿主机取凭据。不豁免的话 boundary.md 自己的示例写法会被自己的红线拦下。

## 5. 文档表态（M4-16）

把「明确不提供」写进 README（中英）和 walkthrough 的已知限制。**这是零代码成本但不可省的一项**：不写，第一个跑 testcontainers 的用户就会自己挂 `docker.sock`，而且意识不到代价。

同时 walkthrough 加了三幕——连数据库、装私有依赖、够内网——它们正好是 M4 存在的理由。

## 验收

- `go vet ./...` 干净；`go test ./...` 全绿。
- `go test -tags docker -p 1 -count=1 ./...` 12 个包全绿（并行跑会因为 Docker 争用出假阳性，串行才是准的）。
- 实机走完一遍：services 起落回收、forward 两种目标 + `host` 字段的 TLS、公网目标被拒且 undo 清干净、prep 的播种 / 漂移 / reseed / 声明层 / CA 入信任库。

## 留下的

- `--cap-drop ALL` + `--security-opt no-new-privileges` 仍然待定：agent 以非 root 运行、`CapEff` 实测为 0，但 `CapBnd` 还是默认全集且镜像里有 8 个 setuid root 二进制。改动约两行。
- `[[services]]` 不计入 `doctor` 的内存预算：服务容器不设内存上限（给 postgres 猜一个小数字的后果是启动即 OOM，比不限更糟），而 `vm-memory` 只算 `max_running × resources.memory`。
- `forward` 没有审计，这是机制本身的代价，补偿只能靠"逐条声明 + 每次打印 + 记进 meta"。
