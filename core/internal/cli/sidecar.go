package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/execution"
	"sandx/internal/proxy"
	"sandx/internal/task"
)

// sidecarOf 是伴生容器的名字。kind 取 svc / fwd，和 label 里的 sbx.kind 一致。
func sidecarOf(t task.Task, kind, name string) string {
	return t.Container() + "-" + kind + "-" + name
}

// envArgs 把 map 排序后转成 KEY=VALUE，保证容器参数稳定（golden 测试、docker inspect 对比）。
func envArgs(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// ensureServices 起 [[services]] 声明的伴生容器（M4-11、boundary.md §3.2）。
//
// 它们接在 t.Network() 上——那是 internal 网络，**容器之间通信根本不需要出网路由**，
// 这正是这一类最安全的原因：服务起得来，不变量一点没动。
//
// 容器内用服务名当主机名访问（network alias），不是 localhost：localhost 指的是
// Task 容器自己，别的容器不可能出现在那上面。boundary.md §3.2 的 "localhost:5432"
// 是示意，实际地址是 db:5432——sbx run 会逐条打出来。
func (a *App) ensureServices(t task.Task, u *execution.Undo) error {
	for _, s := range a.Cfg.Services {
		name := sidecarOf(t, "svc", s.Name)
		st, exists, err := a.Docker.Inspect(name)
		if err != nil {
			return err
		}
		if exists {
			if !st.Running {
				u.Add("container_state", name, true, func(ctx context.Context) error { return a.Docker.WithContext(ctx).Stop(name) })
				if err := a.Docker.Start(name); err != nil {
					return err
				}
			}
			continue
		}
		labels := t.Labels()
		labels["sbx.kind"] = "svc"
		labels["sbx.svc"] = s.Name
		spec := docker.RunSpec{
			Name:    name,
			Image:   s.Image,
			Labels:  labels,
			Network: t.Network(),
			Aliases: []string{s.Name},
			Env:     envArgs(s.Env),
			// 服务容器不设内存上限：这里没有配置字段，而给 postgres 猜一个小数字
			// 的后果是启动即 OOM，比不限更糟。它和 Task 共享同一台 Docker VM 的预算。
			LogMaxSize: "10m",
		}
		if err := execution.CreateOwnedContainer(a.Docker, spec, u); err != nil {
			return fmt.Errorf("service %s (%s): %w", s.Name, s.Image, err)
		}
		fmt.Fprintf(a.Out, "service %s: %s, reachable in the task as %s:%d\n", s.Name, s.Image, s.Name, s.Port)
	}
	return nil
}

// socatRef 是转发容器用的镜像，按 digest 固定（M4-13）。
// 这个 sidecar 坐在 Task 和内网之间，是整条链上最该确定的一环：:latest 意味着
// 某天 docker pull 换来的是另一份二进制，而没有任何地方会记下这件事。
// 这是个 multi-arch index 的 digest，amd64 / arm64 都能拉。
const socatRef = "alpine/socat@sha256:82ad20f6f6e29b91ff33b6662d24063522b1378f08fd8569cd3ab412cac13f50"

// hostAlias 是裸端口号那种写法的目标：宿主机。
const hostAlias = "host.docker.internal"

// resolved 是一条 forward 解析之后的样子。
type resolved struct {
	Name   string
	Port   int
	Target string // socat 的目标，host:port
	Addr   string // 解析出来的实际地址（宿主机那条是 hostAlias）
	Host   string
	ToHost bool // 目标是宿主机（🟡）而不是内网（🔴）
	Note   string
}

// Level 是给人看的风险级别，和 boundary.md §3.1 的三级对应。
func (r resolved) Level() string {
	if r.ToHost {
		return "your machine"
	}
	return "internal network"
}

// resolveForward 把一条配置解析成确定的目标地址。
//
// **解析只在这里发生一次，IP 固化进 socat 的目标**：行为确定、能把实际地址打出来、
// 也避免 DNS 在 Task 跑着的时候把目标换掉（boundary.md §3.3.1）。
func resolveForward(f config.Forward) (resolved, error) {
	r := resolved{Name: f.Name, Port: f.Port, Host: f.Host}
	host, port, err := f.To.Split()
	if err != nil {
		return r, fmt.Errorf("forward %s: %v", f.Name, err)
	}
	if host == "" {
		// 裸端口号 = 宿主机。这条不做 DNS 解析：host.docker.internal 是 Docker 自己
		// 维护的静态条目（Linux 上由 --add-host host-gateway 给出），没有被重绑定的余地。
		r.Target = net.JoinHostPort(hostAlias, strconv.Itoa(port))
		r.Addr, r.ToHost = hostAlias, true
		return r, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return r, fmt.Errorf("forward %s: cannot resolve %q: %v", f.Name, host, err)
	}
	ip := pickIP(ips)
	switch {
	case ip.IsLoopback():
		// 127.0.0.1 在 forwarder 里指的是 forwarder 自己，不是你的机器——照字面转发
		// 会得到一个连不上的洞。改指宿主机，并说清楚换了什么。
		r.Target = net.JoinHostPort(hostAlias, strconv.Itoa(port))
		r.Addr, r.ToHost = hostAlias, true
		r.Note = fmt.Sprintf("%s resolves to %s, which inside the forwarder means the forwarder itself; pointing it at your machine (%s) instead. The short form is to = %d.", host, ip, hostAlias, port)
	case ip.IsPrivate() || ip.IsLinkLocalUnicast():
		r.Target = net.JoinHostPort(ip.String(), strconv.Itoa(port))
		r.Addr = ip.String()
	case f.AllowPublic:
		r.Target = net.JoinHostPort(ip.String(), strconv.Itoa(port))
		r.Addr = ip.String()
		r.Note = "allow_public is set: this target is a public address, so this forward is a TCP tunnel that squid never sees (no allowlist, no access log, invisible to sbx net denied)"
	default:
		// to 能写任意地址的话，forwarder 就成了绕过 squid 的通用出网隧道
		// （boundary.md §3.3.1）。这是 forwarder 唯一的硬限制。
		return r, fmt.Errorf("forward %s: %s resolves to %s, which is a public address. Public hosts go through network.allow + squid, where they are filtered and logged.\n"+
			"If your company really uses public address ranges internally, set allow_public = true on this one forward", f.Name, host, ip)
	}
	return r, nil
}

// pickIP 优先选 IPv4：转发目标多半是内网服务，IPv6 的可达性反而更不确定。
func pickIP(ips []net.IP) net.IP {
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip
		}
	}
	return ips[0]
}

