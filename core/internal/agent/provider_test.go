package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/docker"
)

func TestCodexAuthenticationClassification(t *testing.T) {
	for _, tc := range []struct {
		name, output   string
		err            error
		logged, failed bool
	}{
		{"authenticated stderr-only", "", nil, true, false},
		{"unauthenticated", "", &docker.Error{Stderr: "Not logged in", Err: errors.New("exit 1")}, false, false},
		{"daemon unavailable", "", &docker.Error{Stderr: "Cannot connect to Docker", Err: errors.New("exit 1")}, false, true},
		{"executable missing", "", &docker.Error{Stderr: "codex: not found", Err: errors.New("exit 127")}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := ParseCodexStatus(tc.output, tc.err)
			if st.LoggedIn != tc.logged || (err != nil) != tc.failed {
				t.Fatalf("%+v %v", st, err)
			}
		})
	}
}

func TestCodexResumeRequiresExactSafeSession(t *testing.T) {
	p, _ := ResolveProvider("codex")
	for _, id := range []string{"", "--last", "x; touch injected"} {
		if _, err := p.Command(Launch{Headless: true, Continue: true, SessionID: id}); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	sid := "0199a213-81c0-7800-8aa1-bbab2a035a53"
	for _, headless := range []bool{false, true} {
		script, err := p.Command(Launch{Headless: headless, Continue: true, SessionID: sid})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(script, "resume "+sid) || strings.Contains(script, "--last") {
			t.Fatal(script)
		}
		if out, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("invalid shell: %s %v", out, err)
		}
	}
}

// Execute the actual generated shell against a controlled CLI, exercising
// stdin, pipeline exit codes, session persistence, status and raw log routing.
// Container shutdown is covered separately by the real Docker lifecycle test.
func TestCodexHeadlessShell(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required")
	}
	for _, code := range []string{"0", "17"} {
		t.Run(code, func(t *testing.T) {
			dir := t.TempDir()
			for _, p := range []string{"state", "gen", "bin", "home/.codex"} {
				if err := os.MkdirAll(filepath.Join(dir, p), 0755); err != nil {
					t.Fatal(err)
				}
			}
			prompt := "write 'quotes' and $variables\nsecond line"
			if err := os.WriteFile(filepath.Join(dir, "gen/prompt.txt"), []byte(prompt), 0600); err != nil {
				t.Fatal(err)
			}
			stub := "#!/bin/bash\ncat > \"$TEST_PROMPT\"\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"0199a213-81c0-7800-8aa1-bbab2a035a53\"}' '{\"type\":\"turn.completed\"}'\nexit " + code + "\n"
			if err := os.WriteFile(filepath.Join(dir, "bin/codex"), []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			p, _ := ResolveProvider("codex")
			script, err := p.Command(Launch{Headless: true})
			if err != nil {
				t.Fatal(err)
			}
			script = strings.ReplaceAll(script, "/sbx", dir)
			script = strings.ReplaceAll(script, "/home/agent", filepath.Join(dir, "home"))
			script = strings.ReplaceAll(script, "kill 1", "exit 0")
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "TEST_PROMPT="+filepath.Join(dir, "prompt-observed"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s %v", out, err)
			}
			for path, want := range map[string]string{"state/run.exit": code, "state/codex-session": "0199a213-81c0-7800-8aa1-bbab2a035a53", "prompt-observed": prompt} {
				data, err := os.ReadFile(filepath.Join(dir, path))
				if err != nil || string(data) != want {
					t.Fatalf("%s: %q %v", path, data, err)
				}
			}
			data, _ := os.ReadFile(filepath.Join(dir, "state/run.log"))
			if !strings.Contains(string(data), "thread.started") {
				t.Fatal("missing raw log")
			}
		})
	}
}

func TestCodexInteractiveSessionCaptureFiltersOtherWorktrees(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required")
	}
	dir := t.TempDir()
	for _, path := range []string{"state", "home/.codex/sessions", "bin"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	stub := `#!/bin/bash
jq -cn --arg cwd "$PWD" '{type:"session_meta",payload:{id:"0199a213-81c0-7800-8aa1-bbab2a035a53",cwd:$cwd}}' > "$CODEX_HOME/sessions/own.jsonl"
jq -cn '{type:"session_meta",payload:{id:"ffffffff-ffff-ffff-ffff-ffffffffffff",cwd:"/another/task"}}' > "$CODEX_HOME/sessions/other.jsonl"
`
	if err := os.WriteFile(filepath.Join(dir, "bin/codex"), []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}
	p, _ := ResolveProvider("codex")
	script, err := p.Command(Launch{})
	if err != nil {
		t.Fatal(err)
	}
	script = strings.ReplaceAll(script, "/sbx", dir)
	script = strings.ReplaceAll(script, "/home/agent", filepath.Join(dir, "home"))
	script = strings.ReplaceAll(script, "exec bash -l", "exit 0")
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "state/codex-session"))
	if err != nil || strings.TrimSpace(string(data)) != "0199a213-81c0-7800-8aa1-bbab2a035a53" {
		t.Fatalf("wrong task session: %q %v", data, err)
	}
}
