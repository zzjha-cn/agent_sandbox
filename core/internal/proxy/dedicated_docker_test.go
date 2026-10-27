package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sandx/internal/docker"
)

// A peer resolvable only on the egress network catches startup DNS races without
// public DNS, Internet access, credentials or a model request.
func TestRealDockerDedicatedUpstreamStartup(t *testing.T) {
	if os.Getenv("SBX_REAL_DOCKER_E2E") != "1" {
		t.Skip("set SBX_REAL_DOCKER_E2E=1")
	}
	c := docker.New(false)
	name := fmt.Sprintf("sbx-peer-start-%d", time.Now().UnixNano())
	internal, egress, upstream := name+"-internal", name+"-egress", name+"-upstream"
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := Dedicated{Docker: c, Dir: filepath.Join(dir, "proxy"), Name: name, Egress: egress,
		UpstreamHost: "e2e-upstream", UpstreamPort: "8080"}
	t.Cleanup(func() { c.Rm(name); c.Rm(upstream); c.NetworkRm(internal); c.NetworkRm(egress) })
	for _, n := range []string{internal, egress} {
		if err := c.NetworkCreate(n, n == internal, map[string]string{"sbx.e2e": name}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = c.RunContainer(docker.RunSpec{Name: upstream, Image: "busybox", Network: egress, Aliases: []string{"e2e-upstream"},
		Cmd: []string{"sh", "-c", `while :; do printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK' | nc -l -p 8080; done`}})
	if err != nil {
		t.Fatal(err)
	}
	url, err := d.AttachTask(TaskSpec{TaskID: name, Network: internal, Open: true})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Run("run", "--rm", "--network", internal, "curlimages/curl", "--max-time", "10", "-fsS", "-x", url, "http://no-public-dns.invalid/")
	if err != nil || strings.TrimSpace(out) != "OK" {
		t.Fatalf("peer unavailable on first request: out=%q err=%v", out, err)
	}
}
