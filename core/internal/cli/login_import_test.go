package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/docker"
)

const fixtureCodexAuth = `{"auth_mode":"apikey","OPENAI_API_KEY":"sbx-synthetic-secret-for-tests"}`

func TestReadCodexAuth(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		fail       bool
	}{
		{"api key", fixtureCodexAuth, false},
		{"ChatGPT", `{"auth_mode":"chatgpt","tokens":{"id_token":"synthetic-id","access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`, false},
		{"legacy", `{"OPENAI_API_KEY":"synthetic-key"}`, false},
		{"malformed", `{"OPENAI_API_KEY":"DO_NOT_LEAK`, true},
		{"empty", `{}`, true},
		{"null", `null`, true},
		{"incomplete tokens", `{"auth_mode":"chatgpt","tokens":{"access_token":"DO_NOT_LEAK"}}`, true},
		{"unknown mode", `{"auth_mode":"DO_NOT_LEAK","OPENAI_API_KEY":"synthetic"}`, true},
		{"oversize", strings.Repeat("x", maxAuthImportBytes+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			for _, source := range []string{dir, path} {
				data, err := readCodexAuth(source)
				if (err != nil) != tc.fail {
					t.Fatalf("failure=%t: %v", tc.fail, err)
				}
				if err != nil && strings.Contains(err.Error(), "DO_NOT_LEAK") {
					t.Fatal("credentials leaked into error")
				}
				if !tc.fail && string(data) != tc.body {
					t.Fatal("credential bytes changed")
				}
			}
			data, _ := os.ReadFile(path)
			if string(data) != tc.body {
				t.Fatal("source was modified")
			}
		})
	}
	if _, err := readCodexAuth(t.TempDir()); err == nil || !strings.Contains(err.Error(), "keyring") {
		t.Fatalf("missing cache: %v", err)
	}
}

func TestCodexImportRejectsInvalidOptionsBeforeDocker(t *testing.T) {
	for _, args := range [][]string{
		{"claude", "--import", "not-a-path"},
		{"codex", "--import", ""},
		{"codex", "--import", "not-a-path", "--status"},
		{"codex", "--import", "not-a-path", "--logout"},
	} {
		cmd := (&App{}).loginCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

// Execute the actual installation script with a controlled Codex executable.
// The executable deliberately prints the entire cache: the wrapper must hide it.
func TestCodexImportIsAtomicAndPreservesOtherProvider(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		existing, force, reject, race bool
		want                          string
	}{
		{"first import", false, false, false, false, "imported"},
		{"preserve existing", true, false, false, false, "exists"},
		{"explicit replacement", true, true, false, false, "imported"},
		{"invalid staged auth", true, true, true, false, "invalid"},
		{"concurrent import", false, false, false, true, "exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "home")
			bin := filepath.Join(dir, "bin")
			for _, path := range []string{filepath.Join(home, ".codex"), filepath.Join(home, ".claude"), bin} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			dest := filepath.Join(home, ".codex/auth.json")
			if tc.existing {
				if err := os.WriteFile(dest, []byte("old-credentials"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{filepath.Join(home, ".claude/keep"), filepath.Join(home, ".codex/config.toml")} {
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			stub := "#!/bin/sh\ncat \"$CODEX_HOME/auth.json\"\ncat \"$CODEX_HOME/auth.json\" >&2\nexit 0\n"
			if tc.race {
				stub = strings.Replace(stub, "exit 0", "printf raced-credentials > \"$TEST_IMPORT_DEST\"\nexit 0", 1)
			}
			if tc.reject {
				stub = strings.Replace(stub, "exit 0", "exit 1", 1)
			}
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(stub), 0755); err != nil {
				t.Fatal(err)
			}
			mode := "preserve"
			if tc.force {
				mode = "force"
			}
			cmd := exec.Command("bash", "-c", strings.ReplaceAll(codexImportScript, "/home/agent", home), "sbx-import", mode)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TEST_IMPORT_DEST="+dest)
			cmd.Stdin = strings.NewReader(fixtureCodexAuth)
			out, err := cmd.CombinedOutput()
			if string(out) != tc.want || (err == nil) != (tc.want == "imported") {
				t.Fatalf("output=%q err=%v", out, err)
			}
			want := "old-credentials"
			if tc.race {
				want = "raced-credentials"
			}
			if tc.want == "imported" {
				want = fixtureCodexAuth
			}
			data, err := os.ReadFile(dest)
			if err != nil || string(data) != want {
				t.Fatalf("destination changed incorrectly: %v", err)
			}
			if tc.want == "imported" {
				st, _ := os.Stat(dest)
				if st.Mode().Perm() != 0600 {
					t.Fatalf("permissions: %o", st.Mode().Perm())
				}
			}
			for _, path := range []string{filepath.Join(home, ".claude/keep"), filepath.Join(home, ".codex/config.toml")} {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "keep" {
					t.Fatalf("other config changed: %s", path)
				}
			}
			matches, _ := filepath.Glob(filepath.Join(home, ".sbx-*"))
			if len(matches) != 0 {
				t.Fatalf("staging/lock leaked: %v", matches)
			}
		})
	}
}

func TestImportTransmitsCredentialsOnlyOverStdin(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "docker")
	received := filepath.Join(dir, "received")
	t.Setenv("TEST_IMPORT_STDIN", received)
	if err := os.WriteFile(shim, []byte("#!/bin/sh\ncat > \"$TEST_IMPORT_STDIN\"\nprintf imported\n"), 0755); err != nil {
		t.Fatal(err)
	}
	var trace, out bytes.Buffer
	a := &App{Docker: &docker.Client{Bin: shim, Verbose: true, Log: &trace}, Out: &out}
	if err := a.importCodexAuth("fixture-image", []byte(fixtureCodexAuth), false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(trace.String(), "sbx-synthetic-secret") || strings.Contains(out.String(), "sbx-synthetic-secret") {
		t.Fatal("secret leaked into output or command trace")
	}
	data, _ := os.ReadFile(received)
	if string(data) != fixtureCodexAuth {
		t.Fatal("stdin missing credentials")
	}
}
