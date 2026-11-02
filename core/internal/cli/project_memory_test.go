package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sandx/internal/execution"
	"sandx/internal/projectmemory"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

func TestProjectMemoryCommandsWithoutDocker(t *testing.T) {
	a := &App{Home: t.TempDir(), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	run := func(args ...string) []byte {
		t.Helper()
		out := &bytes.Buffer{}
		a.Out = out
		c := a.memoryCmd()
		c.SetArgs(args)
		if e := c.Execute(); e != nil {
			t.Fatal(args, e)
		}
		return out.Bytes()
	}
	run("project", "create", "demo")
	entry := filepath.Join(t.TempDir(), "entry.json")
	os.WriteFile(entry, []byte(`{"kind":"testing","title":"Docker","content":"Check Docker first","source":"operator verified tests"}`), 0600)
	b := run("add", "--project", "demo", "--file", entry)
	var v projectmemory.Entry
	if e := json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	run("approve", v.ID, "--project", "demo", "--digest", v.Digest, "--reviewer", "operator", "--reason", "verified")
	b = run("list", "--project", "demo")
	var entries []projectmemory.Entry
	if e := json.Unmarshal(b, &entries); e != nil || len(entries) != 1 || entries[0].Status != "approved" {
		t.Fatal(string(b), e)
	}
	if a.Docker != nil {
		t.Fatal("local memory unexpectedly requires Docker")
	}
	c := a.memoryCmd()
	c.SetArgs([]string{"approve", v.ID, "--project", "demo", "--digest", v.Digest, "--reviewer", "operator", "--reason", "stale"})
	if e := c.Execute(); e == nil {
		t.Fatal("stale review accepted")
	}
}

func TestCleanupReminderUsesProviderAndMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SBX_HOME", home)
	for _, tc := range []struct {
		provider, mode string
		want           bool
	}{{"claude", "native", true}, {"codex", "native", false}, {"claude", "managed", false}, {"claude", "off", false}} {
		t.Run(tc.provider+tc.mode, func(t *testing.T) {
			ttask := task.Task{Home: home, Name: tc.provider + tc.mode, WS: workspace.Workspace{ID: "repo"}}
			if e := ttask.WriteMeta(task.Meta{Agent: tc.provider}); e != nil {
				t.Fatal(e)
			}
			session := execution.MemorySession{Schema: "sbx.memory-session/v1", TaskKey: ttask.ID(), Provider: tc.provider, Mode: tc.mode}
			if tc.mode == "managed" {
				session.Snapshot = &projectmemory.Snapshot{}
			}
			b, _ := json.Marshal(session)
			dir := execution.MemoryTaskDir(home, ttask.ID())
			os.MkdirAll(dir, 0700)
			os.WriteFile(filepath.Join(dir, "session.json"), b, 0600)
			if shouldRemindClaudeMemory(ttask) != tc.want {
				t.Fatal("wrong cleanup reminder")
			}
		})
	}
}
