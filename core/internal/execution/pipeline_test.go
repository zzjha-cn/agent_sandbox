package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"sandx/internal/completion"
	"sandx/internal/config"
	"sandx/internal/source"
	"sandx/internal/task"
)

type producingDriver struct {
	testDriver
	body    string
	creates int
}

func (d *producingDriver) Create(_ context.Context, req Request) error {
	d.creates++
	return os.WriteFile(filepath.Join(req.Task.Worktree(), "report.json"), []byte(d.body), 0600)
}
func (d *producingDriver) Resume(ctx context.Context, req Request, _ bool) error {
	return d.Create(ctx, req)
}

func generalRequest(t *testing.T) (Request, *FileStore) {
	t.Helper()
	home := t.TempDir()
	tsk, err := task.NewExecution(home, "report")
	if err != nil {
		t.Fatal(err)
	}
	spec := TaskSpec{SchemaVersion: TaskInputSchema, TaskID: "report", Prompt: "private prompt", Outputs: &OutputSelection{Include: []string{"report.json"}}, Criteria: []completion.Criterion{{ID: "file", Kind: "files", Ref: "report"}}}
	spec.Agent.Provider = "codex"
	p := &PipelinePlan{Spec: spec, Policy: DefaultPipelinePolicy()}
	p.Policy.Criteria = map[string]completion.Rule{"report": {Kind: "files", Version: "1", Required: []string{"report.json"}, Format: map[string]string{"report.json": "json"}, AllowedChanges: []string{"report.json"}}}
	req := Request{Task: tsk, TaskID: spec.TaskID, Mode: Headless, Prompt: spec.Prompt, Config: config.Default(), Pipeline: p}
	req.Config.DefaultAgent = "codex"
	store, err := NewFileStore(tsk)
	if err != nil {
		t.Fatal(err)
	}
	return req, store
}

func TestGeneralPipelineCollectsAndChecksBusinessCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, body, state string
		wantErr           bool
	}{{"pass", "{\"ok\":true}", "passed", false}, {"agent-zero-invalid-output", "broken", "failed", true}} {
		t.Run(tc.name, func(t *testing.T) {
			req, store := generalRequest(t)
			code := 0
			driver := &producingDriver{body: tc.body, testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
			result, err := NewService(driver).WithStore(store).Run(context.Background(), req)
			if (err != nil) != tc.wantErr || result.ExecutionState != Finished || result.ValidationState != tc.state {
				t.Fatalf("%+v %v", result, err)
			}
			record, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			facts := record.Attempts[0].Pipeline
			if facts == nil || facts.Prepared == nil || facts.Artifact == nil || facts.Validation == nil || facts.Validation.ArtifactDigest != facts.Artifact.Digest {
				t.Fatalf("missing evidence: %+v", facts)
			}
			if err := os.WriteFile(filepath.Join(req.Task.Worktree(), "report.json"), []byte("later change"), 0600); err != nil {
				t.Fatal(err)
			}
			svc := NewService(driver)
			if err := svc.verifyArtifact(context.Background(), req, *facts.Artifact); err != nil {
				t.Fatal("working copy mutated retained snapshot:", err)
			}
			if _, err := os.Stat(filepath.Join(req.Task.Worktree(), ".git")); !os.IsNotExist(err) {
				t.Fatal("non-Git task initialized Git")
			}
			unified, _ := NewFileStore(req.Task)
			if recovered, err := unified.Load(); err != nil || recovered.SchemaVersion != RecordSchema {
				t.Fatalf("unified reader lost pipeline facts: %v", err)
			}
		})
	}
}

func TestGeneralPipelineRejectsSourceChangeAndLeavesWorkspace(t *testing.T) {
	req, store := generalRequest(t)
	input := t.TempDir()
	if err := os.WriteFile(filepath.Join(input, "data.txt"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Pipeline.Spec.Source = &source.Selection{Kind: "files", Ref: "input"}
	req.Pipeline.Policy.Sources = map[string]source.Grant{"input": {Kind: "files", Path: input, Include: []string{"."}}}
	code := 0
	driver := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	if _, err := NewService(driver).WithStore(store).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "data.txt"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	driver.exists = true
	result, err := NewService(driver).WithStore(store).Run(context.Background(), req)
	if err == nil || result.State != Rejected || driver.creates != 1 {
		t.Fatalf("%+v %v %d", result, err, driver.creates)
	}
	data, err := os.ReadFile(filepath.Join(req.Task.Worktree(), "data.txt"))
	if err != nil || string(data) != "one" {
		t.Fatalf("source change overwrote execution copy: %q %v", data, err)
	}
}

func withDirectorySink(t *testing.T, req *Request) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "published")
	req.Pipeline.Spec.Sink = &SinkSelection{Kind: "local_directory", TargetRef: "reports", DeliveryKey: "report-001"}
	req.Pipeline.Policy.Sinks = map[string]SinkGrant{"reports": {Kind: "local_directory", Path: target}}
	return target
}

