package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/agent"
	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/prep"
	"sandx/internal/proxy"
	"sandx/internal/task"
)

var matrixFailure = errors.New("injected provisioning failure")

type matrixHost struct {
	ProvisioningHost
	client *docker.Client
	fail   string
}

func (h matrixHost) at(step string) error {
	if h.fail == step {
		return matrixFailure
	}
	return nil
}
func (h matrixHost) DockerClient() *docker.Client { return h.client }
func (h matrixHost) DockerClientWithContext(ctx context.Context) *docker.Client {
	return h.client.WithContext(ctx)
}
func (matrixHost) Logf(string, ...any)            {}
func (matrixHost) EffectiveConfig() config.Config { return config.Config{DefaultAgent: "codex"} }
func (h matrixHost) PrepareWorktree(_ context.Context, t task.Task, _ string) (string, error) {
	if err := os.MkdirAll(t.Worktree(), 0755); err != nil {
		return "", err
	}
	return "base", h.at("worktree")
}
func (h matrixHost) AgentImage() (string, error) { return "fixture", h.at("image") }
func (h matrixHost) PrepLoad(task.Task) (string, *prep.Plan, error) {
	return "fixture", &prep.Plan{Manifest: &prep.Manifest{}}, h.at("prep")
}
func (h matrixHost) EgressOf(task.Task, string) proxy.Egress { return matrixEgress{host: h} }
func (h matrixHost) Egress(task.Task) proxy.Egress           { return matrixEgress{host: h} }
func (h matrixHost) EgressWithContext(context.Context, task.Task, string) proxy.Egress {
	return matrixEgress{host: h}
}
func (matrixHost) ProxySpec(task.Task, string) proxy.TaskSpec  { return proxy.TaskSpec{} }
func (matrixHost) WarnSkippedNotifyHosts()                     {}
func (matrixHost) WarnForwardExposure([]task.Link)             {}
func (h matrixHost) EnsureServices(_ task.Task, u *Undo) error { return h.at("services") }
func (h matrixHost) EnsureForwards(task.Task, *Undo, bool) ([]string, []task.Link, error) {
	return nil, nil, h.at("forwards")
}
func (h matrixHost) EnsureVolumes(task.Task, string, *Undo) (map[string]bool, error) {
	return nil, h.at("volumes")
}
func (h matrixHost) PrepSeed(task.Task, string, *prep.Plan, map[string]bool) error {
	return h.at("seed")
}
func (matrixHost) HostClaude() agent.HostClaude { return agent.HostClaude{} }
func (h matrixHost) GenInput(agent.HostClaude, Request) agent.GenInput {
	in := agent.GenInput{Prompt: "new"}
	if h.fail == "gen" {
		in.Host = agent.HostClaude{HasMD: true, Dir: "/nonexistent/sbx-matrix"}
	}
	return in
}
func (h matrixHost) APIKey() (APIKey, error) { return APIKey{}, h.at("key") }
func (h matrixHost) InstallCAs(t task.Task, _ string, _ []string) error {
	if h.fail == "meta" {
		path := filepath.Join(t.StateDir(), "meta.json")
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Mkdir(path, 0755)
	}
	return h.at("ca")
}
func (h matrixHost) StartAgent(_ context.Context, tk task.Task, _ Request) error {
	if h.fail == "agent" {
		for _, name := range []string{"status.json", "run.exit", "codex-session"} {
			if err := os.WriteFile(filepath.Join(tk.StateDir(), name), []byte("new launch state"), 0644); err != nil {
				return err
			}
		}
	}
	return h.at("agent")
}
func (h matrixHost) MergePrepAllow([]string) error { return h.at("allow") }

type matrixEgress struct {
	proxy.Egress
	host matrixHost
}

func (e matrixEgress) Ensure() error                             { return e.host.at("egress") }
func (e matrixEgress) AttachTask(proxy.TaskSpec) (string, error) { return "", e.host.at("attach") }
func (matrixEgress) DetachTask(string, string) error             { return nil }
func (matrixEgress) StopIfIdle() (bool, error)                   { return false, nil }

