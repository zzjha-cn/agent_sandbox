package taskmemory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sandx/internal/projectmemory"
)

type modelFunc func(context.Context, Job) ([]byte, error)

func (f modelFunc) Generate(c context.Context, j Job) ([]byte, error) { return f(c, j) }
func fixture(t *testing.T) (Jobs, Job) {
	t.Helper()
	home := t.TempDir()
	if _, e := (projectmemory.Store{Home: home}).Create("demo"); e != nil {
		t.Fatal(e)
	}
	h := SealHandoff(Handoff{Schema: HandoffSchema, TaskKey: "repo.task", Attempt: "attempt", Sequence: 1, Closed: true, State: "agent_failed", ValidationState: "not_requested", Operations: []Operation{{ID: "op1", Kind: "tool", State: "unknown"}}})
	s := Jobs{Home: home}
	j, e := s.Create("demo", "codex", "test-model", Material{Handoff: h, Summary: Summary{Notes: []string{"Integration tests require Docker."}}}, Limits{TimeoutSeconds: 1, MaxOutputTokens: 512, MaxCalls: 2})
	if e != nil {
		t.Fatal(e)
	}
	return s, j
}
func goodOutput(j Job) []byte {
	b, _ := json.Marshal(Output{Explanation: "Operator reports a test prerequisite; task failed and operation remains unknown.", Suggestions: []string{"Reconcile the unknown operation before any write."}, Candidates: []Candidate{{Kind: "testing", Title: "Docker dependency", Content: "Integration tests require Docker; verify availability first.", Evidence: []string{"summary:" + projectmemory.Digest(j.Material.Summary)}}}})
	return b
}
func TestExtractionFrozenIdempotentReviewedAndCrashPublication(t *testing.T) {
	s, j := fixture(t)
	calls := 0
	model := modelFunc(func(_ context.Context, v Job) ([]byte, error) { calls++; return goodOutput(v), nil })
	got, e := s.Run(context.Background(), j.ID, model, false)
	if e != nil || got.State != "succeeded" || len(got.Candidates) != 1 || got.UpdatedAt.IsZero() {
		t.Fatal(got, e)
	}
	p, e := (projectmemory.Store{Home: s.Home}).Load("demo")
	if e != nil || len(p.History) != 1 || p.History[0].Status != "candidate" || p.History[0].Provenance.Job != j.ID {
		t.Fatal(p, e)
	}
	v := p.History[0]
	if _, e = (projectmemory.Store{Home: s.Home}).Review("demo", v.ID, v.Digest, "approved", "operator", "verified independently"); e != nil {
		t.Fatal(e)
	}
	// Crash after publication, before succeeded save. Frozen output can finish
	// without a second API request or resetting an operator's later review.
	got.State = "running"
	got.Candidates = nil
	if e = s.save(&got); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Run(context.Background(), j.ID, model, false); e == nil {
		t.Fatal("implicit interruption retry")
	}
	got, e = s.Run(context.Background(), j.ID, model, true)
	if e != nil || calls != 1 {
		t.Fatal(got, calls, e)
	}
	p, _ = (projectmemory.Store{Home: s.Home}).Load("demo")
	if len(p.History) != 2 || p.Latest()[0].Status != "approved" {
		t.Fatal(p)
	}
	again, e := s.Create(j.Project, j.Provider, j.Model, j.Material, j.Limits)
	if e != nil || again.ID != j.ID || again.State != "succeeded" {
		t.Fatal(again, e)
	}
	snap, _ := (projectmemory.Store{Home: s.Home}).Select("demo", projectmemory.Selection{})
	if len(snap.Entries) != 1 {
		t.Fatal(snap)
	}
	// Changed material makes a new job; retry above used only original bytes.
	changed := j.Material
	changed.Summary.Notes = []string{"A different operator summary."}
	newJob, e := s.Create("demo", j.Provider, j.Model, changed, j.Limits)
	if e != nil || newJob.ID == j.ID {
		t.Fatal(newJob, e)
	}
}
func TestExtractionStrictOutputEvidenceSecretsAndBudget(t *testing.T) {
	for _, bad := range []string{
		`{}`, `{"explanation":"x","suggestions":[],"candidates":[],"execution_state":"finished"}`,
		`{"explanation":"x","suggestions":[],"candidates":[{"kind":"testing","title":"x","content":"x","evidence":["made up"]}]}`,
		`{"explanation":"Bearer private-token-1234","suggestions":[],"candidates":[]}`,
		`{"explanation":"x","suggestions":[],"candidates":[{"kind":"testing","title":"x","content":"x","evidence":[],"status":"approved"}]}`,
	} {
		t.Run(bad[:min(15, len(bad))], func(t *testing.T) {
			s, j := fixture(t)
			model := modelFunc(func(context.Context, Job) ([]byte, error) { return []byte(bad), nil })
			got, e := s.Run(context.Background(), j.ID, model, false)
			if e == nil || got.State != "failed" {
				t.Fatal(got, e)
			}
			p, _ := (projectmemory.Store{Home: s.Home}).Load("demo")
			if len(p.History) != 0 {
				t.Fatal(p)
			}
			if _, e = s.Run(context.Background(), j.ID, model, false); e == nil {
				t.Fatal("automatic retry")
			}
			got, _ = s.Run(context.Background(), j.ID, model, true)
			got, e = s.Run(context.Background(), j.ID, model, true)
			if e == nil || got.Calls != 2 || got.Error != "call_budget_exhausted" {
				t.Fatal(got, e)
			}
		})
	}
	s, j := fixture(t)
	j.Material.Summary.Notes = []string{"api_key=some-secret-value-123"}
	if _, e := s.Create(j.Project, j.Provider, j.Model, j.Material, j.Limits); e == nil {
		t.Fatal("credential accepted")
	}
	if _, e := DecodeSummary([]byte(`{"goal":"x","raw_log":"secret"}`)); e == nil {
		t.Fatal("raw log accepted")
	}
}
func TestExtractionConcurrencyAndTimeout(t *testing.T) {
	s, j := fixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	model := modelFunc(func(_ context.Context, j Job) ([]byte, error) { close(entered); <-release; return goodOutput(j), nil })
	go func() { _, e := s.Run(context.Background(), j.ID, model, false); done <- e }()
	<-entered
	if _, e := s.Run(context.Background(), j.ID, model, true); e == nil {
		t.Fatal("concurrent model call")
	}
	if _, e := s.Recover(j.ID); e == nil {
		t.Fatal("recovered live worker")
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	s, j = fixture(t)
	start := time.Now()
	got, e := s.Run(context.Background(), j.ID, modelFunc(func(c context.Context, _ Job) ([]byte, error) { <-c.Done(); return nil, c.Err() }), false)
	if e == nil || got.State != "failed" || time.Since(start) > 3*time.Second {
		t.Fatal(got, e)
	}
}
func TestExtractionWorkerProcess(t *testing.T) {
	if os.Getenv("SBX_MEMORY_TEST_WORKER") != "1" {
		return
	}
	s := Jobs{Home: os.Getenv("SBX_MEMORY_TEST_HOME")}
	_, _ = s.Run(context.Background(), os.Getenv("SBX_MEMORY_TEST_JOB"), modelFunc(func(context.Context, Job) ([]byte, error) { fmt.Println("request-started"); select {} }), false)
	os.Exit(9)
}
func TestKilledExtractionWorkerRecoveryWithoutBusinessReplay(t *testing.T) {
	s, j := fixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestExtractionWorkerProcess$")
	cmd.Env = append(os.Environ(), "SBX_MEMORY_TEST_WORKER=1", "SBX_MEMORY_TEST_HOME="+s.Home, "SBX_MEMORY_TEST_JOB="+j.ID)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() || reader.Text() != "request-started" {
		t.Fatal("worker did not reach call")
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	recovered, e := s.Recover(j.ID)
	if e != nil || recovered.State != "interrupted" || recovered.Calls != 1 {
		t.Fatal(recovered, e)
	}
	var calls atomic.Int32
	got, e := s.Run(context.Background(), j.ID, modelFunc(func(_ context.Context, v Job) ([]byte, error) {
		calls.Add(1)
		if projectmemory.Digest(v.Material) != j.MaterialDigest {
			return nil, errors.New("material changed")
		}
		return goodOutput(v), nil
	}), true)
	if e != nil || got.State != "succeeded" || got.Calls != 2 || calls.Load() != 1 {
		t.Fatal(got, e)
	}
}
func TestJobTamperAndSymlinkRejected(t *testing.T) {
	s, j := fixture(t)
	name, _ := s.path(j.ID)
	j.Material.Summary.Goal = "tampered"
	if e := save(name, j); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Load(j.ID); e == nil {
		t.Fatal("material tamper accepted")
	}
	os.Remove(name)
	target := filepath.Join(t.TempDir(), "elsewhere")
	os.WriteFile(target, []byte(`{}`), 0600)
	os.Symlink(target, name)
	if _, e := s.Load(j.ID); e == nil {
		t.Fatal("symlink accepted")
	}
	if _, e := s.Load("../escape"); e == nil {
		t.Fatal("traversal accepted")
	}
	if strings.Contains(j.Error, "Bearer") {
		t.Fatal("raw backend error stored")
	}
}

func TestSafeSummaryStrictJSONAndHandoffRetention(t *testing.T) {
	for _, b := range []string{`null`, `{"goal":"a","goal":"b"}`, `{"notes":["Bearer secret-value-1234"]}`, `{"goal":"x"} {"goal":"y"}`} {
		if _, e := DecodeSummary([]byte(b)); e == nil {
			t.Fatal("invalid summary accepted", b)
		}
	}
	s, j := fixture(t)
	key, attempt := j.Material.Handoff.TaskKey, j.Material.Handoff.Attempt
	notes := Summary{Goal: "resume safely", Todo: []string{"verify tests"}}
	if e := SaveSummary(s.Home, key, attempt, notes); e != nil {
		t.Fatal(e)
	}
	if e := SaveHandoff(s.Home, j.Material.Handoff); e != nil {
		t.Fatal(e)
	}
	h, e := ReadHandoff(s.Home, key, attempt)
	if e != nil || h.OperatorSummary == nil || h.OperatorSummary.Goal != notes.Goal {
		t.Fatal(h, e)
	}
	updated := j.Material.Handoff
	updated.Sequence++
	if e = SaveHandoff(s.Home, updated); e != nil {
		t.Fatal(e)
	}
	h, e = ReadHandoff(s.Home, key, attempt)
	if e != nil || h.Sequence != updated.Sequence || h.OperatorSummary.Todo[0] != "verify tests" {
		t.Fatal(h, e)
	}
}

func TestSummaryCanonicalSizeRoundTrip(t *testing.T) {
	s, j := fixture(t)
	notes := Summary{Notes: []string{strings.Repeat("x", (16<<10)-100)}}
	if e := SaveSummary(s.Home, j.Material.Handoff.TaskKey, j.Material.Handoff.Attempt, notes); e != nil {
		t.Fatal(e)
	}
	h, e := WithSummary(s.Home, j.Material.Handoff)
	if e != nil || h.OperatorSummary.Notes[0] != notes.Notes[0] {
		t.Fatal("near-limit notes unreadable", e)
	}
	// Go canonical JSON escapes '<'; validate the stored representation too.
	b := []byte(`{"notes":["` + strings.Repeat("<", 3000) + `"]}`)
	if _, e = DecodeSummary(b); e == nil {
		t.Fatal("oversized canonical notes accepted")
	}
	if _, e = DecodeSummary([]byte(`{"notes":["\u0073k-abcdefghijklmno"]}`)); e == nil {
		t.Fatal("escaped credential accepted")
	}
}
