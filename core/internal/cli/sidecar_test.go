package cli

import (
	"strings"
	"testing"

	"sandx/internal/config"
)

func TestResolveForwardHostShorthand(t *testing.T) {
	r, err := resolveForward(config.Forward{Name: "db", Port: 5432, To: config.Target{S: "5432"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Target != hostAlias+":5432" || !r.ToHost {
		t.Fatalf("bare port should mean the host machine, got %+v", r)
	}
}

func TestResolveForwardPrivateAddress(t *testing.T) {
	r, err := resolveForward(config.Forward{Name: "pg", Port: 5432, To: config.Target{S: "10.10.16.80:5432"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Target != "10.10.16.80:5432" || r.ToHost {
		t.Fatalf("a private address is an internal target, got %+v", r)
	}
}

// 公网目标必须被拒：允许的话 forwarder 就成了绕过 squid 的通用出网隧道
// （boundary.md §3.3.1），白名单、策略拦截、访问日志一个都不剩。
func TestResolveForwardRejectsPublic(t *testing.T) {
	_, err := resolveForward(config.Forward{Name: "api", Port: 443, To: config.Target{S: "1.1.1.1:443"}})
	if err == nil {
		t.Fatal("a public target must be rejected")
	}
	if !strings.Contains(err.Error(), "network.allow") || !strings.Contains(err.Error(), "allow_public") {
		t.Fatalf("the error must point at both the right way and the escape hatch: %v", err)
	}
	r, err := resolveForward(config.Forward{Name: "api", Port: 443, To: config.Target{S: "1.1.1.1:443"}, AllowPublic: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Note == "" {
		t.Fatal("allow_public must come with a warning")
	}
}

// 127.0.0.1 在 forwarder 里指的是 forwarder 自己。照字面转发会得到一个
// 连不上的洞，所以要改指宿主机并说清楚。
func TestResolveForwardRewritesLoopback(t *testing.T) {
	r, err := resolveForward(config.Forward{Name: "db", Port: 5432, To: config.Target{S: "127.0.0.1:5432"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Target != hostAlias+":5432" || !r.ToHost || r.Note == "" {
		t.Fatalf("loopback should be rewritten to the host, with a note: %+v", r)
	}
}

func TestLinksCell(t *testing.T) {
	for _, c := range []struct {
		svc, fwd int
		want     string
	}{
		{0, 0, "-"}, {2, 0, "svc:2"}, {0, 1, "fwd:1"}, {1, 2, "svc:1 fwd:2"},
	} {
		if got := linksCell(c.svc, c.fwd); got != c.want {
			t.Errorf("linksCell(%d,%d) = %q, want %q", c.svc, c.fwd, got, c.want)
		}
	}
}

func TestEnvArgsIsSorted(t *testing.T) {
	got := envArgs(map[string]string{"B": "2", "A": "1"})
	if len(got) != 2 || got[0] != "A=1" || got[1] != "B=2" {
		t.Fatalf("env must be stable and sorted: %v", got)
	}
}
