package cli

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"sandx/internal/config"
	"sandx/internal/proxy"
)

// 内网探针（boundary.md §3.4）。
//
// 连不上内网的目标有四种完全不同的原因，而它们的症状都是"连不上"：DNS、路由、
// TLS、对端策略。沙箱只能修前两种，后两种得去找服务方——但用户没有工具分辨，
// 于是默认怀疑沙箱，时间全花在调 sbx 上。
//
// 办法是**同一组探测在两个位置各跑一遍**：容器里一遍，宿主机上一遍，然后看它们
// 在哪一级分裂。分裂点就是结论：
//
//	宿主机过了、容器没过  → 沙箱侧的问题（容器 DNS、Docker VM 的透明代理）
//	两边停在同一级        → 不是沙箱的问题，对端对谁都这样
//
// 第二种结论才是这个探针最大的价值：它是唯一能阻止用户继续调沙箱的信息。

// probeImage 复用 forwarder 已经 pin 住的那个镜像：它带 busybox（getent / nc）
// 和编译进 OpenSSL 的 socat，四级阶梯一个不缺，不用为了诊断再引一个依赖。
const probeImage = socatRef

// probeTimeout 是单级的上限。探针跑在 doctor 的 Slow 组里，但也不能让一个
// 黑洞路由把整条命令拖住：四级 × 两个位置 × 每个目标。
const probeTimeout = 5 * time.Second

// 阶梯的四级。顺序就是依赖顺序，前一级不过后面的不用试。
const (
	stepDNS    = "dns"
	stepTCP    = "tcp"
	stepSNI    = "tls+sni"
	stepNoSNI  = "tls-nosni"
	stepStatus = "ok"
)

// step 是阶梯上的一级结果。
type step struct {
	Name string
	OK   bool
	Info string // 过了是细节（IP、证书签发者），没过是原因
}

// ladder 是一个位置上的整组探测结果。
type ladder struct {
	Where  string // "your machine" / "the container"
	Steps  []step
	IP     string
	Issuer string // 对端证书的签发者，拿到了才填
	Err    error  // 整组没跑起来（docker 起不来之类），和"某一级没过"不是一回事
}

// stopsAt 返回第一个没过的级别；全过返回空串。
func (l ladder) stopsAt() string {
	for _, s := range l.Steps {
		if !s.OK {
			return s.Name
		}
	}
	return ""
}

func (l ladder) find(name string) (step, bool) {
	for _, s := range l.Steps {
		if s.Name == name {
			return s, true
		}
	}
	return step{}, false
}

// reached 表示 TCP 这一级过了——对 forward 来说这就是"这个洞能用"。
// TLS 两级是诊断信息：forward 是纯 TCP 中继，握不握得上手不归它管。
func (l ladder) reached() bool {
	s, ok := l.find(stepTCP)
	return ok && s.OK
}

// probeTarget 是一个要探的目标。
type probeTarget struct {
	Name string // forward 的 name，用来在输出里指回配置
	Host string
	Port int
}

func (t probeTarget) addr() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// wantsTLS 决定要不要跑 TLS 两级。非 TLS 端口上跑只会得到两行噪音，
// 而真正有用的场景（私有 GitLab、私有 registry）都在这几个端口上。
func (t probeTarget) wantsTLS() bool {
	switch t.Port {
	case 443, 8443, 9443:
		return true
	}
	return false
}

// forwardTargets 从合并后的配置里取探测目标（boundary.md §3.3）。
//
// 只取指向内网的那些：`to` 是裸端口号时目标是宿主机，那条路由由 Docker 自己
// 维护（host.docker.internal / host-gateway），不存在本探针能查出来的分裂。
func forwardTargets(fwds []config.Forward) []probeTarget {
	var out []probeTarget
	for _, f := range fwds {
		host, port, err := f.To.Split()
		if err != nil || host == "" {
			continue
		}
		// 配了 host 的那几条在容器里用的是真实域名（--add-host 保住 SNI），
		// 探针也该按那个名字探，否则测不到对端的 SNI 策略。
		if f.Host != "" {
			host = f.Host
		}
		out = append(out, probeTarget{Name: f.Name, Host: host, Port: port})
	}
	return out
}