func TestGeneralCompletedDeliveryCannotBeReplayedAfterTargetChanges(t *testing.T) {
	req, store := generalRequest(t)
	target := withDirectorySink(t, &req)
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "report.json"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "deliver", ""); err == nil {
		t.Fatal("completed delivery was replayed")
	}
	body, err := os.ReadFile(filepath.Join(target, "report.json"))
	if err != nil || string(body) != "changed" || d.creates != 1 {
		t.Fatalf("target overwritten or agent replayed: %q %v creates=%d", body, err, d.creates)
	}
}

func TestGeneralCurrentExclusionsBlockRetainedOutput(t *testing.T) {
	req, store := generalRequest(t)
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	req.Pipeline.Policy.Exclude = []string{"report.json"}
	if err := NewService(d).verifyArtifact(context.Background(), req, *record.Attempts[0].Pipeline.Artifact); err == nil {
		t.Fatal("new exclusion did not block retained output")
	}
}

func TestGeneralContinuationRejectsSourceReauthorization(t *testing.T) {
	req, store := generalRequest(t)
	input := t.TempDir()
	if err := os.WriteFile(filepath.Join(input, "data.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Pipeline.Spec.Source = &source.Selection{Kind: "files", Ref: "input"}
	req.Pipeline.Policy.Sources = map[string]source.Grant{"input": {Kind: "files", Path: input, Include: []string{"."}}}
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	g := req.Pipeline.Policy.Sources["input"]
	g.Path = t.TempDir()
	req.Pipeline.Policy.Sources["input"] = g
	for _, action := range []string{"verify", "approve", "deliver"} {
		if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, action, "operator"); err == nil {
			t.Fatalf("%s accepted rebound source authority", action)
		}
	}
	if d.creates != 1 {
		t.Fatal("continuation replayed agent")
	}
}

func TestGeneralAuthorizationChangeDuringPreparationStopsAgent(t *testing.T) {
	req, store := generalRequest(t)
	input, replacement := t.TempDir(), t.TempDir()
	req.Pipeline.Spec.Source = &source.Selection{Kind: "files", Ref: "input"}
	req.Pipeline.Policy.Sources = map[string]source.Grant{"input": {Kind: "files", Path: input, Include: []string{"."}}}
	policy := req.Pipeline.Policy
	calls := 0
	req.Pipeline.Reauthorize = func() (PipelinePolicy, error) {
		calls++
		if calls == 3 {
			grant := policy.Sources["input"]
			grant.Path = replacement
			policy.Sources["input"] = grant
		}
		return policy, nil
	}
	d := &producingDriver{body: "{}"}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err == nil || d.creates != 0 || calls != 3 {
		t.Fatalf("agent started after source authorization changed: err=%v creates=%d checks=%d", err, d.creates, calls)
	}
}

func TestGeneralBoundaryProtectsConfiguredCredentialPaths(t *testing.T) {
	for _, name := range []string{"relative-key", "~/sbx-boundary-test-key"} {
		t.Run(name, func(t *testing.T) {
			req, _ := generalRequest(t)
			req.Config.Agents["codex"] = config.Agent{APIKeyFile: name}
			path := name
			if name[0] == '~' {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(home, name[2:])
			}
			req.Pipeline.Spec.Source = &source.Selection{Kind: "files", Ref: "input"}
			req.Pipeline.Policy.Sources = map[string]source.Grant{"input": {Kind: "files", Path: path}}
			if err := pipelineBoundary(req); err == nil {
				t.Fatal("credential path admitted as input")
			}
		})
	}
}

func TestGeneralReviewPersistsReleasesLocksAndContinuesDelivery(t *testing.T) {
	req, store := generalRequest(t)
	target := withDirectorySink(t, &req)
	req.Pipeline.Spec.Criteria = append(req.Pipeline.Spec.Criteria, completion.Criterion{ID: "human", Kind: "manual", Ref: "human"})
	req.Pipeline.Policy.Criteria["human"] = completion.Rule{Kind: "manual", Version: "1"}
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	r, err := NewService(d).WithStore(store).Run(context.Background(), req)
	if err == nil || r.ValidationState != "needs_review" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("published before review")
	}
	record, err := store.Load()
	if err != nil || record.Recovery() != "continue_verification" {
		t.Fatalf("%+v %v", record, err)
	}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err == nil {
		t.Fatal("resubmitted prompt while awaiting review")
	}
	r, err = NewService(d).WithStore(store).Continue(context.Background(), req, "approve", "operator-a")
	if err != nil || r.ProductDeliveryState != "delivered" || d.creates != 1 {
		t.Fatalf("%+v %v creates=%d", r, err, d.creates)
	}
	record, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := record.Attempts[0]
	if len(record.Attempts) != 1 || a.Pipeline.Review == nil || a.Pipeline.Review.Reviewer != "operator-a" || a.ErrorKind != "" || len(record.Operations) != 1 || record.Operations[0].State != "completed" {
		t.Fatalf("bad continuation evidence: %+v", record)
	}
}

