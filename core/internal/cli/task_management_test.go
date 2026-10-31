package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sandx/internal/docker"
	"sandx/internal/protocol"
)

func TestTaskListIsOfflineAndDoesNotLoadBrokenConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SBX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("invalid = [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := &App{Out: &out, Err: &out, Docker: &docker.Client{Bin: "must-not-call-docker"}}
	cmd := app.taskCmd()
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result protocol.TaskList
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.SchemaVersion != protocol.TaskListSchema || result.Tasks == nil || len(result.Tasks) != 0 {
		t.Fatalf("%s %v", out.Bytes(), err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 {
		t.Fatal("read-only list mutated home", err)
	}
}

func TestTaskLogsRefusesUnknownWithoutDocker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SBX_HOME", home)
	var out bytes.Buffer
	app := &App{Out: &out, Err: &out, Docker: &docker.Client{Bin: "must-not-call-docker"}}
	cmd := app.taskCmd()
	cmd.SetArgs([]string{"logs", "missing"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("missing log accepted")
	}
}

func TestAgentLogCannotRedirectHostReads(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "host-secret")
	log := filepath.Join(root, "run.log")
	os.WriteFile(secret, []byte("must not be read"), 0600)
	if err := os.Symlink(secret, log); err != nil {
		t.Fatal(err)
	}
	if _, err := readLog(log); err == nil {
		t.Fatal("followed log symlink")
	}
	var out bytes.Buffer
	if _, err := copyFrom(log, &out, 0); err == nil || out.Len() != 0 {
		t.Fatal("follow copied host secret")
	}
}
