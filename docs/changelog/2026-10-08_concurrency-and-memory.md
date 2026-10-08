# 2026-10-08 · 并发上限与内存预算（M2-14）

> 承接 [2026-10-08_trust.md](2026-10-08_trust.md)。

## 一句话

`max_running` 从"能配但没人管"变成真的限流；`max_running × memory` 超过 Docker VM 内存 85% 时警告一次。

## 并发

`sbx run` 在真要启动一个容器之前数一遍正在运行的 agent 容器：

```
$ sbx run api
sbx: 已经有 3 个 Task 在跑，达到 max_running = 3：shop-e76272.main、shop-e76272.fix、blog-a1b2c3.main
先 sbx stop 掉一个，或者在 ~/.sbx/config.toml 里调大 max_running（注意内存：每个 Task 上限 3g）
```

三个决定：

**计数跨 Workspace。** `max_running` 管的是 Docker VM 的内存，而那是所有仓库共用的——在 A 仓库开满三个，B 仓库再开就该被拦住。限额本身仍然取自你当前所在仓库合并后的配置（它是四层配置里的一个标量）。

**新建和重启都要过。** design §10.1 把这一步画在"Task 不存在"的分支里，停止的 Task 直接跳到启动容器那一步。但重启一个已停止的 Task 同样多占一份内存，所以这里对两种情况都查。`sbx run` 一个**已经在跑**的 Task 只是 attach，直接放行——否则第三个 Task 会被自己挡在门外。

**数不出来就放行。** docker 调用失败时打印警告继续走，不把正常使用挡在一次临时故障上；真起不来的话后面的 docker 调用自然会报错。

## 内存预算

```
警告: max_running(3) × resources.memory(3g) = 9.0g，超过 Docker VM 内存 9.7g 的 85%。
并发跑满时可能触发 VM 级 OOM；调小 max_running 或 memory，或者把 Docker 的内存调大。
```

只警告不拒绝：`--memory` 是上限不是预留，三个 Task 同时吃满 3g 是小概率。VM 内存取自 `docker info` 的 `MemTotal`——Docker Desktop 下那是 VM 的内存，不是宿主机的。

## 阈值：从 0.85 放宽到 0.95（2026-10-09 定）

design §11 原本同时写了两件事：

- 阈值 `max_running × memory > VM 内存 × 0.85` 时警告（R10）；
- 推荐 **10GB VM + 3 × 3g**，并说"在 VM 里留有余量"。

但 `3 × 3g = 9g > 10g × 0.85 = 8.5g`——**照文档配置的人每次 `sbx run` 都会看到警告**。在这台机器上（VM 实际 9.7g）复现了，M1 的 e2e 跑一遍也刷了好几条。一个每次都响的警告等于没有警告。

当时提了三条路（VM 提到 12GB / 默认 `max_running` 降到 2 / 阈值放宽到 0.95），**决定取第三条**：改的是一个内部阈值，不动用户可见的推荐配置，也不减并发位。

0.95 下：

| 配置 | VM | 结果 |
|---|---|---|
| 3 × 3g = 9g | 10g（推荐） | 安静（≤ 9.5g） |
| 3 × 3g = 9g | 9.7g（Docker Desktop 实测） | 安静（≤ 9.2g） |
| 4 × 3g = 12g | 10g | 报警 |
| 3 × 4g = 12g | 10g | 报警 |
| 3 × 3g = 9g | 8g | 报警 |

代价写在 design §11 里：留给 squid（128m）、镜像层缓存和 VM 自身的余量变薄，这条线附近的 OOM 得靠 `sbx ls` 的 `exited(oom)` 事后发现。

## 验证

`internal/cli` 新增 12 个子测试（并发 5 种情形、预算 6 种、label 解析），`internal/config` 9 个（`MemoryBytes` 的单位和异常输入）。

手工：起一个带 `sbx.role=agent` label 的探针容器，`max_running = 1` 时被拦并点名、`= 2` 时放行；默认配置下内存警告在这台机器上如实打印（之后已删除探针容器 `sbx-fake-limit-probe`）。

## 顺带

`docker.Client` 多了 `Info()` 和 `Running()`；`ListByLabel` 的解析部分抽成 `parseItems` 给两边共用。