// ensureForwards 为每条 forward 起一个独立的 socat 容器（M4-13）。
//
// 一条一个容器：多条规则不会在监听端口上打架，回收也能逐条来。每个容器同时接
// t.Network()（internal，Task 从这边连它）和 sbx-egress（它从这边出去），
// **Task 容器自己仍然只有 internal 那一张网卡，没有默认路由**——forwarder 是
// Task 网络里的一个普通 peer，不是 Task 的网关（boundary.md §3.3.4）。
//
// 返回要加给 Task 容器的 --add-host（配了 host 字段的那几条）和记进 meta 的信息。
// create 为 true 时是新建 Task：每条都重建，目标重新解析一次。resume 时相反——
// 已经在跑的 forwarder 原样留着，因为 Task 容器的 /etc/hosts 里记的是它当时的 IP，
// 重建会把那条记录变成陈的（--add-host 只能在建容器时给）。
func (a *App) ensureForwards(t task.Task, u *execution.Undo, create bool) ([]string, []task.Link, error) {
	var addHosts []string
	var links []task.Link
	for _, f := range a.Cfg.Forward {
		name := sidecarOf(t, "fwd", f.Name)
		if !create {
			st, exists, err := a.Docker.Inspect(name)
			if err != nil {
				return nil, nil, err
			}
			if exists {
				if !st.Running {
					u.Add("container_state", name, true, func(ctx context.Context) error { return a.Docker.WithContext(ctx).Stop(name) })
					if err := a.Docker.Start(name); err != nil {
						return nil, nil, err
					}
				}
				a.checkAddHost(t, f, name)
				continue
			}
			if f.Host != "" {
				a.logf("warning: the forwarder for %s was gone and is being rebuilt; %s inside task %s still points at its old address. Recreate the task (sbx done %s, then sbx run %s) if that name stops resolving.",
					f.Name, f.Host, t.Name, t.Name, t.Name)
			}
		}
		r, err := resolveForward(f)
		if err != nil {
			return nil, nil, err
		}
		if _, exists, err := a.Docker.Inspect(name); err != nil {
			return nil, nil, err
		} else if exists {
			return nil, nil, fmt.Errorf("forward %s already exists; preserve it until sbx done removes the previous task", f.Name)
		}
		labels := t.Labels()
		labels["sbx.kind"] = "fwd"
		labels["sbx.fwd"] = f.Name
		spec := docker.RunSpec{
			Name:    name,
			Image:   socatRef,
			Labels:  labels,
			Network: t.Network(),
			Aliases: []string{f.Name},
			Cmd: []string{
				fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", f.Port),
				"TCP:" + r.Target,
			},
			Resources:  docker.Resources{Memory: "32m"},
			LogMaxSize: "10m",
		}
		if runtime.GOOS == "linux" {
			// 和 proxy/shared.go 同一写法：Linux 上 host.docker.internal 要自己给
			spec.AddHosts = []string{hostAlias + ":host-gateway"}
		}
		if err := execution.CreateOwnedContainer(a.Docker, spec, u); err != nil {
			return nil, nil, fmt.Errorf("forward %s: %w", f.Name, err)
		}
		// 出网那张网卡接在这里。Task 容器永远不接这张网。
		if err := a.Docker.NetworkConnect(proxy.EgressNet, name, ""); err != nil {
			return nil, nil, fmt.Errorf("forward %s: %w", f.Name, err)
		}
		link := task.Link{Name: f.Name, Port: f.Port, To: r.Addr + ":" + portOf(r.Target), Host: f.Host, Internal: !r.ToHost}
		links = append(links, link)
		a.describeForward(r)
		if f.Host != "" {
			ip, err := a.Docker.ContainerIP(name, t.Network())
			if err != nil {
				return nil, nil, fmt.Errorf("forward %s: %w", f.Name, err)
			}
			addHosts = append(addHosts, f.Host+":"+ip)
			fmt.Fprintf(a.Out, "           %s also resolves to it inside the task, so TLS keeps working\n", f.Host)
		}
	}
	if len(links) > 0 {
		a.warnForwardExposure(links)
	}
	return addHosts, links, nil
}

