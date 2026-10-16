package cli

import (
	"errors"
	"strings"
	"testing"

	"sandx/internal/config"
)

func steps(pairs ...any) []step {
	var out []step
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, step{Name: pairs[i].(string), OK: pairs[i+1].(bool)})
	}
	return out
}

// 这是探针存在的理由：两边停在同一级，结论必须是"不是沙箱的问题"。
// 如果这里报成 fail，用户就会接着去调 sbx——而那正是要避免的事。
func TestVerdictBothStopAtTheSameRungIsNotTheSandbox(t *testing.T) {
	tgt := probeTarget{Host: "gitlab.corp.example", Port: 443}
	host := ladder{Where: "your machine", Steps: steps(stepDNS, true, stepTCP, false)}
	ctr := ladder{Where: "the container", Steps: steps(stepDNS, true, stepTCP, false)}

	lv, detail, fix := verdict(tgt, host, ctr)
	if lv != lvWarn {
		t.Fatalf("level = %v, want warn (sbx cannot fix the peer)", lv)
	}
	if !strings.Contains(detail, "both stop at tcp") {
		t.Fatalf("detail should name the shared stopping point: %s", detail)
	}
	if !strings.Contains(fix, "not the sandbox") {
		t.Fatalf("fix must say it is not the sandbox: %s", fix)
	}
}

// 宿主机过了而容器没过，才是沙箱该背的锅。
// 两边都解析不出来时，提示要指向"名字或 VPN"，而不是"对端在拒绝"——
// 指错方向比不给提示更费时间。
func TestVerdictBothFailAtDNSBlamesTheNameNotThePeer(t *testing.T) {
	tgt := probeTarget{Host: "gitlab.corp.example", Port: 443}
	l := ladder{Steps: []step{{Name: stepDNS, Info: "no such host"}}}
	lv, _, fix := verdict(tgt, l, l)
	if lv != lvWarn {
		t.Fatalf("level = %v, want warn", lv)
	}
	if !strings.Contains(fix, "VPN") {
		t.Fatalf("fix should raise the name/VPN angle: %s", fix)
	}
	if strings.Contains(fix, "refusing") {
		t.Fatalf("a DNS miss is not the peer refusing: %s", fix)
	}
}

func TestVerdictHostReachesButContainerDoesNotIsAFailure(t *testing.T) {
	tgt := probeTarget{Host: "gitlab.corp.example", Port: 443}
	host := ladder{Steps: steps(stepDNS, true, stepTCP, true)}
	ctr := ladder{Steps: []step{{Name: stepDNS, OK: true}, {Name: stepTCP, Info: "unreachable"}}}

	lv, detail, fix := verdict(tgt, host, ctr)
	if lv != lvFail {
		t.Fatalf("level = %v, want fail", lv)
	}
	if !strings.Contains(detail, "your machine can") {
		t.Fatalf("detail should contrast the two positions: %s", detail)
	}
	// 这个场景的修法只有一个地方，提示必须指到那儿，否则用户无从下手。
	if !strings.Contains(fix, "Proxies") {
		t.Fatalf("fix should point at the Docker proxy bypass list: %s", fix)
	}
}

// 容器 DNS 和宿主机 DNS 是两套，分裂在 dns 这一级时修的地方和 tcp 不一样。
func TestVerdictContainerDNSSplitPointsAtTheResolver(t *testing.T) {
	tgt := probeTarget{Host: "gitlab.corp.example", Port: 443}
	host := ladder{Steps: steps(stepDNS, true, stepTCP, true)}
	ctr := ladder{Steps: []step{{Name: stepDNS, Info: "no such host"}}}

	lv, _, fix := verdict(tgt, host, ctr)
	if lv != lvFail {
		t.Fatalf("level = %v, want fail", lv)
	}
	if !strings.Contains(fix, "resolver") {
		t.Fatalf("fix should be about DNS, not routing: %s", fix)
	}
}

// 真实世界里咬过我们的那一个：TCP 通、裸 IP 的 TLS 通、带 SNI 就断。
// forward 本身是好的（纯 TCP），所以不能报 fail；但必须说清是对端在断。
func TestVerdictSNISplitIsReportedAsThePeersPolicy(t *testing.T) {
	tgt := probeTarget{Host: "gitlab.corp.example", Port: 443}
	host := ladder{Steps: steps(stepDNS, true, stepTCP, true)}
	ctr := ladder{
		IP: "10.42.0.50",
		Steps: []step{
			{Name: stepDNS, OK: true, Info: "10.42.0.50"},
			{Name: stepTCP, OK: true},
			{Name: stepSNI, Info: "handshake failure"},
			{Name: stepNoSNI, OK: true, Info: "CN = Corp Private CA"},
		},
	}
	lv, detail, fix := verdict(tgt, host, ctr)
	if lv != lvWarn {
		t.Fatalf("level = %v, want warn: the TCP hole works, TLS is the peer's call", lv)
	}
	if !strings.Contains(detail, "with SNI but succeeds without") {
		t.Fatalf("detail should name the SNI split: %s", detail)
	}
	if !strings.Contains(fix, "prep build") {
		t.Fatalf("fix should route the user to prep, the only way through: %s", fix)
	}
}

