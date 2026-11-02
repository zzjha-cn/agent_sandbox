package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/completion"
	"sandx/internal/config"
	"sandx/internal/projectmemory"
	"sandx/internal/taskmemory"
)

type memoryModelFunc func(context.Context, taskmemory.Job) ([]byte, error)

func (f memoryModelFunc) Generate(c context.Context, j taskmemory.Job) ([]byte, error) {
	return f(c, j)
}
func TestHandoffProjectsSafeFactsAndUnknownAcrossAttempts(t *testing.T) {
	req, store, r := successfulRecord(t)
	r.Operations = append(r.Operations, OperationRecord{Key: "external-op", AttemptID: r.Attempts[0].Result.AttemptID, Kind: "tool", State: "unknown", TargetRef: "credential-secret-location", ObjectRef: "raw-secret-response"})
	r.Attempts[0].Closed = false
	r.Attempts[0].Result.setStage(ExecutePhase, ProgressRunning)
	r.Attempts[0].Pipeline = &PipelineFacts{InputSnapshot: "input-digest", Artifact: &ArtifactRef{Digest: "artifact-digest"}, Validation: &completion.Result{State: "failed", Evidence: []completion.Evidence{{ID: "test", Kind: "tests", State: "failed", Reason: "raw test output secret", ExitCode: new(int)}}}}
	unlock, e := store.Lock()
	if e != nil {
		t.Fatal(e)
	}
	e = store.Save(context.Background(), r)
	unlock()
	if e != nil {
		t.Fatal(e)
	}
	// Sidecar already exists at checkpoint; explicit refresh also reconstructs it
	// after a killed controller without claiming the process is dead or successful.
	h, e := taskmemory.ReadHandoff(req.Task.Home, req.Task.ID(), r.Attempts[0].Result.AttemptID)
	if e != nil || !h.Incomplete || h.Closed || h.Recovery != "reconcile_operations" || h.Operations[0].State != "unknown" || h.Validation[0].State != "failed" {
		t.Fatal(h, e)
	}
	b, _ := json.Marshal(h)
	for _, secret := range []string{req.Prompt, "credential-secret-location", "raw-secret-response", "raw test output secret"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("handoff leaked", secret)
		}
	}
	os.RemoveAll(taskmemory.TaskDir(req.Task.Home, req.Task.ID()))
	h, e = RefreshHandoff(req.Task, "")
	if e != nil || h.ArtifactDigest != "artifact-digest" {
		t.Fatal(h, e)
	}
	after, _ := store.Load()
	if after.Sequence != r.Sequence || after.Operations[0].State != "unknown" {
		t.Fatal("handoff changed business record")
	}
}
func TestHandoffFailureNeverConvertsBusinessSuccess(t *testing.T) {
	req, store := durableRequest(t)
	os.WriteFile(filepath.Join(req.Task.Home, "task-memory"), []byte("blocked derived storage"), 0600)
	result, e := NewService(&testDriver{outcome: AgentOutcome{ExitCode: new(int)}}).WithStore(store).Run(context.Background(), req)
	if e != nil || result.State != Finished {
		t.Fatal(result, e)
	}
	if _, e = RefreshHandoff(req.Task, ""); e == nil {
		t.Fatal("projection failure hidden from explicit query")
	}
	r, e := store.Load()
	if e != nil || r.Attempts[0].Result.State != Finished {
		t.Fatal(r, e)
	}
}
func TestAutomaticExtractionIndependentSuccessFailureAndOptIn(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		auto, fail, agentFail bool
	}{{"disabled", false, false, false}, {"candidate", true, false, false}, {"model failure", true, true, false}, {"failed agent", true, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			req, store := durableRequest(t)
			req.Config = config.Default()
			req.Config.DefaultAgent = "codex"
			req.Config.Memory.Mode = "managed"
			req.Config.Memory.Project = "demo"
			req.Config.Memory.Extract.Auto = tc.auto
			req.Config.Memory.Extract.Model = "test-model"
			ps := projectmemory.Store{Home: req.Task.Home}
			if _, e := ps.Create("demo"); e != nil {
				t.Fatal(e)
			}
			calls := 0
			var jobID string
			model := memoryModelFunc(func(_ context.Context, j taskmemory.Job) ([]byte, error) {
				calls++
				u, e := store.Lock()
				if e != nil {
					t.Fatal("model call retained execution lock", e)
				}
				u()
				jobID = j.ID
				if strings.Contains(func() string { b, _ := json.Marshal(j.Material); return string(b) }(), req.Prompt) {
					t.Fatal("prompt read")
				}
				if tc.fail {
					return nil, errors.New("backend raw secret")
				}
				return json.Marshal(taskmemory.Output{Explanation: "Only durable facts were available; no reusable lesson can be inferred.", Suggestions: []string{}, Candidates: []taskmemory.Candidate{}})
			})
			driver := &testDriver{outcome: AgentOutcome{ExitCode: new(int)}}
			if tc.agentFail {
				code := 13
				driver.outcome.ExitCode = &code
			}
			var stream bytes.Buffer
			result, e := NewService(driver, NewJSONLSink(&stream)).WithStore(store).WithExtractionModel(model).Run(context.Background(), req)
			if e != nil {
				t.Fatal(result, e)
			}
			want := Finished
			if tc.agentFail {
				want = AgentFailed
			}
			if result.State != want {
				t.Fatal(result)
			}
			if calls != map[bool]int{true: 1, false: 0}[tc.auto] {
				t.Fatal(calls)
			}
			r, _ := store.Load()
			if r.Attempts[0].Result.State != want || strings.Contains(stream.String(), "backend raw secret") {
				t.Fatal(r, stream.String())
			}
			if tc.auto {
				p, e := taskmemory.ReadProcessing(req.Task.Home, req.Task.ID(), result.AttemptID)
				if e != nil || tc.fail != (p.State == "failed") {
					t.Fatal(p, e)
				}
				j, e := (taskmemory.Jobs{Home: req.Task.Home}).Load(jobID)
				if e != nil || tc.fail != (j.State == "failed") {
					t.Fatal(j, e)
				}
			}
			if strings.Join(driver.steps, ",") != "inspect,admit,create,wait" {
				t.Fatal("extraction replayed business runtime", driver.steps)
			}
		})
	}
}