// probeHost 在宿主机上跑一遍阶梯，用 Go 自己的 net 和 crypto/tls。
//
// 刻意不调 curl / openssl：macOS 自带的 curl 是 LibreSSL，它和系统代理的交互
// 会给出和实际不符的结论——这个探针存在的部分原因就是当初被它误导过。
func probeHost(t probeTarget) ladder {
	l := ladder{Where: "your machine"}

	ips, err := net.LookupIP(t.Host)
	if err != nil || len(ips) == 0 {
		l.Steps = append(l.Steps, step{Name: stepDNS, Info: dnsReason(err)})
		return l
	}
	ip := pickIP(ips)
	l.IP = ip.String()
	l.Steps = append(l.Steps, step{Name: stepDNS, OK: true, Info: l.IP})

	conn, err := net.DialTimeout("tcp", t.addr(), probeTimeout)
	if err != nil {
		l.Steps = append(l.Steps, step{Name: stepTCP, Info: netReason(err)})
		return l
	}
	conn.Close()
	l.Steps = append(l.Steps, step{Name: stepTCP, OK: true})

	if !t.wantsTLS() {
		return l
	}
	// 带 SNI：把 ServerName 填成主机名，和浏览器、git、go 的行为一致。
	sni := tlsProbe(t.addr(), t.Host)
	l.Steps = append(l.Steps, sni)
	// 不带 SNI：连裸 IP。InsecureSkipVerify + 空 ServerName 时 Go 不发 SNI 扩展，
	// 这正是要对照的那一组——对端如果只在看到 SNI 时才断，这一级会过。
	noSNI := tlsProbe(net.JoinHostPort(l.IP, strconv.Itoa(t.Port)), "")
	l.Steps = append(l.Steps, noSNI)
	l.Issuer = firstIssuer(sni, noSNI)
	return l
}

// tlsProbe 握一次手，成功时把对端证书的签发者带回来。
// 证书校验一律关掉：这里要回答的是"连到的是谁"，不是"证书是否可信"——
// 私有 CA 签的证书在宿主机的信任库里本来就可能不在。
func tlsProbe(addr, serverName string) step {
	name := stepSNI
	if serverName == "" {
		name = stepNoSNI
	}
	conn, err := net.DialTimeout("tcp", addr, probeTimeout)
	if err != nil {
		return step{Name: name, Info: netReason(err)}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(probeTimeout))

	c := tls.Client(conn, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}) //nolint:gosec // 见上
	if err := c.Handshake(); err != nil {
		return step{Name: name, Info: netReason(err)}
	}
	s := step{Name: name, OK: true}
	if certs := c.ConnectionState().PeerCertificates; len(certs) > 0 {
		s.Info = certs[0].Issuer.String()
	}
	return s
}

func firstIssuer(steps ...step) string {
	for _, s := range steps {
		if s.OK && s.Info != "" {
			return s.Info
		}
	}
	return ""
}

func dnsReason(err error) string {
	if err == nil {
		return "no addresses"
	}
	var d *net.DNSError
	if ok := asDNSError(err, &d); ok && d.IsNotFound {
		return "no such host"
	}
	return netReason(err)
}

