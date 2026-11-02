package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the generated launch scripts, not just string assertions: hostile-looking
// memory text must remain data, both providers must receive it, and native
// switches must reach new and resumed invocations in both UI modes.
func TestMemoryLaunchScripts(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, mode := range []string{"native", "managed", "off"} {
			for _, headless := range []bool{false, true} {
				for _, resume := range []bool{false, true} {
					label := provider + "/" + mode + "/" + map[bool]string{true: "headless", false: "interactive"}[headless] + "/" + map[bool]string{true: "resume", false: "new"}[resume]
					t.Run(label, func(t *testing.T) {
						if provider == "codex" {
							if _, e := exec.LookPath("jq"); e != nil {
								t.Skip("jq required")
							}
						}
						dir := t.TempDir()
						for _, p := range []string{"gen", "state", "bin", "home/.codex/sessions"} {
							if e := os.MkdirAll(filepath.Join(dir, p), 0700); e != nil {
								t.Fatal(e)
							}
						}
						memory := "REFERENCE: $(touch " + filepath.Join(dir, "injected") + ") `false` 'quotes' $HOME\n"
						prompt := "USER TASK: repair tests\n"
						for name, content := range map[string]string{"memory-context.md": memory, "prompt.txt": prompt, "memory-prompt.txt": memory + prompt} {
							if e := os.WriteFile(filepath.Join(dir, "gen", name), []byte(content), 0600); e != nil {
								t.Fatal(e)
							}
						}
						stub := `#!/bin/bash
printf '%s\n' "$@" > "$TEST_DIR/args"
printf '%s' "$CLAUDE_CODE_DISABLE_AUTO_MEMORY" > "$TEST_DIR/native-disabled"
if [[ "$1" == "exec" ]]; then cat > "$TEST_DIR/input"; fi
if [[ "$1" == "-p" ]]; then printf '%s' "$2" > "$TEST_DIR/input"; fi
printf '%s\n' '{"type":"thread.started","thread_id":"0199a213-81c0-7800-8aa1-bbab2a035a53"}'
`
						os.WriteFile(filepath.Join(dir, "bin", provider), []byte(stub), 0700)
						p, _ := ResolveProvider(provider)
						o := Launch{Headless: headless, Continue: resume, MemoryMode: mode, MemoryContext: mode == "managed"}
						if resume && provider == "codex" {
							o.SessionID = "0199a213-81c0-7800-8aa1-bbab2a035a53"
						}
						script, e := p.Command(o)
						if e != nil {
							t.Fatal(e)
						}
						script = strings.ReplaceAll(script, "/sbx", dir)
						script = strings.ReplaceAll(script, "/home/agent", filepath.Join(dir, "home"))
						script = strings.ReplaceAll(script, "kill 1", "exit 0")
						script = strings.ReplaceAll(script, "exec bash -l", "exit 0")
						cmd := exec.Command("bash", "-c", script)
						cmd.Dir = dir
						cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "TEST_DIR="+dir, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=")
						if out, e := cmd.CombinedOutput(); e != nil {
							t.Fatalf("%v %s", e, out)
						}
						args, _ := os.ReadFile(filepath.Join(dir, "args"))
						input, _ := os.ReadFile(filepath.Join(dir, "input"))
						if _, e = os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(e) {
							t.Fatal("memory executed as shell")
						}
						if mode == "managed" {
							body := string(args)
							if headless {
								body = string(input)
							}
							if !strings.Contains(body, strings.TrimSpace(memory)) {
								t.Fatalf("missing memory: %s", body)
							}
						} else if strings.Contains(string(args), "REFERENCE:") || strings.Contains(string(input), "REFERENCE:") {
							t.Fatal("memory leaked into disabled/native launch")
						}
						if provider == "codex" {
							want := "false"
							if mode == "native" {
								want = "true"
							}
							for _, key := range []string{"features.memories", "memories.generate_memories", "memories.use_memories"} {
								if !strings.Contains(string(args), key+"="+want) {
									t.Fatalf("missing %s in %s", key, args)
								}
							}
						} else {
							b, _ := os.ReadFile(filepath.Join(dir, "native-disabled"))
							if (string(b) == "1") != (mode != "native") {
								t.Fatal("Claude native control mismatch", string(b))
							}
						}
					})
				}
			}
		}
	}
}
