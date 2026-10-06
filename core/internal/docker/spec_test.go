package docker

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		os.WriteFile(p, []byte(got), 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("mismatch %s\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestRunSpecArgs(t *testing.T) {
	s := RunSpec{
		Name:    "sbx-demo-abc123-t1",
		Image:   "sbx/web-go:0123456789ab",
		Labels:  map[string]string{"sbx.task": "t1", "sbx.ws": "demo-abc123", "sbx.role": "agent"},
		Network: "sbx-demo-abc123-t1-net",
		Mounts: []Mount{
			{Source: "/Users/me/.sbx/worktrees/demo/t1", Target: "/Users/me/.sbx/worktrees/demo/t1"},
			{Source: "sbx-demo-abc123-t1-dep-0", Target: "/Users/me/.sbx/worktrees/demo/t1/node_modules", Volume: true},
			{Source: "/Users/me/.sbx/state/demo/t1/gen", Target: "/sbx/gen", ReadOnly: true},
		},
		Env:       []string{"SBX_TASK=t1", "HTTPS_PROXY=http://u:p@proxy:3128"},
		User:      "1000:1000",
		Workdir:   "/Users/me/.sbx/worktrees/demo/t1",
		Resources: Resources{CPUs: 2, Memory: "3g", Pids: 1024},
	}
	golden(t, "runspec.golden", strings.Join(s.Args(), "\n")+"\n")
}

func TestBuildArgs(t *testing.T) {
	b := BuildSpec{ContextDir: "/tmp/ctx", Dockerfile: "Dockerfile", Tag: "sbx/x:1",
		BuildArgs: map[string]string{"UID": "501", "BASE": "debian"}, Labels: map[string]string{"sbx.kind": "image"}}
	got := strings.Join(b.Args(), " ")
	want := "build -t sbx/x:1 -f Dockerfile --build-arg BASE=debian --build-arg UID=501 --label sbx.kind=image /tmp/ctx"
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestErrorIncludesCommand(t *testing.T) {
	e := &Error{Args: []string{"run", "--bogus", "a b"}, Stderr: "unknown flag: --bogus\n", Err: os.ErrInvalid}
	s := e.Error()
	if !strings.Contains(s, "docker run --bogus 'a b'") || !strings.Contains(s, "unknown flag") {
		t.Fatal(s)
	}
}

func TestExecArgs(t *testing.T) {
	got := strings.Join(execArgs("c", ExecOpts{User: "agent", Workdir: "/w", Env: []string{"A=1"}, TTY: true}, []string{"bash"}), " ")
	if got != "exec -it -u agent -w /w -e A=1 c bash" {
		t.Fatal(got)
	}
}

func TestRunSpecLogMaxSize(t *testing.T) {
	got := strings.Join(RunSpec{Image: "img", LogMaxSize: "10m"}.Args(), " ")
	if !strings.Contains(got, "--log-opt max-size=10m --log-opt max-file=2 img") {
		t.Fatal(got)
	}
}
