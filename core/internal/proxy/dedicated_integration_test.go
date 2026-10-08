//go:build docker

package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/docker"
)

// dedicated 模式的验收（M2-7）：白名单生效、策略拦截生效、不需要凭据、
// 日志能读出来并带上 Task 归属、stop/detach 的生命周期正确。
func TestIntegrationDedicated(t *testing.T) {
	c := docker.New(testing.Verbose())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	net := "sbx-it-ded-net"
	d := Dedicated{Docker: c, Dir: filepath.Join(dir, "proxy"), Name: "sbx-it-ded-proxy",
		TaskID: "ws-d.t1", Egress: "sbx-it-ded-egress",
		UpstreamHost: os.Getenv("SBX_IT_UPSTREAM_HOST"), UpstreamPort: os.Getenv("SBX_IT_UPSTREAM_PORT")}
	cleanup := func() {
		c.Rm(d.Name)
		c.NetworkRm(net)
		c.NetworkRm(d.Egress)
	}
	cleanup()
	t.Cleanup(cleanup)
	if err := c.NetworkCreate(net, true, map[string]string{"sbx.kind": "it"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	block := BuiltinList("policy-block")
	spec := TaskSpec{TaskID: d.TaskID, Network: net,
		Allow: RenderAllow(block, []string{"github.com", ".anthropic.com"}), Block: block}
	url, err := d.AttachTask(spec)
	if err != nil {
		t.Fatal(err)
	}
	if url != "http://proxy:3128" {
		t.Fatalf("proxy url %q：dedicated 不做认证，地址里不该有凭据", url)
	}

	curl := func(target string) string {
		out, _ := c.Run("run", "--rm", "--network", net, "curlimages/curl", "-s", "-o", "/dev/null",
			"-w", "%{http_code}/%{http_connect}", "--max-time", "20", "-x", url, target)
		return out
	}
	cases := []struct {
		name, target string
		ok           func(string) bool
	}{
		{"放行 github", "https://github.com/", func(s string) bool { return strings.HasPrefix(s, "200/") || strings.HasPrefix(s, "301/") }},
		{"拒绝名单外的域名", "https://example.com/", func(s string) bool { return strings.HasSuffix(s, "/403") }},
		{"策略拦截云端 MCP", "https://mcp-proxy.anthropic.com/", func(s string) bool { return strings.HasSuffix(s, "/403") }},
	}
	for _, cs := range cases {
		if got := curl(cs.target); !cs.ok(got) {
			t.Errorf("%s: got %s", cs.name, got)
		} else {
			t.Logf("%s: %s", cs.name, got)
		}
	}

	// 白名单热加载：加一条就不用重启
	spec.Allow = RenderAllow(block, []string{"github.com", ".anthropic.com", "example.com"})
	if _, err := d.AttachTask(spec); err != nil {
		t.Fatal(err)
	}
	if got := curl("https://example.com/"); strings.HasSuffix(got, "/403") {
		t.Errorf("热加载后 example.com 还是被拒：%s", got)
	}

	entries, err := d.AccessLog()
	if err != nil {
		t.Fatal(err)
	}
	denied, tagged := false, false
	for _, e := range entries {
		if e.Status == 403 {
			denied = true
		}
		if e.TaskID == d.TaskID {
			tagged = true
		}
		if e.Status == 407 {
			t.Errorf("dedicated 不该有认证失败：%+v", e)
		}
	}
	if !denied || !tagged {
		t.Errorf("access.log: denied=%v tagged=%v（%d 条）", denied, tagged, len(entries))
	}

	// agent 容器不存在 → StopIfIdle 停掉 sidecar；再 Attach 能起回来
	if stopped, err := d.StopIfIdle(); err != nil || !stopped {
		t.Fatalf("StopIfIdle: %v %v", stopped, err)
	}
	if _, err := d.AttachTask(spec); err != nil {
		t.Fatal(err)
	}
	if err := d.DetachTask(d.TaskID, net); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := c.Inspect(d.Name); err != nil || exists {
		t.Fatalf("sidecar 应该随 done 一起删掉：exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(d.Dir); !os.IsNotExist(err) {
		t.Fatalf("配置目录应该删掉：%v", err)
	}
}
