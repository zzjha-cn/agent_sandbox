package proxy

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		os.WriteFile(p, got, 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("mismatch %s\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestRenderAllow(t *testing.T) {
	got := RenderAllow([]string{"mcp-proxy.anthropic.com"},
		[]string{".github.com", "github.com", "api.github.com", "x.io", "mcp-proxy.anthropic.com"},
		[]string{"X.io", ".anthropic.com", "a.b.example.com", ".example.com", ".b.example.com"},
	)
	want := []string{".anthropic.com", ".example.com", ".github.com", "x.io"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestBuiltinLists(t *testing.T) {
	b := BuiltinList("builtin")
	if !contains(b, ".claude.com") || !contains(b, ".anthropic.com") {
		t.Fatal(b)
	}
	if got := BuiltinList("policy-block"); !reflect.DeepEqual(got, []string{"mcp-proxy.anthropic.com"}) {
		t.Fatal(got)
	}
	all := RenderAllow(BuiltinList("policy-block"), b, BuiltinList("web-go"))
	if contains(all, "mcp-proxy.anthropic.com") {
		t.Fatal("policy-block entry must not be allowed")
	}
}

func TestACLName(t *testing.T) {
	if got := ACLName("my-repo-abc123.fix-login"); got != "my_repo_abc123_fix_login" {
		t.Fatal(got)
	}
}

func TestRenderGolden(t *testing.T) {
	golden(t, "squid.conf.golden", RenderMain("host.docker.internal", "7890"))
	golden(t, "squid-direct.conf.golden", RenderMain("", ""))
	golden(t, "task-block.conf.golden", RenderTask("demo-abc123.t1", true, false))
	golden(t, "task-cloudmcp.conf.golden", RenderTask("demo-abc123.t1", false, false))
	golden(t, "dedicated.conf.golden", RenderDedicated("demo-abc123.t1", true, false, "host.docker.internal", "7890"))
	golden(t, "dedicated-open.conf.golden", RenderDedicated("demo-abc123.t1", true, true, "", ""))
}

// dedicated 实例只服务一个 Task，所以不能有认证，也不能有 include 片段；
// 拦截规则必须排在放行之前，否则 policy-block 会被白名单放过去（M0-5）。
func TestRenderDedicatedShape(t *testing.T) {
	conf := string(RenderDedicated("demo.t1", true, false, "", ""))
	for _, no := range []string{"auth_param", "proxy_auth", "include ", "cache_peer"} {
		if strings.Contains(conf, no) {
			t.Errorf("dedicated conf should not contain %q:\n%s", no, conf)
		}
	}
	deny := strings.Index(conf, "http_access deny blocked")
	allow := strings.Index(conf, "http_access allow CONNECT SSL_ports allowed")
	if deny < 0 || allow < 0 || deny > allow {
		t.Fatalf("block rule must come before allow rules:\n%s", conf)
	}
	open := string(RenderDedicated("demo.t1", false, true, "", ""))
	if !strings.Contains(open, "http_access allow all") || strings.Contains(open, "allow.txt") {
		t.Fatalf("open mode:\n%s", open)
	}
	if up := string(RenderDedicated("demo.t1", false, false, "host.docker.internal", "7890")); !strings.Contains(up, "cache_peer host.docker.internal parent 7890") || !strings.Contains(up, "never_direct allow all") {
		t.Fatalf("upstream:\n%s", up)
	}
}

func TestUpsertPasswd(t *testing.T) {
	s := upsertPasswd("", "b.t", "h1")
	s = upsertPasswd(s, "a.t", "h2")
	s = upsertPasswd(s, "b.t", "h3")
	if s != "a.t:h2\nb.t:h3\n" {
		t.Fatalf("%q", s)
	}
	if s = upsertPasswd(s, "a.t", ""); s != "b.t:h3\n" {
		t.Fatalf("%q", s)
	}
	if s = upsertPasswd(s, "b.t", ""); s != "" {
		t.Fatalf("%q", s)
	}
}

func TestApr1MatchesOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not found")
	}
	for _, c := range [][2]string{{"secret", "abcdefgh"}, {"", "xy"}, {strings.Repeat("p", 40), "12345678"}} {
		out, err := exec.Command("openssl", "passwd", "-apr1", "-salt", c[1], c[0]).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := apr1(c[0], c[1]), strings.TrimSpace(string(out)); got != want {
			t.Errorf("apr1(%q,%q)=%s want %s", c[0], c[1], got, want)
		}
	}
}