func asDNSError(err error, target **net.DNSError) bool {
	for e := err; e != nil; {
		if d, ok := e.(*net.DNSError); ok {
			*target = d
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// netReason 把 Go 的错误压成一行能读的原因。原始错误里的地址和包路径对
// 诊断没有帮助，而且会把 doctor 的输出撑爆。
func netReason(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "connection refused"):
		return "connection refused"
	case strings.Contains(s, "i/o timeout"), strings.Contains(s, "deadline exceeded"):
		return "timed out"
	case strings.Contains(s, "no route to host"):
		return "no route to host"
	case strings.Contains(s, "EOF"):
		return "peer closed the connection"
	}
	// "remote error:" 是 Go 对"对端发来的 alert"的前缀，剥掉它但保住后面的
	// "tls: ..."——那一段恰好是在说对端为什么不干，不能一起扔掉。
	s = strings.TrimPrefix(s, "remote error: ")
	// 传输层错误带着 "dial tcp <addr>: " 这类前缀，地址在别处已经打过了。
	for _, p := range []string{"dial tcp ", "read tcp ", "write tcp "} {
		if strings.HasPrefix(s, p) {
			if i := strings.LastIndex(s, ": "); i >= 0 {
				s = s[i+2:]
			}
			break
		}
	}
	return s
}

// probeScript 是容器里跑的那段 sh。
//
// 它是一个**常量**：主机名和端口经环境变量传进去，不插进脚本正文。
// 配置里的值是外部输入，拼进 shell 就是一个注入点。
//
// 每一级输出一行 `<级别> ok|fail <细节>`，而且整段永远 exit 0——
// 探测失败是结果不是错误，不该让 docker run 带着非零码回来。
const probeScript = `
emit() { printf '%s %s %s\n' "$1" "$2" "$3"; }

ip=$(getent ahostsv4 "$PH" 2>/dev/null | head -1 | cut -d' ' -f1)
if [ -z "$ip" ]; then emit dns fail "no such host"; exit 0; fi
emit dns ok "$ip"

if nc -z -w3 "$PH" "$PP" 2>/dev/null; then emit tcp ok ""; else emit tcp fail "unreachable"; exit 0; fi

[ "$PTLS" = "1" ] || exit 0

tls() { # $1 = 级别名, $2 = socat 目标地址
  out=$(socat -d -d -d -T5 /dev/null "OPENSSL:$2:$PP,verify=0" 2>&1)
  if [ $? -eq 0 ]; then
    issuer=$(echo "$out" | sed -n 's/.*SSL peer cert issuer: "\(.*\)"$/\1/p' | head -1)
    emit "$1" ok "$issuer"
  else
    why=$(echo "$out" | sed -n 's/.*SSL_connect(): error[^ ]* \(.*\)$/\1/p' | head -1)
    [ -n "$why" ] || why=$(echo "$out" | tail -1 | sed 's/^.* [EWN] //')
    emit "$1" fail "$why"
  fi
}

tls tls+sni "$PH"
tls tls-nosni "$ip"
`

// probeContainer 在容器里跑同一组探测。
//
// 跑在 sbx-egress 上（没有就退回默认 bridge）：那正是 forwarder 和 squid 待的
// 位置，也就是"沙箱实际从哪里出去"。**不是** Task 自己的 internal 网络——那张网
// 按设计没有默认路由，在那儿探什么都不通，量到的是设计而不是故障。
func (a *App) probeContainer(t probeTarget) ladder {
	l := ladder{Where: "the container"}
	args := []string{"run", "--rm",
		"-e", "PH=" + t.Host,
		"-e", "PP=" + strconv.Itoa(t.Port),
		"-e", "PTLS=" + boolArg(t.wantsTLS()),
	}
	if ok, err := a.Docker.NetworkExists(proxy.EgressNet); err == nil && ok {
		args = append(args, "--network", proxy.EgressNet)
	}
	args = append(args, "--entrypoint", "sh", probeImage, "-c", probeScript)

	out, err := a.Docker.Run(args...)
	if err != nil {
		l.Err = err
		return l
	}
	l.Steps, l.IP, l.Issuer = parseProbe(out)
	return l
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// parseProbe 读 probeScript 的输出。认不出来的行跳过：这是诊断，
// 多一行噪音也比整项检查失败好。
func parseProbe(out string) (steps []step, ip, issuer string) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(line), " ", 3)
		if len(f) < 2 {
			continue
		}
		s := step{Name: f[0], OK: f[1] == stepStatus}
		if len(f) == 3 {
			s.Info = strings.TrimSpace(f[2])
		}
		switch s.Name {
		case stepDNS, stepTCP, stepSNI, stepNoSNI:
		default:
			continue
		}
		if s.Name == stepDNS && s.OK {
			ip = s.Info
		}
		if s.OK && (s.Name == stepSNI || s.Name == stepNoSNI) && issuer == "" {
			issuer = s.Info
		}
		steps = append(steps, s)
	}
	return steps, ip, issuer
}

