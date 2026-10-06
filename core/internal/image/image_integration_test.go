//go:build docker

package image

import (
	"os"
	"strings"
	"testing"

	"sandx/internal/docker"
)

// 首次运行需要构建镜像（数分钟）。SBX_IT_UPSTREAM 设置构建代理，如 http://host.docker.internal:7890。
func TestIntegrationEnsure(t *testing.T) {
	c := docker.New(testing.Verbose())
	in, _ := BuiltinInputs("web-go", "latest", os.Getuid(), os.Getgid())
	b := Builder{Docker: c, Upstream: os.Getenv("SBX_IT_UPSTREAM"), Out: os.Stderr}
	tag, err := b.Ensure(in)
	if err != nil {
		t.Fatal(err)
	}
	if tag2, err := b.Ensure(in); err != nil || tag2 != tag {
		t.Fatal(tag2, err)
	}
	out, err := c.Run("run", "--rm", tag, "bash", "-lc",
		"id -u; id -un; claude --version; tmux -V; jq --version; go version; node --version; pnpm --version; echo $GOMODCACHE")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	t.Log(out)
	if lines[0] == "0" || lines[1] != "agent" {
		t.Fatalf("should run as non-root agent: %v", lines[:2])
	}
	if !strings.Contains(out, "go version go") || !strings.Contains(out, "Claude Code") {
		t.Fatal("missing go or claude in login shell PATH")
	}
	if lines[len(lines)-1] != "/sbx/cache/go-mod" {
		t.Fatal("GOMODCACHE", lines[len(lines)-1])
	}
}
