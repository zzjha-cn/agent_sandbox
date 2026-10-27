package proxy

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"sandx/internal/docker"
	"sandx/internal/fsutil"
)

func checkpointDocker(t *testing.T, connected bool) *docker.Client {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CHECKPOINT_LOG", filepath.Join(dir, "calls"))
	networks := "{}"
	if connected {
		networks = `{"task-net":{}}`
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CHECKPOINT_LOG"
if [ "$1 $2" = 'container inspect' ]; then
 case "$4" in
  *NetworkSettings*) echo '` + networks + `' ;;
  *) echo '{"Running":true}' ;;
 esac
fi
`
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return &docker.Client{Bin: bin}
}

func TestSharedAttachmentRollbackRestoresOnlyCurrentTask(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(map[bool]string{false: "new network", true: "existing network"}[connected], func(t *testing.T) {
			p := Shared{Docker: checkpointDocker(t, connected), Dir: t.TempDir()}
			spec := TaskSpec{TaskID: "ws.task", Network: "task-net", CredFile: filepath.Join(t.TempDir(), "proxy.cred"), Allow: []string{"new.invalid"}, Open: true}
			original := map[string]string{p.path("passwd"): "ws.task:oldhash\nother:keep\n", p.path("tasks", spec.TaskID+".conf"): "old config", p.path("allow", spec.TaskID+".txt"): "old.invalid\n", spec.CredFile: strings.Repeat("a", 64) + "\n"}
			for path, data := range original {
				if err := fsutil.AtomicWrite(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e, rollback, err := BeginAttachment(p, spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.AttachTask(spec); err != nil {
				t.Fatal(err)
			}
			current, err := os.ReadFile(p.path("passwd"))
			if err != nil {
				t.Fatal(err)
			}
			if err := fsutil.AtomicWrite(p.path("passwd"), append(current, []byte("newer:retain\n")...), 0644); err != nil {
				t.Fatal(err)
			}
			if err := rollback(context.Background()); err != nil {
				t.Fatal(err)
			}
			for path, data := range original {
				got, err := os.ReadFile(path)
				if path == p.path("passwd") {
					data += "newer:retain\n"
					a, b := strings.Split(strings.TrimSpace(string(got)), "\n"), strings.Split(strings.TrimSpace(data), "\n")
					sort.Strings(a)
					sort.Strings(b)
					got = []byte(strings.Join(a, "\n"))
					data = strings.Join(b, "\n")
				}
				if err != nil || string(got) != data {
					t.Fatalf("%s: %q want %q (%v)", path, got, data, err)
				}
			}
			calls, err := os.ReadFile(os.Getenv("CHECKPOINT_LOG"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "network disconnect") == connected {
				t.Fatal("changed preexisting network attachment", string(calls))
			}
		})
	}
}

func TestDedicatedAttachmentRollbackPreservesOldInstanceAndConfig(t *testing.T) {
	p := Dedicated{Docker: checkpointDocker(t, true), Name: "old-proxy", Dir: t.TempDir()}
	conf := p.path("squid.conf")
	if err := fsutil.AtomicWrite(conf, []byte("old config"), 0600); err != nil {
		t.Fatal(err)
	}
	e, rollback, err := BeginAttachment(p, TaskSpec{TaskID: "ws.task", Network: "task-net"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.AttachTask(TaskSpec{TaskID: "ws.task", Network: "task-net", Open: true}); err != nil {
		t.Fatal(err)
	}
	if err := rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(conf)
	if err != nil || string(got) != "old config" {
		t.Fatalf("lost config %q %v", got, err)
	}
	calls, err := os.ReadFile(os.Getenv("CHECKPOINT_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "rm -f old-proxy") {
		t.Fatal("deleted preexisting proxy")
	}
}

func TestSharedCleanupLockHonorsIndependentDeadline(t *testing.T) {
	p := Shared{Dir: t.TempDir()}
	unlock, err := p.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if release, err := p.lockContext(ctx); err != context.DeadlineExceeded {
		if release != nil {
			release()
		}
		t.Fatalf("cleanup lock ignored deadline: %v", err)
	}
}