func matrixClient(t *testing.T, fail string) *docker.Client {
	t.Helper()
	script := `#!/bin/sh
case "$1 $2" in
 'network inspect') echo 'No such network' >&2;exit 1 ;;
 'network create') if [ "$MATRIX_FAIL" = network ];then exit 73;fi ;;
 'network ls'|'ps -a') exit 0 ;;
 'container inspect') echo '{"Running":true}' ;;
 'stop -t') exit 0 ;;
 'run -d') if [ "$MATRIX_FAIL" = container ];then exit 73;fi ;;
 *) exit 0 ;;
esac
`
	shim := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(shim, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MATRIX_FAIL", fail)
	return &docker.Client{Bin: shim}
}

func TestCreateStageFaultMatrixPreservesExistingTaskState(t *testing.T) {
	stages := map[string]string{"worktree": "preparing the worktree", "image": "preparing the image", "prep": "reading the prep image", "egress": "preparing the egress proxy", "network": "creating the task network", "attach": "attaching to the egress proxy", "services": "starting the declared services", "forwards": "opening the declared forwards", "volumes": "preparing volumes", "seed": "seeding dependencies from the prep image", "gen": "rendering generated files", "key": "creating the container", "container": "creating the container", "ca": "installing the CA certificates declared by prep", "meta": "writing meta", "agent": "starting the agent"}
	for fail, stage := range stages {
		t.Run(fail, func(t *testing.T) {
			req := testRequest("test")
			req.Task.Home = t.TempDir()
			tk := req.Task
			old := map[string]string{filepath.Join(tk.StateDir(), "run.exit"): "17", filepath.Join(tk.StateDir(), "codex-session"): "22222222-2222-4222-8222-222222222222", filepath.Join(tk.StateDir(), "meta.json"): "{\"agent\":\"codex\",\"task\":\"old\"}\n", filepath.Join(tk.StateDir(), "status.json"): "{\"state\":\"exited\"}\n", filepath.Join(tk.GenDir(), "prompt.txt"): "old prompt\n"}
			for path, data := range old {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := NewProvisioner(matrixHost{client: matrixClient(t, fail), fail: fail}).Create(context.Background(), req)
			var failure *ExecutionError
			if !errors.As(err, &failure) || failure.Stage != stage || !failure.CleanupAttempted || len(failure.Cleanup) != 0 {
				t.Fatalf("stage %s: %#v / %v", stage, failure, err)
			}
			if !strings.Contains(err.Error(), stage) {
				t.Fatal("missing diagnosis", err)
			}
			for path, data := range old {
				got, e := os.ReadFile(path)
				if e != nil || string(got) != data {
					t.Fatalf("lost old state %s: %q %v", path, got, e)
				}
				st, e := os.Stat(path)
				if e != nil || st.Mode().Perm() != 0600 {
					t.Fatalf("mode changed %s: %v", path, e)
				}
			}
			if _, err := os.Stat(tk.Worktree()); err != nil {
				t.Fatal("worktree removed", err)
			}
		})
	}
}

func TestResumeStageFaultMatrixPreservesExistingTaskState(t *testing.T) {
	for fail, stage := range map[string]string{"allow": "restoring prep allowlist", "egress": "restoring the egress proxy", "attach": "restoring the proxy attachment", "services": "restoring declared services", "forwards": "restoring declared forwards", "gen": "rendering generated files", "agent": "starting the agent"} {
		t.Run(fail, func(t *testing.T) {
			req := testRequest("test")
			req.Task.Home = t.TempDir()
			tk := req.Task
			if err := tk.WriteMeta(task.Meta{Agent: "codex", PrepAllow: []string{"example.invalid"}}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"status.json", "run.exit", "codex-session"} {
				if err := os.WriteFile(filepath.Join(tk.StateDir(), name), []byte("old launch state"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			oldMeta, err := os.ReadFile(filepath.Join(tk.StateDir(), "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(tk.GenDir(), 0755); err != nil {
				t.Fatal(err)
			}
			prompt := filepath.Join(tk.GenDir(), "prompt.txt")
			if err := os.WriteFile(prompt, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			err = NewProvisioner(matrixHost{client: matrixClient(t, fail), fail: fail}).Resume(context.Background(), req, docker.State{})
			var failure *ExecutionError
			if !errors.As(err, &failure) || failure.Stage != stage || len(failure.Cleanup) != 0 {
				t.Fatalf("%s: %#v %v", stage, failure, err)
			}
			for _, name := range []string{"status.json", "run.exit", "codex-session"} {
				got, err := os.ReadFile(filepath.Join(tk.StateDir(), name))
				if err != nil || string(got) != "old launch state" {
					t.Fatalf("old marker lost: %s", name)
				}
			}
			got, e := os.ReadFile(prompt)
			if e != nil || string(got) != "old" {
				t.Fatalf("prompt lost: %q %v", got, e)
			}
			got, e = os.ReadFile(filepath.Join(tk.StateDir(), "meta.json"))
			if e != nil || string(got) != string(oldMeta) {
				t.Fatalf("meta lost: %q %v", got, e)
			}
		})
	}
}