func TestVerdictReachableIsOKAndNamesTheIssuer(t *testing.T) {
	tgt := probeTarget{Host: "gitlab.corp.example", Port: 443}
	ctr := ladder{
		IP:     "10.42.0.50",
		Issuer: "CN = Corp Private CA",
		Steps:  steps(stepDNS, true, stepTCP, true, stepSNI, true, stepNoSNI, true),
	}
	lv, detail, _ := verdict(tgt, ladder{Steps: steps(stepDNS, true, stepTCP, true)}, ctr)
	if lv != lvOK {
		t.Fatalf("level = %v, want ok", lv)
	}
	if !strings.Contains(detail, "10.42.0.50") || !strings.Contains(detail, "Corp Private CA") {
		t.Fatalf("detail should carry the address and the issuer: %s", detail)
	}
}

// 探针自己跑不起来，不该把它伪装成"目标不可达"——那是完全相反的结论。
func TestVerdictProbeContainerFailureIsNotAReachabilityVerdict(t *testing.T) {
	tgt := probeTarget{Host: "gitlab.corp.example", Port: 443}
	lv, detail, _ := verdict(tgt, ladder{}, ladder{Err: errors.New("no such image")})
	if lv != lvFail && lv != lvWarn {
		t.Fatalf("level = %v", lv)
	}
	if !strings.Contains(detail, "could not run the probe") {
		t.Fatalf("detail should say the probe itself failed: %s", detail)
	}
}

func TestParseProbeReadsTheLadder(t *testing.T) {
	out := "dns ok 10.42.0.50\ntcp ok \ntls+sni fail handshake failure\ntls-nosni ok CN = Corp Private CA\n"
	got, ip, issuer := parseProbe(out)
	if len(got) != 4 {
		t.Fatalf("steps = %v", got)
	}
	if ip != "10.42.0.50" || issuer != "CN = Corp Private CA" {
		t.Fatalf("ip = %q, issuer = %q", ip, issuer)
	}
	if got[2].Name != stepSNI || got[2].OK || got[2].Info != "handshake failure" {
		t.Fatalf("sni step = %+v", got[2])
	}
}

// 容器里什么都可能打到 stdout（镜像拉取提示之类）。认不出来的行跳过，
// 不能让整项检查死在一行噪音上。
func TestParseProbeIgnoresNoise(t *testing.T) {
	got, _, _ := parseProbe("Unable to find image locally\n\ndns ok 10.42.0.50\nwhatever ok x\n")
	if len(got) != 1 || got[0].Name != stepDNS {
		t.Fatalf("steps = %v", got)
	}
}

func TestLadderStopsAt(t *testing.T) {
	l := ladder{Steps: steps(stepDNS, true, stepTCP, false, stepSNI, false)}
	if l.stopsAt() != stepTCP {
		t.Fatalf("stopsAt = %q", l.stopsAt())
	}
	if (ladder{Steps: steps(stepDNS, true, stepTCP, true)}).stopsAt() != "" {
		t.Fatal("a complete ladder stops nowhere")
	}
	if l.reached() {
		t.Fatal("reached must follow the tcp rung, not the last one")
	}
}

// 裸端口号的 forward 指向宿主机，那条路由是 Docker 自己维护的，
// 探它只会得到一个和沙箱无关的结论，所以不进探测名单。
func TestForwardTargetsSkipHostForwards(t *testing.T) {
	got := forwardTargets([]config.Forward{
		{Name: "db", To: config.Target{S: "5432"}},
		{Name: "gitlab", To: config.Target{S: "10.42.0.50:443"}},
	})
	if len(got) != 1 || got[0].Name != "gitlab" || got[0].Port != 443 {
		t.Fatalf("targets = %+v", got)
	}
}

// 配了 host 的那几条在容器里用真实域名访问（--add-host 保住 SNI），
// 探针要跟着用那个名字，否则根本测不到对端的 SNI 策略。
func TestForwardTargetsPreferTheDeclaredHostname(t *testing.T) {
	got := forwardTargets([]config.Forward{
		{Name: "gitlab", To: config.Target{S: "10.42.0.50:443"}, Host: "gitlab.corp.example"},
	})
	if len(got) != 1 || got[0].Host != "gitlab.corp.example" {
		t.Fatalf("targets = %+v", got)
	}
}

func TestWantsTLSOnlyOnTLSPorts(t *testing.T) {
	if !(probeTarget{Port: 443}).wantsTLS() || !(probeTarget{Port: 8443}).wantsTLS() {
		t.Fatal("443/8443 should get the TLS rungs")
	}
	if (probeTarget{Port: 5432}).wantsTLS() {
		t.Fatal("a postgres port should not get TLS rungs: two lines of noise")
	}
}

// 配置里的值是外部输入。脚本必须是常量，主机名只能经环境变量进去，
// 否则就是一个注入点。
func TestProbeScriptTakesItsInputFromTheEnvironment(t *testing.T) {
	for _, v := range []string{"$PH", "$PP", "$PTLS"} {
		if !strings.Contains(probeScript, v) {
			t.Fatalf("script should read %s from the environment", v)
		}
	}
	// 探测失败是结果不是错误：脚本的每条路径都走到 exit 0 或自然结束。
	if strings.Contains(probeScript, "exit 1") {
		t.Fatalf("the script must never exit non-zero: %s", probeScript)
	}
}

func TestNetReasonCompressesGoErrors(t *testing.T) {
	cases := map[string]string{
		"dial tcp 10.42.0.50:443: connect: connection refused": "connection refused",
		"dial tcp 10.42.0.50:443: i/o timeout":                 "timed out",
		"remote error: tls: handshake failure":                 "tls: handshake failure",
	}
	for in, want := range cases {
		if got := netReason(errors.New(in)); got != want {
			t.Fatalf("netReason(%q) = %q, want %q", in, got, want)
		}
	}
	if netReason(nil) != "" {
		t.Fatal("nil error has no reason")
	}
}
