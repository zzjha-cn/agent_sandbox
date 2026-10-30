package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"sandx/internal/completion"
	"sandx/internal/config"
	"sandx/internal/task"
)

func crashPipelineRequest(t *testing.T, home string) (Request, *FileStore) {
	t.Helper()
	tsk, err := task.NewExecution(home, "report")
	if err != nil {
		t.Fatal(err)
	}
	spec := TaskSpec{SchemaVersion: TaskInputSchema, TaskID: "report", Prompt: "never replay", Outputs: &OutputSelection{Include: []string{"report.json"}}, Criteria: []completion.Criterion{{ID: "file", Kind: "files", Ref: "check"}}, Sink: &SinkSelection{Kind: "local_directory", TargetRef: "target", DeliveryKey: "one"}}
	spec.Agent.Provider = "codex"
	policy := DefaultPipelinePolicy()
	policy.Criteria = map[string]completion.Rule{"check": {Kind: "files", Version: "1", Required: []string{"report.json"}, Format: map[string]string{"report.json": "json"}}}
	policy.Sinks = map[string]SinkGrant{"target": {Kind: "local_directory", Path: filepath.Join(home, "published")}}
	req := Request{Task: tsk, TaskID: "report", Mode: Headless, Prompt: spec.Prompt, Config: config.Default(), Pipeline: &PipelinePlan{Spec: spec, Policy: policy}}
	req.Config.DefaultAgent = "codex"
	store, err := NewFileStore(tsk)
	if err != nil {
		t.Fatal(err)
	}
	return req, store
}

type pipelineCrashStore struct {
	RecordStore
	home, mode string
}

func (s pipelineCrashStore) pause() {
	if err := os.WriteFile(filepath.Join(s.home, "ready"), []byte("ready"), 0600); err != nil {
		panic(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}
func (s pipelineCrashStore) Save(ctx context.Context, r *TaskRecord) error {
	last := r.Attempts[len(r.Attempts)-1]
	if s.mode == "collect" && stageState(last.Result, CollectPhase) == ProgressSucceeded && stageState(last.Result, EvaluatePhase) == ProgressSkipped {
		s.pause()
	}
	if s.mode == "receipt" {
		for _, op := range r.Operations {
			if op.Kind == "delivery" && op.State == "completed" {
				s.pause()
			}
		}
	}
	if err := s.RecordStore.Save(ctx, r); err != nil {
		return err
	}
	if s.mode == "evaluate" && stageState(last.Result, EvaluatePhase) == ProgressRunning {
		s.pause()
	}
	if s.mode == "deliver" && stageState(last.Result, DeliverPhase) == ProgressRunning {
		s.pause()
	}
	return nil
}

type crashProducer struct {
	producingDriver
	home string
}

func (d *crashProducer) Create(ctx context.Context, req Request) error {
	f, err := os.OpenFile(filepath.Join(d.home, "effects"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString("agent\n")
	if err := f.Close(); err != nil {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	return d.producingDriver.Create(ctx, req)
}

func TestGeneralPipelineCrashHelper(t *testing.T) {
	mode := os.Getenv("SBX_G234_CRASH")
	if mode == "" {
		return
	}
	home := os.Getenv("SBX_G234_HOME")
	req, store := crashPipelineRequest(t, home)
	code := 0
	driver := &crashProducer{home: home, producingDriver: producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}}
	if _, err := NewService(driver).WithStore(pipelineCrashStore{store, home, mode}).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestGeneralProcessDeathResumesCompletionWithoutAgent(t *testing.T) {
	for _, mode := range []string{"collect", "evaluate", "deliver", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			req, store := crashPipelineRequest(t, home)
			child := exec.Command(os.Args[0], "-test.run=^TestGeneralPipelineCrashHelper$", "-test.timeout=30s")
			child.Env = append(os.Environ(), "SBX_G234_CRASH="+mode, "SBX_G234_HOME="+home)
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { child.Process.Kill(); child.Wait() })
			ready := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(home, "ready")); err == nil {
					ready = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !ready {
				t.Fatal("child did not reach requested durable boundary")
			}
			before, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			attemptID := before.Attempts[0].Result.AttemptID
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			child.Wait()
			d := &producingDriver{body: "unexpected"}
			action := "verify"
			if mode == "deliver" || mode == "receipt" {
				action = "deliver"
			}
			r, err := NewService(d).WithStore(store).Continue(context.Background(), req, action, "")
			if err != nil || r.ProductDeliveryState != "delivered" || d.creates != 0 {
				t.Fatalf("%+v %v creates=%d", r, err, d.creates)
			}
			after, err := store.Load()
			if err != nil || len(after.Attempts) != 1 || after.Attempts[0].Result.AttemptID != attemptID || !after.Attempts[0].Closed {
				t.Fatalf("%+v %v", after, err)
			}
			effects, err := os.ReadFile(filepath.Join(home, "effects"))
			if err != nil || string(effects) != "agent\n" {
				t.Fatalf("replayed agent: %q %v", effects, err)
			}
			if mode == "collect" && len(after.Attempts[0].Pipeline.Quarantines) != 1 {
				t.Fatal("uncommitted snapshot was not quarantined")
			}
		})
	}
}