func TestGeneralDeliveryRetryDoesNotRunAgent(t *testing.T) {
	req, store := generalRequest(t)
	target := withDirectorySink(t, &req)
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "old"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	r, err := NewService(d).WithStore(store).Run(context.Background(), req)
	if err == nil || r.ExecutionState != Finished || r.ValidationState != "passed" || r.ProductDeliveryState != "failed" {
		t.Fatalf("%+v %v", r, err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	r, err = NewService(d).WithStore(store).Continue(context.Background(), req, "deliver", "")
	if err != nil || r.ProductDeliveryState != "delivered" || d.creates != 1 {
		t.Fatalf("%+v %v creates=%d", r, err, d.creates)
	}
}

type lostDeliveryReceiptStore struct{ RecordStore }

func (s lostDeliveryReceiptStore) Save(ctx context.Context, r *TaskRecord) error {
	for _, op := range r.Operations {
		if op.Kind == "delivery" && op.State == "completed" {
			return errors.New("simulated crash before durable receipt")
		}
	}
	return s.RecordStore.Save(ctx, r)
}

func TestGeneralLostReceiptReconcilesSameTargetWithoutAgent(t *testing.T) {
	req, store := generalRequest(t)
	target := withDirectorySink(t, &req)
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	if _, err := NewService(d).WithStore(lostDeliveryReceiptStore{store}).Run(context.Background(), req); err == nil {
		t.Fatal("lost receipt returned success")
	}
	if _, err := os.Stat(filepath.Join(target, "report.json")); err != nil {
		t.Fatal("fixture did not commit target")
	}
	record, err := store.Load()
	if err != nil || record.Recovery() != "reconcile_operations" {
		t.Fatalf("%+v %v", record, err)
	}
	r, err := NewService(d).WithStore(store).Continue(context.Background(), req, "deliver", "")
	if err != nil || r.ProductDeliveryState != "delivered" || d.creates != 1 {
		t.Fatalf("%+v %v creates=%d", r, err, d.creates)
	}
}

func TestGeneralPatchAppliesAndPruningPreservesDeliveredTarget(t *testing.T) {
	req, store := generalRequest(t)
	target := withDirectorySink(t, &req)
	req.Pipeline.Spec.Outputs.Patch = true
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	r, err := NewService(d).WithStore(store).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(target, "changes.patch")
	apply := exec.Command("git", "-C", t.TempDir(), "apply", "--check", patch)
	if output, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("patch does not apply: %s %v", output, err)
	}
	if err := PruneArtifacts(context.Background(), req.Task, r.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "report.json")); err != nil {
		t.Fatal("pruning removed published content")
	}
	record, err := store.Load()
	if err != nil || record.Attempts[0].Pipeline.Artifact.Retention != "pruned" {
		t.Fatalf("%+v %v", record, err)
	}
	if _, err := os.Stat(filepath.Join(pipelineRoot(req), "inputs", record.Attempts[0].Pipeline.InputSnapshot)); err != nil {
		t.Fatal("pruning removed source baseline")
	}
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "deliver", ""); err == nil {
		t.Fatal("delivered using a pruned snapshot")
	}
}

func TestGeneralReviewRejectsChangedRulesAndReverificationDoesNotRunAgent(t *testing.T) {
	req, store := generalRequest(t)
	req.Pipeline.Spec.Criteria = append(req.Pipeline.Spec.Criteria, completion.Criterion{ID: "human", Kind: "manual", Ref: "human"})
	req.Pipeline.Policy.Criteria["human"] = completion.Rule{Kind: "manual", Version: "1"}
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	r, err := NewService(d).WithStore(store).Run(context.Background(), req)
	if err == nil || r.State != ReviewPending {
		t.Fatalf("%+v %v", r, err)
	}
	if err := PruneArtifacts(context.Background(), req.Task, r.AttemptID); err == nil {
		t.Fatal("pruned pending review evidence")
	}
	rule := req.Pipeline.Policy.Criteria["human"]
	rule.Version = "2"
	req.Pipeline.Policy.Criteria["human"] = rule
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "approve", "operator"); err == nil {
		t.Fatal("accepted stale review evidence")
	}
	r, err = NewService(d).WithStore(store).Continue(context.Background(), req, "verify", "")
	if err == nil || r.State != ReviewPending || d.creates != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "approve", "operator"); err != nil {
		t.Fatal(err)
	}
}

func TestGeneralLostReceiptRejectsTargetRebinding(t *testing.T) {
	req, store := generalRequest(t)
	withDirectorySink(t, &req)
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	if _, err := NewService(d).WithStore(lostDeliveryReceiptStore{store}).Run(context.Background(), req); err == nil {
		t.Fatal("expected lost receipt")
	}
	g := req.Pipeline.Policy.Sinks["reports"]
	g.Path = filepath.Join(t.TempDir(), "different")
	req.Pipeline.Policy.Sinks["reports"] = g
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "deliver", ""); err == nil {
		t.Fatal("rebound logical delivery to a different physical target")
	}
	if _, err := os.Stat(g.Path); !os.IsNotExist(err) {
		t.Fatal("wrote rebound target")
	}
}
