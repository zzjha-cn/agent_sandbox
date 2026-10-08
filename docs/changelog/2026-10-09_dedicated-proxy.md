# 2026-10-09 · dedicated 代理、上游代理与资源分层（M2-7、M2-8、M2-15）

> 承接 [2026-10-09_api-key-and-cloud-mcp.md](2026-10-09_api-key-and-cloud-mcp.md)。这三项做完，**M2 全部完成**。

## M2-7：一个 Task 一个代理

默认所有 Task 共用 `sbx-proxy`，靠代理认证区分谁是谁。现在可以让某个 Task 用自己的 sidecar：

```bash
sbx run spike --proxy dedicated          # 只影响这一次
```

```toml
[network]
proxy = "dedicated"                      # 或者写进任意一层配置
```

```
$ docker ps --format '{{.Names}}'
sbx-shop-e76272-spike
sbx-shop-e76272-spike-proxy
```

### 两种部署收敛成一个接口

`run` / `stop` / `done` / `net` 里没有一处 `if mode == "dedicated"`。两种部署都实现 `proxy.Egress`：

```go
type Egress interface {
	Ensure() error
	AttachTask(TaskSpec) (string, error)
	DetachTask(taskID, network string) error
	StopIfIdle() (stopped bool, err error)
	AccessLog() ([]Entry, error)
}
```

各自的差别留在实现里：`Shared.StopIfIdle` 是"没有 shared Task 在跑就停掉全局代理"，`Dedicated.StopIfIdle` 是"我这个 Task 的容器停了就停掉我的 sidecar"；`Shared.DetachTask` 删片段和密码行，`Dedicated.DetachTask` 直接把容器和配置目录删掉。

### 不做代理认证

shared 必须认证——一个实例接在所有 Task 的网络上，不认证就没法区分谁在请求、也挡不住 A 冒用 B 的白名单。dedicated 没有这个问题：sidecar 只接在这一个 Task 的 internal 网络上，除了它没人连得到，所以代理地址就是干净的 `http://proxy:3128`，不用往环境变量里塞 token。

代价是 access.log 里的 `%un` 恒为 `-`。与其给 dedicated 另写一套日志解析，不如在读日志时按实例归属把 TaskID 补上——`net denied` 的筛选、分类、聚合全都不用改：

```
$ sbx net denied ded
HOST         COUNT  LAST    KIND    TASK
example.com  1      0s ago  不在白名单  fixture-9b6530.ded
```

`sbx net denied` 不带 Task 名时，会把共享代理和本 Workspace 里每个 dedicated sidecar 的日志合起来看。读不到的跳过（Task 停了它的 sidecar 也停了，这不算错）；一个都读不到才报错。

### 模式是建容器时定的

记在 `meta.json` 里。之后改配置不会把一个已经在跑的 Task 搬到另一种代理上——容器的环境变量里写着代理地址，网络也是按那个模式接的，搬过去只会搬一半。要换就 `done` 之后重建。新建 Task 时反过来只看生效配置，不理会上一轮残留的 meta。

### 实测踩到的一个 label

agent 容器上的 `sbx.proxy` label 原来是写死的 `"shared"`。这个 label 是 `Shared.StopIfIdle` 判断"还有没有 shared Task 在跑"的依据——写死之后，一个 dedicated 的 Task 会让共享代理永远停不掉：

```
$ sbx stop sh1            # 唯一的 shared Task
已停止 sh1
已停止 sbx-proxy          # 修之前这行不会出现，ded2 把它拖着
```

改成跟着实际模式写。这类 bug 单元测试照不到，是起两个 Task 混着跑才撞出来的。

### 内存

`sbx-proxy` 一共 128m；dedicated 则是**每个 Task 128m**。这部分不计入 §11 的内存预算公式（那个算的是 agent 容器），算在留给 VM 的余量里——昨天把阈值从 0.85 放宽到 0.95，这块余量本来就薄了，文档里写清楚了。

## M2-8：上游代理

`network.upstream` 渲染成 `cache_peer` + `never_direct allow all`，shared 和 dedicated 都支持（shared 下只有一个上游，要给不同 Task 配不同上游就得用 dedicated）。只接受 http——缺端口或者写成 `socks5://` 在配置校验阶段就报错，并指出是哪一层写的。Linux 上给 squid 容器加 `--add-host host.docker.internal:host-gateway`。

改了上游不用重建容器：下次 `run` 时主配置变了会自动 reconfigure。

## M2-15：资源上限分层

`resources.cpus / memory / pids` 三个字段本来就走 `bindings` 表，四层都能写——这一项缺的是验证和文档。补了一个三层各覆盖一个字段的测试，确认 `sbx config show` 的来源列指得对，非法值的报错也能指出是哪一层写的。

项目层也能写这三个字段：它们不是 M2-2 的红线字段（不含 key/token/secret，也不是绝对路径），而且 `.sbx/` 的任何改动都要过信任确认才会生效。

## 验证

- `go build` / `go vet` / `go test ./...` 全绿；新增 proxy 的 golden + 形状测试、cli 的代理选择测试、config 的分层测试。
- `go test -tags docker -run TestIntegrationDedicated`：真起一个 sidecar，验证白名单放行、名单外 403、云端 MCP 策略拦截 403、热加载后放行、日志带 TaskID、没有 407、`StopIfIdle` 后能再起来、`DetachTask` 之后容器和配置目录都没了。
- 真机跑了一轮完整流程：`run --proxy dedicated` → `stop`（sidecar 跟着停）→ `run`（起回来）→ `net denied` → `net allow --project`（热加载后 200）→ `done`（容器、网络、state 无残留）。再混着起一个 shared Task，确认两边互不干扰、`sbx-proxy` 该停的时候停。

## M2 完成

M2-1 到 M2-15 全部完成。剩下的都在 M3：Profile（py-rust、自定义镜像）、Codex、headless 模式、`sbx doctor`、`sbx port`、发布物和 CI。
