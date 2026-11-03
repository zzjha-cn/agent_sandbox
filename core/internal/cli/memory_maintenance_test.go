package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"sandx/internal/projectmemory"
)

func TestMemoryMaintenanceCLISharingReviewAndRenewal(t *testing.T) {
	home := t.TempDir()
	a := &App{Home: home, Err: &bytes.Buffer{}}
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
	run("project", "create", "source")
	run("project", "create", "destination")
	input := filepath.Join(t.TempDir(), "input.json")
	if e := os.WriteFile(input, []byte(`{"kind":"testing","title":"Integration tests","content":"Run offline tests before integration tests","source":"local checked evidence"}`), 0600); e != nil {
		t.Fatal(e)
	}
	var entry projectmemory.Entry
	if e := json.Unmarshal(run("add", "--project", "source", "--file", input), &entry); e != nil {
		t.Fatal(e)
	}
	due := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if e := json.Unmarshal(run("approve", entry.ID, "--project", "source", "--digest", entry.Digest, "--reviewer", "source operator", "--reason", "verified", "--review-due", due), &entry); e != nil {
		t.Fatal(e)
	}
	bundleFile := filepath.Join(t.TempDir(), "shared.json")
	if e := os.WriteFile(bundleFile, run("export", "--project", "source", "--entries", entry.ID), 0600); e != nil {
		t.Fatal(e)
	}
	var imported []projectmemory.Entry
	if e := json.Unmarshal(run("import", "--project", "destination", "--file", bundleFile), &imported); e != nil || len(imported) != 1 || imported[0].Status != "candidate" {
		t.Fatal(imported, e)
	}
	var snap projectmemory.Snapshot
	if e := json.Unmarshal(run("snapshot", "--project", "destination"), &snap); e != nil || len(snap.Entries) != 0 {
		t.Fatal(snap, e)
	}
	var p projectmemory.Project
	json.Unmarshal(run("project", "show", "destination"), &p)
	n := imported[0]
	json.Unmarshal(run("approve", n.ID, "--project", "destination", "--digest", n.Digest, "--reviewer", "destination operator", "--reason", "checked locally", "--sequence", strconv.FormatUint(p.Sequence, 10), "--review-due", due), &n)
	if n.Status != "approved" {
		t.Fatal(n)
	}
	next := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	json.Unmarshal(run("renew", n.ID, "--project", "destination", "--digest", n.Digest, "--reviewer", "destination operator", "--reason", "retested", "--review-due", next), &n)
	if n.Revision != 3 || n.ReviewDue == nil || n.ReviewDue.Format(time.RFC3339) != next {
		t.Fatal(n)
	}
	// Reimport after review must not overwrite local authority or duplicate entries.
	json.Unmarshal(run("import", "--project", "destination", "--file", bundleFile), &imported)
	if len(imported) != 1 || imported[0].Digest != n.Digest {
		t.Fatal(imported)
	}
	for _, name := range []string{"inspect", "stats", "check"} {
		var report projectmemory.Maintenance
		if e := json.Unmarshal(run(name, "--project", "destination"), &report); e != nil || report.HistoryRevisions != 3 || report.StatusCounts["approved"] != 1 {
			t.Fatal(report, e)
		}
	}
	run("archive", n.ID, "--project", "destination", "--digest", n.Digest, "--reviewer", "destination operator", "--reason", "obsolete")
	json.Unmarshal(run("snapshot", "--project", "destination"), &snap)
	if len(snap.Entries) != 0 {
		t.Fatal("archived content injected")
	}
	if a.Docker != nil {
		t.Fatal("memory maintenance requires Docker")
	}
}
