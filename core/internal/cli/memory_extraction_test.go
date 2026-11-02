package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sandx/internal/execution"
	"sandx/internal/projectmemory"
	"sandx/internal/task"
	"sandx/internal/taskmemory"
)

type memoryTransport func(*http.Request) (*http.Response, error)

func (f memoryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func memoryCLIRecord(t *testing.T, home string) task.Task {
	t.Helper()
	tt, e := task.NewExecution(home, "test-task")
	if e != nil {
		t.Fatal(e)
	}
	store, e := execution.NewFileStore(tt)
	if e != nil {
		t.Fatal(e)
	}
	unlock, e := store.Lock()
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	stages := []execution.StageProgress{}
	for _, p := range []execution.Phase{execution.AdmissionPhase, execution.PreparePhase, execution.ExecutePhase, execution.CollectPhase, execution.EvaluatePhase, execution.DeliverPhase, execution.FinalizePhase} {
		stages = append(stages, execution.StageProgress{Phase: p, State: execution.ProgressSucceeded})
	}
	now := time.Now().UTC()
	code := 13
	r := &execution.TaskRecord{SchemaVersion: execution.RecordSchema, TaskKey: tt.ID(), Attempts: []execution.AttemptRecord{{Result: execution.Result{TaskID: tt.Name, AttemptID: "attempt", Mode: execution.Headless, State: execution.AgentFailed, ExecutionState: execution.AgentFailed, AgentExitCode: &code, Stages: stages, ValidationState: "not_requested", ProductDeliveryState: "not_requested"}, StartedAt: now, UpdatedAt: now, Closed: true}}}
	if e = store.Save(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	return tt
}
func TestMemoryCLIExtractionReviewSnapshotAndRetryNoDocker(t *testing.T) {
	home := t.TempDir()
	tt := memoryCLIRecord(t, home)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("default_agent='codex'\n[memory.extract]\nmodel='test-model'\n"), 0600)
	a := &App{Home: home, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	calls := 0
	fail := false
	a.MemoryModel = taskmemory.HTTPModel{APIKey: "private-api-value", Client: &http.Client{Transport: memoryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if fail {
			return nil, errors.New("private backend failure")
		}
		b, _ := io.ReadAll(r.Body)
		if bytes.Contains(b, []byte("private-api-value")) {
			t.Fatal("credential in request body")
		}
		var request struct {
			Input string `json:"input"`
		}
		json.Unmarshal(b, &request)
		var material taskmemory.Material
		if e := json.Unmarshal([]byte(request.Input), &material); e != nil {
			t.Fatal(e)
		}
		out, _ := json.Marshal(taskmemory.Output{Explanation: "Task failed; test prerequisite is an operator declaration.", Suggestions: []string{"Check the prerequisite"}, Candidates: []taskmemory.Candidate{{Kind: "testing", Title: "Docker prerequisite", Content: material.Summary.Notes[0], Evidence: []string{"summary:" + projectmemory.Digest(material.Summary)}}}})
		body, _ := json.Marshal(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": string(out)}}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	run := func(wantErr bool, args ...string) []byte {
		t.Helper()
		out := &bytes.Buffer{}
		a.Out = out
		c := a.memoryCmd()
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		c.SetArgs(args)
		e := c.Execute()
		if wantErr != (e != nil) {
			t.Fatal(args, e)
		}
		return out.Bytes()
	}
	run(false, "project", "create", "demo")
	summary := filepath.Join(t.TempDir(), "summary.json")
	os.WriteFile(summary, []byte(`{"goal":"Fix tests","todo":["Verify integration"],"notes":["Integration tests require Docker"]}`), 0600)
	var handoff taskmemory.Handoff
	json.Unmarshal(run(false, "handoff", tt.Name, "--scope", "general", "--summary", summary), &handoff)
	if handoff.OperatorSummary == nil || handoff.OperatorSummary.Goal != "Fix tests" || handoff.State != "agent_failed" {
		t.Fatal(handoff)
	}
	args := []string{"extract", tt.Name, "--scope", "general", "--project", "demo"}
	var j taskmemory.Job
	if e := json.Unmarshal(run(false, args...), &j); e != nil || j.State != "succeeded" || len(j.Candidates) != 1 {
		t.Fatal(j, e)
	}
	run(false, args...)
	if calls != 1 {
		t.Fatal("same material called model twice", calls)
	}
	var candidates []projectmemory.Entry
	json.Unmarshal(run(false, "candidates", "--project", "demo"), &candidates)
	if len(candidates) != 1 || candidates[0].Provenance.Job != j.ID {
		t.Fatal(candidates)
	}
	v := candidates[0]
	run(false, "approve", v.ID, "--project", "demo", "--digest", v.Digest, "--reviewer", "operator", "--reason", "verified independently")
	var snap projectmemory.Snapshot
	json.Unmarshal(run(false, "snapshot", "--project", "demo"), &snap)
	if len(snap.Entries) != 1 {
		t.Fatal(snap)
	}
	// New material makes one new job. Explicit retry uses frozen material even if
	// the input summary and task record are subsequently unavailable.
	os.WriteFile(summary, []byte(`{"notes":["Check Docker daemon availability"]}`), 0600)
	fail = true
	var failed taskmemory.Job
	json.Unmarshal(run(true, append(args, "--summary", summary)...), &failed)
	if failed.State != "failed" || strings.Contains(failed.Error, "private") {
		t.Fatal(failed)
	}
	os.Remove(summary)
	fail = false
	run(false, "retry", failed.ID)
	if calls != 3 {
		t.Fatal(calls)
	}
	run(false, "job", j.ID)
	run(false, "jobs")
	run(false, "recover", failed.ID)
	if a.Docker != nil {
		t.Fatal("memory commands accessed Docker")
	}
	store, _ := execution.NewFileStore(tt)
	record, e := store.Load()
	if e != nil || record.Sequence != 1 || record.Attempts[0].Result.State != execution.AgentFailed {
		t.Fatal("memory workflow changed business record", record, e)
	}
}