// verdict 是这个探针真正的产出：看两条阶梯在哪一级分裂，给出一句结论。
//
// 返回的 level 只对"沙箱该不该为此负责"表态：
//
//	lvOK   容器到得了，forward 能用
//	lvFail 宿主机到得了而容器到不了 —— 沙箱侧的问题，有得修
//	lvWarn 两边都到不了 —— 对端或网络的问题，sbx 修不了，但你得知道
func verdict(t probeTarget, host, ctr ladder) (level, string, string) {
	if ctr.Err != nil {
		return lvWarn, fmt.Sprintf("%s: could not run the probe container (%v)", t.addr(), ctr.Err), ""
	}
	hostStop, ctrStop := host.stopsAt(), ctr.stopsAt()

	// 两边都通到了 TCP：洞是好的。剩下的只是 TLS 层面的观察。
	if ctr.reached() {
		detail := fmt.Sprintf("%s reachable", t.addr())
		if ctr.IP != "" {
			detail += " at " + ctr.IP
		}
		// 带 SNI 断、裸 IP 通 —— 这是对端在看到 SNI 之后主动断的典型形状。
		sni, hasSNI := ctr.find(stepSNI)
		nosni, hasNo := ctr.find(stepNoSNI)
		if hasSNI && hasNo && !sni.OK && nosni.OK {
			return lvWarn, detail + fmt.Sprintf("; TLS fails with SNI but succeeds without (%s)", sni.Info),
				"the peer drops the handshake once it sees the server name — that is the peer's policy, not something sbx can change. For go module fetches, seed the dependency with sbx prep build instead of reaching the source at run time."
		}
		if ctr.Issuer != "" {
			detail += ", TLS peer issued by " + ctr.Issuer
		}
		return lvOK, detail, ""
	}

	// 容器没到，宿主机到了：分裂点就是沙箱侧的毛病。
	if host.reached() {
		switch ctrStop {
		case stepDNS:
			return lvFail, fmt.Sprintf("%s: the container cannot resolve this name, your machine can", t.addr()),
				"the container uses the Docker VM's resolver, not your machine's. Check Docker Desktop → Settings → Resources → Network (or your VPN client's split-DNS settings)."
		default:
			return lvFail, fmt.Sprintf("%s: the container cannot connect, your machine can (%s)", t.addr(), reasonOf(ctr, stepTCP)),
				"your machine reaches this through a route the Docker VM does not have — usually a proxy exception list. Add the host or its subnet to Docker Desktop → Settings → Resources → Proxies → Bypass, then retry."
		}
	}

	// 两边都没到，而且卡在同一级：和沙箱无关，两个位置看到的是同一个世界。
	// 给的建议要分级别——卡在 DNS 多半是名字写错或没连 VPN，和"对端在拒绝"
	// 是完全不同的两件事，提示指错方向比不给提示更费时间。
	if hostStop == ctrStop {
		detail := fmt.Sprintf("%s: unreachable from the container and from your machine alike, both stop at %s (%s)", t.addr(), ctrStop, reasonOf(ctr, ctrStop))
		if ctrStop == stepDNS {
			return lvWarn, detail,
				"neither position can resolve this name, so the sandbox is not involved. Check the hostname in your [[forward]] entry, and whether this name needs a VPN that is currently down."
		}
		return lvWarn, detail,
			"both positions behave the same, so this is not the sandbox — the target, or the network in front of it, is refusing. Take it up with whoever runs that service; sbx has no way around it (boundary.md §3.4)."
	}
	return lvWarn, fmt.Sprintf("%s: unreachable from both, your machine stops at %s and the container at %s", t.addr(), hostStop, ctrStop),
		"neither position gets through, so the sandbox is not what is blocking you."
}

func reasonOf(l ladder, name string) string {
	if s, ok := l.find(name); ok && s.Info != "" {
		return s.Info
	}
	return "no detail"
}

// checkReach 是 doctor 里的那一项。目标取自配置里的 [[forward]]——
// 没配就没有可探的东西，这时候跳过而不是去猜一个地址。
func checkReach(a *App, _ docEnv) Check {
	targets := forwardTargets(a.Cfg.Forward)
	if len(targets) == 0 {
		return skip("reach", "no [[forward]] targets configured, nothing to probe")
	}
	worst, lines, fix := lvOK, []string{}, ""
	for _, t := range targets {
		lv, detail, f := verdict(t, probeHost(t), a.probeContainer(t))
		if t.Name != "" {
			detail = t.Name + ": " + detail
		}
		lines = append(lines, detail)
		if lv > worst {
			worst, fix = lv, f
		}
	}
	return Check{Name: "reach", Level: worst, Detail: strings.Join(lines, "\n"), Fix: fix}
}
