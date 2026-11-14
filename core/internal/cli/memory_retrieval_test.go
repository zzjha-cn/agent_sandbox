package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sandx/internal/projectmemory"
)

func TestMemorySearchSnapshotAndFeedbackWithoutDocker(t *testing.T) {
	a := &App{Home: t.TempDir(), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	run := func(args ...string) []byte {
		t.Helper()
		out := &bytes.Buffer{}
		a.Out = out
		cmd := a.memoryCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(args, err)
		}
		return out.Bytes()
	}
	run("project", "create", "demo")
	file := filepath.Join(t.TempDir(), "input.json")
	os.WriteFile(file, []byte(`{"kind":"troubleshooting","title":"GOMODCACHE prep","content":"Verify seeded module cache","source":"operator verified","memory_type":"procedural","requires":{"go.version":"1.27"},"experience":{"actions":"set GOMODCACHE","validation":"compiled in a new task","unknowns":"original root cause unknown"}}`), 0600)
	var entry projectmemory.Entry
	if err := json.Unmarshal(run("add", "--project", "demo", "--file", file), &entry); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(run("approve", entry.ID, "--project", "demo", "--digest", entry.Digest, "--reviewer", "operator", "--reason", "verified"), &entry)
	store := projectmemory.Store{Home: a.Home}
	before, _ := store.Load("demo")
	var result projectmemory.Snapshot
	json.Unmarshal(run("search", "--project", "demo", "--query", "GOMODCACHE"), &result)
	if len(result.Entries) != 0 || result.DecisionCounts["conditions_unknown"] != 1 {
		t.Fatal(result)
	}
	json.Unmarshal(run("search", "--project", "demo", "--query", "GOMODCACHE", "--facts", "go.version=1.27", "--limit", "1"), &result)
	if len(result.Entries) != 1 || result.Entries[0].ID != entry.ID {
		t.Fatal(result)
	}
	after, _ := store.Load("demo")
	if before.Sequence != after.Sequence {
		t.Fatal("search mutated project")
	}
	if _, err := os.Stat(filepath.Join(a.Home, "project-memory", "snapshots")); !os.IsNotExist(err) {
		t.Fatal("search pinned snapshot")
	}
	var snapshot projectmemory.Snapshot
	json.Unmarshal(run("snapshot", "--project", "demo", "--query", "GOMODCACHE", "--facts", "go.version=1.27"), &snapshot)
	if snapshot.Digest != result.Digest { // limit=1 vs default changes snapshot identity; compare entries instead.
		if len(snapshot.Entries) != 1 || snapshot.Entries[0].Digest != entry.Digest {
			t.Fatal(snapshot)
		}
	}
	run("feedback", "add", entry.ID, "--project", "demo", "--digest", entry.Digest, "--kind", "validated", "--note", "operator reports successful compilation", "--task", "demo-task")
	var feedback []projectmemory.Feedback
	json.Unmarshal(run("feedback", "list", "--project", "demo"), &feedback)
	if len(feedback) != 1 || feedback[0].EntryDigest != entry.Digest {
		t.Fatal(feedback)
	}
	p, _ := store.Load("demo")
	if p.Latest()[0].Digest != entry.Digest {
		t.Fatal("feedback approved or renewed memory")
	}
	if a.Docker != nil {
		t.Fatal("memory retrieval initialized Docker")
	}
}