func portOf(hostport string) string {
	_, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return ""
	}
	return p
}

// checkAddHost 核对 Task 容器里那条 --add-host 还指不指着这个 forwarder。
//
// --add-host 只能在建容器时给，而容器重启之后 Docker 不保证把同一个 IP 发回来。
// 不核对的话，表现是"域名还在、就是连不上"，没有任何地方会说一句。
func (a *App) checkAddHost(t task.Task, f config.Forward, name string) {
	if f.Host == "" {
		return
	}
	ip, err := a.Docker.ContainerIP(name, t.Network())
	if err != nil {
		return
	}
	hosts, err := a.Docker.ExtraHosts(t.Container())
	if err != nil {
		return
	}
	for _, h := range hosts {
		if h == f.Host+":"+ip {
			return
		}
	}
	a.logf("warning: %s inside task %s no longer points at the forwarder (it is now %s). Recreate the task to fix it: sbx done %s, then sbx run %s",
		f.Host, t.Name, ip, t.Name, t.Name)
}

// stopSidecars 跟着 sbx stop 一起把伴生容器停掉——Task 停了还留着一个 redis 跑在那儿，
// 占的是同一台 Docker VM 的内存，而没有任何地方会提醒你。
func (a *App) stopSidecars(t task.Task) {
	for _, kind := range []string{"svc", "fwd"} {
		items, err := a.Docker.ListByLabel("container", "sbx.ws="+t.WS.ID, "sbx.task="+t.Name, "sbx.kind="+kind)
		if err != nil {
			continue
		}
		for _, it := range items {
			if strings.Contains(it.Str("State"), "running") {
				a.Docker.Stop(it.Str("Names"))
			}
		}
	}
}

