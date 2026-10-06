# M0-3 结果：Squid 串联宿主机代理

- 日期：2026-10-04
- 环境：macOS 13.0 arm64，Docker Desktop 27.4.0（VirtioFS，VM 8C / 8GB），`ubuntu/squid` 6.13
- 复现：`./run.sh`

## 结论：✅ 通过，设计不变

| 检查项 | 结果 |
|---|---|
| 宿主机代理的类型 | `127.0.0.1:7890` 是 **HTTP/SOCKS 混合端口**（Clash 类）。macOS 系统代理的 HTTP、HTTPS、SOCKS 都指向它 |
| 容器能否访问宿主机代理 | ✅ `host.docker.internal:7890` 可以访问（Docker Desktop 会把它转到宿主机的 loopback） |
| Squid `cache_peer` 用 HTTP 方式串联 | ✅ 日志里是 `FIRSTUP_PARENT/192.168.65.254`，流量确实经过宿主机代理 |
| 白名单放行 | ✅ api.anthropic.com、github.com → `TCP_TUNNEL/200` |
| 白名单拒绝 | ✅ example.com → `TCP_DENIED/403` |
| internal 网络不经代理直连域名或 IP | ✅ 都失败（`000`），没有路由 |

R4（宿主机代理只提供 SOCKS）的风险**解除**：混合端口支持 HTTP 方式串联。

## 意外发现：单文件 bind mount 会读到截断的内容 ⚠️

宿主机用 `sed -i` 修改 `squid.conf` 之后，容器内看到的文件**末尾被截断**（`cache deny all` 变成了 `cache d`），`squid -k reconfigure` 解析出错。原因是单文件 bind mount 绑定的是 inode：`sed -i` 换了一个新文件，而 Docker Desktop 的文件共享只同步了部分内容。

**对实现的约束**（已在 M0-4 里验证有效）：
- 配置一律**挂载目录**，不挂载单个文件。
- 更新配置时先写临时文件，再 `rename` 到目标位置。
- 这条同样适用于 §4.2 里 Claude 的 `settings.json` 等只读挂入的文件：改成挂载一个生成目录，或者在启动时复制进去。

## 日志格式
`logformat sbx %ts.%03tu %un %>a %Ss/%>Hs %rm %ru %Sh/%<a`：带上用户名（shared 模式）和上游节点信息，方便排查。
