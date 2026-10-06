# M0-2 结果：macOS bind mount 的读写性能

- 日期：2026-10-04
- 环境：macOS 13.0 arm64，Docker Desktop 27.4.0（**VirtioFS**，VM 8C / 8GB）；宿主机 node 20.19、go 1.27；容器 node 22、go 1.27.1
- 样本：create-next-app 生成的 Next.js 16.3 项目、gin（`--depth 1`）
- 复现：`./bench.sh`（每个场景先预热一轮填充包缓存，记录的是第二轮耗时）

## 结论：✅ 只遮盖依赖目录就够用，Docker Desktop + VirtioFS 可以作为默认，不需要换 OrbStack

| 场景 | 耗时 | 和宿主机相比 |
|---|---|---|
| Next.js 宿主机原生（`npm ci` + `next build`） | 17.6s | 基准 |
| Next.js 容器 · **node_modules 在 volume** | 18.6s | **+6%** |
| Next.js 容器 · node_modules 在 bind mount | 31.7s | +80% ⚠️ |
| gin 宿主机原生（`go test ./...`） | 3.7s（另一轮 3.0s） | 基准 |
| gin 容器 · 源码在 bind mount，模块和构建缓存在 volume | 1.3–1.8s | 更快 |
| `git status` ×20 宿主机 | 0.2s | 基准 |
| `git status` ×20 容器（bind mount） | 0.5s | 每次多约 15ms，可以忽略 |

- **ADR 0008 得到验证**：依赖目录必须用 volume 遮盖，否则安装和构建慢 80%。源码本身留在 bind mount 上，性能可以接受。
- R3（macOS IO 性能）风险**解除**。OrbStack 不再需要作为必要的对照，留作可选项。

## 对实现的补充

1. **缓存路径要按镜像确认**：官方 `golang` 镜像的 `GOMODCACHE` 是 `/go/pkg/mod`，不是 `~/go`。第一次测试时 volume 挂错了位置，结果每次都在重新下载。Agent 层应该**显式设置**这些环境变量，让它们指向固定的共享 volume 路径：`GOMODCACHE`、`GOCACHE`、`npm_config_cache`、`PIP_CACHE_DIR`、`UV_CACHE_DIR`、`CARGO_HOME`，不要依赖各个镜像的默认值。
2. **非 root 运行的必要性又多了一条证据**：gin 的 `TestSaveUploadedFileWithPermissionFailed` 在容器里以 root 运行时**必然失败**（root 不受文件权限限制）。Agent 用 root 跑测试，会得到和宿主机不一样的结果。Agent 层必须使用非 root 的 `agent` 用户（§5.3 已有这条设计，这里是佐证）。
3. **`.next` 等构建产物**：这次测试中 `.next` 写在 bind mount 上，开销包含在 18.6s 里，可以接受。不强制遮盖，但 web-go Profile 的默认遮盖列表里可以考虑加上 `.next`，减少写回宿主机的文件。