// describeForward 逐条打印生效的 forward 和解析出的实际地址（M4-14、boundary.md §3.4）。
// 预期管理不能靠自觉：开了什么洞、通到哪儿，必须在每次 sbx run 的输出里看得见。
func (a *App) describeForward(r resolved) {
	fmt.Fprintf(a.Out, "forward %s: %s:%d in the task -> %s (%s)\n", r.Name, r.Name, r.Port, r.Target, r.Level())
	if r.Note != "" {
		fmt.Fprintf(a.Out, "           note: %s\n", r.Note)
	}
}

// warnForwardExposure 在「接了内网资源 + 出口不设防」这个组合下提醒一句
// （boundary.md §3.5、R-B8）。open 之所以可控，前提是沙箱里没什么值得偷的；
// 接上真实数据之后这个前提就不成立了。
func (a *App) warnForwardExposure(links []task.Link) {
	var internal []string
	for _, l := range links {
		if l.Internal {
			internal = append(internal, l.Name)
		}
	}
	if len(internal) == 0 {
		return
	}
	fmt.Fprintf(a.Err, "note: these forwards reach real internal services: %s\n", strings.Join(internal, ", "))
	fmt.Fprintln(a.Err, "      whatever the agent does to them, it does for real:")
	fmt.Fprintln(a.Err, "      use a read-only account or a separate schema; the network layer cannot undo a DROP TABLE.")
	if a.Cfg.Network.Mode == "open" {
		fmt.Fprintln(a.Err, "      network.mode is open, so this task has both real data and unrestricted egress.")
		fmt.Fprintln(a.Err, "      consider network.mode = \"allowlist\" for this workspace (or sbx run --net allowlist).")
	}
}

// removeSidecars 回收这个 Task 的 services 和 forwarder 容器。
// 必须排在删网络之前：它们接在 Task 网络上，不先删就删不动网络（和 M3-11 的端口转发同理）。
func (a *App) removeSidecars(t task.Task) error {
	var failures []error
	for _, kind := range []string{"svc", "fwd"} {
		items, err := a.Docker.ListByLabel("container", "sbx.ws="+t.WS.ID, "sbx.task="+t.Name, "sbx.kind="+kind)
		if err != nil {
			failures = append(failures, fmt.Errorf("list %s sidecars: %w", kind, err))
			continue
		}
		for _, it := range items {
			if err := a.Docker.Rm(it.Str("Names")); err != nil && !docker.IsNotFound(err) {
				failures = append(failures, fmt.Errorf("remove sidecar %s: %w", it.Str("Names"), err))
			}
		}
	}
	return errors.Join(failures...)
}

// countSidecars 数这个 Task 挂着的伴生容器，给 sbx ls 的 LINKS 列用。
func (a *App) countSidecars(t task.Task) (svc, fwd int) {
	for _, kind := range []string{"svc", "fwd"} {
		items, err := a.Docker.ListByLabel("container", "sbx.ws="+t.WS.ID, "sbx.task="+t.Name, "sbx.kind="+kind)
		if err != nil {
			continue
		}
		if kind == "svc" {
			svc = len(items)
		} else {
			fwd = len(items)
		}
	}
	return svc, fwd
}
