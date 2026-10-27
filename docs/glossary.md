# 术语表

> 状态：**随讨论演进**（起草 2026-10-22）。标记 ✅ 的是代码里已经成立的定义，🚧 的是提案中、尚未确定的词。
>
> 新词在这里定下来之前不要进别的文档——[maturity.md](maturity.md) 和 [boundary.md](boundary.md) 的论证都依赖术语是稳定的。

## 已成立的词 ✅

| 词 | 定义 | 锚点 |
|---|---|---|
| **Workspace** | 一个仓库在一台机器上的沙箱上下文。id 由**仓库路径**派生，因此同一仓库在不同机器/不同路径下是不同的 Workspace | `commands.md` |
| **Task** | 一个 worktree + 一个分支 + 一个容器 + 一组 sidecar + 一个 `--internal` 网络。sbx 的调度与回收都以它为单位 | `internal/task` |
| **Sidecar** | Task 所在 internal 网络上的普通对端，**不是网关**。目前两种：squid（HTTP/HTTPS，带白名单与日志）、forwarder（单 TCP 端口 → 宿主机） | `boundary.md` §0 |
| **prep** | 把「取」变成「存」：在 build 期（有凭据、无 agent）把外部依赖固化进镜像或 volume | `boundary.md` §2 |
| **委托人（principal）** | 对 agent 行为负责的主体。今天的前提是 **机器的主人 = 委托人** | `maturity.md` §0 |
| **daemon** | 一个 Docker daemon。**sbx 的隔离单位**——不变量守的是 Task 与外界，不守同一 daemon 上 Task 之间 | `maturity.md` §5（待补） |

## 提案中的词 🚧

| 词 | 候选定义 | 未决问题 |
|---|---|---|
| **Worker** | 一台机器 + 一个 daemon + 一个常驻 sbx 进程 | 常驻进程与今天的一次性 CLI 是同一个二进制的两种模式，还是两个东西？ |
| **Node（节点）** | 拓扑中的一个可调度位置 | 与 Worker 是否严格 1:1？若 1:1，这个词就是冗余的，应当删掉一个 |
| **Master / 控制面** | 派发任务、收集事件的上游 | **是否属于 sbx 的交付范围**？还是只交付协议 + worker 侧实现？ |
| **任务信封（envelope）** | 自描述的任务输入：repo + ref + prompt + 已批准的配置快照 + 凭据取法 + 上限 | 签名与批准发生在哪一端？ |
| **租户（tenant）** | 一组共享同一份凭据与可见性的委托人 | 一个 Worker 是否恒为单租户？ |


## 执行模型的词 🚧

参照系是**批作业 + 物化产物传递**（CI pipeline / Argo Workflow），**不是流处理**。理由：任务有界、agent 不确定且不可重放、工作区是双向的。详见讨论结论。

| 词 | 候选定义 | 状态 |
|---|---|---|
| **runner** | 在沙箱里被执行的东西。V1 限定为 **agent runner**，按 claude + codex 两个真实实现切，带能力声明位（有无会话恢复、退出怎么检测、要不要就绪等待） | 入场 ✅，接口形状待定 |
| **source / workspace** | 任务的输入。今天硬编码为 git worktree，而它同时也是 sink——**两者不是两端，是同一个工作区的两个视角** | 待拆 |
| **sink** | 产物的交付通道。git sink（分支 + push，push 由宿主机侧做，见 [ADR 0019](private/adr/0019-git-readonly-mounts.md)）、文件夹 / volume sink | 入场取决于非代码场景，**是「能否走出代码场景」的开关** |
| **完成判据** | sink 定义的「这个任务算做完了」。git sink = 有 commit；文件夹 sink = ？**没有判据的 sink 只是一个挂载点** | 未定，抽象能否立住的关键 |
| **builder** | 沙箱初始化行为 | ❌ **不入场**：`prep` 已是唯一实现，改名无收益 |
| **edge** | **前一个 task 的 sink 成为后一个 task 的 source**——编排期的数据依赖，**不是运行期的网络通道**。两个 task 不同时活着，不需要在 internal 网络上打洞 | 形态已定，优先级未定 |

**不属于 edge 的：**到 master 的连接。它属于 worker 进程，容器不应知道 master 存在（[ADR 0020](private/adr/0020-worker-mode.md) 推论 2）。

**sbx 里唯一真正的「流」是事件流**（上报）：单向、无界、可丢、消费者在远端。流处理的直觉用在控制面成立，用在数据面不成立。

## 撞名告警

「**Task**」在今天的 sbx 里已有严格定义（见上）。集群语境里说「daemon 中存在多个 task」如果指的是同一个东西，就**不要另造词**；如果指的是「master 派下来的工作项」（可能一个工作项 → 一个 Task，也可能 → 多个），那它是另一个概念，必须另起名（建议 **Job**），否则后面每一句话都要靠上下文消歧。
