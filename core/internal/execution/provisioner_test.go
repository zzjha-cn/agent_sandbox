package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sandx/internal/agent"
	"sandx/internal/proxy"
	"sandx/internal/task"
	"testing"

	"sandx/internal/docker"
)

func TestInterruptedContainerCreationCleansOnlyOwnedContainer(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(map[bool]string{true: "partially created", false: "foreign name conflict"}[created], func(t *testing.T) {
			dir := t.TempDir()
			shim := filepath.Join(dir, "docker")
			removed := filepath.Join(dir, "removed")
			t.Setenv("TEST_REMOVED", removed)
			value := "foreign"
			if created {
				value = "owned"
			}
			t.Setenv("TEST_CONTAINER", value)
			script := `#!/bin/sh
case "$1" in
 run) exit 1 ;;
 ps) if [ "$TEST_CONTAINER" = owned ]; then printf '%s\n' '{"Names":"owned"}'; fi ;;
 rm) printf '%s' "$*" > "$TEST_REMOVED" ;;
 *) exit 2 ;;
esac
`
			if err := os.WriteFile(shim, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			var undo Undo
			if err := createOwnedContainer(&docker.Client{Bin: shim}, docker.RunSpec{Name: "owned", Image: "fixture"}, &undo); err == nil {
				t.Fatal("expected docker run failure")
			}
			if failures := undo.Run(context.Background()); len(failures) != 0 {
				t.Fatal(failures)
			}
			_, err := os.Stat(removed)
			if created && err != nil {
				t.Fatal("partially created container leaked")
			}
			if !created && !os.IsNotExist(err) {
				t.Fatal("removed a foreign container")
			}
		})
	}
}

type resumeEgress struct{ proxy.Egress }

func (resumeEgress) DetachTask(string, string) error           { return nil }
func (resumeEgress) StopIfIdle() (bool, error)                 { return false, nil }
func (resumeEgress) Ensure() error                             { return nil }
func (resumeEgress) AttachTask(proxy.TaskSpec) (string, error) { return "", nil }

type resumeHost struct {
	ProvisioningHost
	client *docker.Client
	cancel context.CancelFunc
}

func (h resumeHost) DockerClient() *docker.Client { return h.client }
func (h resumeHost) DockerClientWithContext(ctx context.Context) *docker.Client {
	return h.client.WithContext(ctx)
}
func (resumeHost) Egress(task.Task) proxy.Egress              { return resumeEgress{} }
func (resumeHost) ProxySpec(task.Task, string) proxy.TaskSpec { return proxy.TaskSpec{} }
func (resumeHost) EnsureServices(task.Task, *Undo) error      { return nil }
func (resumeHost) EnsureForwards(task.Task, *Undo, bool) ([]string, []task.Link, error) {
	return nil, nil, nil
}
func (resumeHost) WarnForwardExposure([]task.Link)                   {}
func (resumeHost) HostClaude() agent.HostClaude                      { return agent.HostClaude{} }
func (resumeHost) GenInput(agent.HostClaude, Request) agent.GenInput { return agent.GenInput{} }
func (resumeHost) Logf(string, ...any)                               {}
func (h resumeHost) StartAgent(context.Context, task.Task, Request) error {
	h.cancel()
	return context.Canceled
}

func TestResumeFailureStopsContainerAndPreservesTask(t *testing.T) {
	for _, mode := range []string{"start-failed", "agent-cancelled", "cleanup-failed"} {
		t.Run(mode, func(t *testing.T) {
			req := testRequest("test")
			req.Task.Home = t.TempDir()
			if err := req.Task.WriteMeta(task.Meta{Task: req.Task.Name}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(req.Task.Worktree(), 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(req.Task.Worktree(), "keep")
			if err := os.WriteFile(marker, []byte("work"), 0644); err != nil {
				t.Fatal(err)
			}
			stopped := filepath.Join(req.Task.StateDir(), "stop-observed")
			t.Setenv("TEST_STOPPED", stopped)
			t.Setenv("TEST_MODE", mode)
			shim := filepath.Join(t.TempDir(), "docker")
			script := `#!/bin/sh
case "$1" in
 start) if [ "$TEST_MODE" = start-failed ];then exit 1;fi ;;
 container) printf '%s' '{"Running":true}' ;;
 stop) touch "$TEST_STOPPED";if [ "$TEST_MODE" = cleanup-failed ];then exit 1;fi ;;
 *) exit 2 ;;
esac
`
			if err := os.WriteFile(shim, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			host := resumeHost{client: (&docker.Client{Bin: shim}).WithContext(ctx), cancel: cancel}
			err := NewProvisioner(host).Resume(ctx, req, docker.State{})
			var failure *ExecutionError
			if !errors.As(err, &failure) || !failure.CleanupAttempted {
				t.Fatalf("cleanup missing: %v", err)
			}
			if mode != "start-failed" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost primary cancellation: %v", err)
			}
			if (len(failure.Cleanup) > 0) != (mode == "cleanup-failed") {
				t.Fatalf("wrong cleanup errors: %v", err)
			}
			for _, path := range []string{stopped, marker, filepath.Join(req.Task.StateDir(), "meta.json")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("%s: %v", path, err)
				}
			}
		})
	}
}
