package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/agent"
	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/task"
)

func TestStarterSelectsCodexAndTaskSession(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "fresh"}[fresh], func(t *testing.T) {
			req := testRequest("test")
			req.Task.Home = t.TempDir()
			req.Config = config.Default()
			req.Config.DefaultAgent = "codex"
			req.Fresh = fresh
			if err := os.MkdirAll(req.Task.StateDir(), 0755); err != nil {
				t.Fatal(err)
			}
			sid := "0199a213-81c0-7800-8aa1-bbab2a035a53"
			if err := os.WriteFile(filepath.Join(req.Task.StateDir(), "codex-session"), []byte(sid), 0600); err != nil {
				t.Fatal(err)
			}
			shim := filepath.Join(t.TempDir(), "docker")
			t.Setenv("TEST_STATE", req.Task.StateDir())
			script := `#!/bin/sh
case "$*" in
 *"tmux has-session"*) exit 1 ;;
 *"codex login status"*) exit 0 ;;
 *"tmux new-session"*) printf '%s' "$*" > "$TEST_STATE/launched"; printf '0' > "$TEST_STATE/run.exit"; exit 0 ;;
 *) echo "unexpected invocation" >&2; exit 2 ;;
esac
`
			if err := os.WriteFile(shim, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			starter := AgentStarter{Docker: &docker.Client{Bin: shim}, APIKey: func() (APIKey, error) { return APIKey{}, nil },
				ImportMemory:    func(string) error { t.Fatal("Codex imported Claude memory"); return nil },
				InstallRuntimes: func(task.Task, agent.Runtime) {}, Logf: func(string, ...any) {}, RotateLog: func(string) error { return nil }}
			if err := starter.Start(context.Background(), req.Task, req); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(req.Task.StateDir(), "launched"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "claude") || !strings.Contains(string(data), "codex exec") {
				t.Fatalf("wrong provider: %s", data)
			}
			if strings.Contains(string(data), "resume "+sid) == fresh {
				t.Fatalf("wrong fresh/resume: %s", data)
			}
		})
	}
}
