package proxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sandx/internal/docker"
)

func TestRealDockerSharedAttachmentRollback(t *testing.T) {
	if os.Getenv("SBX_REAL_DOCKER_E2E") != "1" {
		t.Skip("set SBX_REAL_DOCKER_E2E=1")
	}
	c := docker.New(false)
	name := fmt.Sprintf("sbx-checkpoint-%d", time.Now().UnixNano())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := Shared{Docker: c, Dir: filepath.Join(dir, "proxy"), Name: name, Egress: name + "-egress"}
	network := name + "-task"
	t.Cleanup(func() {
		if err := c.Rm(name); err != nil && !docker.IsNotFound(err) {
			t.Error(err)
		}
		if err := c.NetworkRm(network); err != nil {
			t.Error(err)
		}
		if err := c.NetworkRm(p.Egress); err != nil {
			t.Error(err)
		}
	})
	if err := c.NetworkCreate(network, true, map[string]string{"sbx.e2e": name}); err != nil {
		t.Fatal(err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	task := TaskSpec{TaskID: "fixture.a", Network: network, CredFile: filepath.Join(dir, "a.cred"), Allow: []string{"before.invalid"}}
	if _, err := p.AttachTask(task); err != nil {
		t.Fatal(err)
	}
	oldAllow, err := os.ReadFile(p.path("allow", task.TaskID+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	oldCred, err := os.ReadFile(task.CredFile)
	if err != nil {
		t.Fatal(err)
	}
	oldPasswd, err := os.ReadFile(p.path("passwd"))
	if err != nil {
		t.Fatal(err)
	}
	e, rollback, err := BeginAttachment(p, task)
	if err != nil {
		t.Fatal(err)
	}
	task.Allow = []string{"after.invalid"}
	task.Open = true
	if _, err := e.AttachTask(task); err != nil {
		t.Fatal(err)
	}
	other := TaskSpec{TaskID: "fixture.b", Network: network, CredFile: filepath.Join(dir, "b.cred"), Open: true}
	if _, err := p.AttachTask(other); err != nil {
		t.Fatal(err)
	}
	newer, err := os.ReadFile(p.path("passwd"))
	if err != nil {
		t.Fatal(err)
	}
	var otherLine string
	for _, line := range strings.Split(string(newer), "\n") {
		if strings.HasPrefix(line, "fixture.b:") {
			otherLine = line
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{p.path("allow", task.TaskID+".txt"): oldAllow, task.CredFile: oldCred} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("state not restored: %s", filepath.Base(path))
		}
	}
	current, err := os.ReadFile(p.path("passwd"))
	if err != nil {
		t.Fatal(err)
	}
	if otherLine == "" || !strings.Contains(string(current), otherLine) || !strings.Contains(string(current), strings.TrimSpace(string(oldPasswd))) {
		t.Fatal("rollback changed another Task or lost original credential")
	}
	if connected, err := c.NetworkConnected(network, name); err != nil || !connected {
		t.Fatalf("old attachment lost: %v", err)
	}
	if _, err := p.AccessLog(); err != nil {
		t.Fatal(err)
	}
}
