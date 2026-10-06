//go:build docker

package proxy

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/docker"
)

// 复现 M0-4/M0-5 的身份隔离矩阵。宿主机需要上游代理时设置 SBX_IT_UPSTREAM_HOST/PORT
// （如 host.docker.internal / 7890）。
func TestIntegrationSharedIsolation(t *testing.T) {
	c := docker.New(testing.Verbose())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	s := Shared{Docker: c, Dir: dir, Name: "sbx-it-proxy", Egress: "sbx-it-egress",
		UpstreamHost: os.Getenv("SBX_IT_UPSTREAM_HOST"), UpstreamPort: os.Getenv("SBX_IT_UPSTREAM_PORT")}
	netA, netB := "sbx-it-a", "sbx-it-b"
	cleanup := func() {
		c.Rm(s.Name)
		for _, n := range []string{netA, netB, s.Egress} {
			c.NetworkRm(n)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, n := range []string{netA, netB} {
		if err := c.NetworkCreate(n, true, map[string]string{"sbx.kind": "it"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnsureShared(); err != nil {
		t.Fatal(err)
	}
	block := BuiltinList("policy-block")
	urlA, err := s.AttachTask(TaskSpec{TaskID: "ws-a.t1", Network: netA, CredFile: filepath.Join(dir, "a.cred"),
		Allow: RenderAllow(block, []string{"github.com", ".anthropic.com"}), Block: block})
	if err != nil {
		t.Fatal(err)
	}
	urlB, err := s.AttachTask(TaskSpec{TaskID: "ws-b.t2", Network: netB, CredFile: filepath.Join(dir, "b.cred"),
		Allow: []string{".anthropic.com"}, Block: block})
	if err != nil {
		t.Fatal(err)
	}
	// 同一个 Task 再 attach 一次，token 不变
	if again, err := s.AttachTask(TaskSpec{TaskID: "ws-a.t1", Network: netA, CredFile: filepath.Join(dir, "a.cred"),
		Allow: RenderAllow(block, []string{"github.com", ".anthropic.com"}), Block: block}); err != nil || again != urlA {
		t.Fatal("re-attach changed token", err)
	}

	curl := func(network, proxyURL, target string) string {
		out, _ := c.Run("run", "--rm", "--network", network, "curlimages/curl", "-s", "-o", "/dev/null",
			"-w", "%{http_code}/%{http_connect}", "--max-time", "20", "-x", proxyURL, target)
		return out
	}
	ub, _ := url.Parse(urlB)
	tokenB, _ := ub.User.Password()
	cases := []struct {
		name, network, proxy, target string
		ok                           func(string) bool
	}{
		{"A 放行 github", netA, urlA, "https://github.com/", func(s string) bool { return strings.HasPrefix(s, "200/") || strings.HasPrefix(s, "301/") }},
		{"B 拒绝 github", netB, urlB, "https://github.com/", func(s string) bool { return strings.HasSuffix(s, "/403") }},
		{"B 放行 anthropic", netB, urlB, "https://api.anthropic.com/", func(s string) bool { return !strings.HasPrefix(s, "000/") }},
		{"冒用 A 的名字", netB, "http://ws-a.t1:" + tokenB + "@proxy:3128", "https://github.com/", func(s string) bool { return strings.HasSuffix(s, "/407") }},
		{"不带凭据", netA, "http://proxy:3128", "https://github.com/", func(s string) bool { return strings.HasSuffix(s, "/407") }},
		{"策略拦截云端 MCP", netA, urlA, "https://mcp-proxy.anthropic.com/", func(s string) bool { return strings.HasSuffix(s, "/403") }},
	}
	for _, cs := range cases {
		got := curl(cs.network, cs.proxy, cs.target)
		if !cs.ok(got) {
			t.Errorf("%s: got %s", cs.name, got)
		} else {
			t.Logf("%s: %s", cs.name, got)
		}
	}

	if err := s.DetachTask("ws-a.t1", netA); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks", "ws-a.t1.conf")); !os.IsNotExist(err) {
		t.Fatal("fragment should be removed")
	}
	pw, _ := os.ReadFile(filepath.Join(dir, "passwd"))
	if strings.Contains(string(pw), "ws-a.t1:") || !strings.Contains(string(pw), "ws-b.t2:") {
		t.Fatalf("passwd after detach: %s", pw)
	}
	if got := curl(netB, urlB, "https://api.anthropic.com/"); strings.HasPrefix(got, "000/") {
		t.Errorf("B should still work after A detached: %s", got)
	}
	logs, _ := c.Exec(s.Name, docker.ExecOpts{}, "cat", "/var/log/squid/access.log")
	if !strings.Contains(logs, "TCP_DENIED/403") || !strings.Contains(logs, "ws-a.t1") {
		t.Errorf("access.log missing expected entries:\n%s", logs)
	}
}
