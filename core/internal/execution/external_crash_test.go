package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"sandx/internal/config"
	"sandx/internal/task"
)

type externalCrashStore struct {
	RecordStore
	home, mode string
}

func (s externalCrashStore) Save(ctx context.Context, r *TaskRecord) error {
	pause := func() { (pipelineCrashStore{home: s.home}).pause() }
	for _, op := range r.Operations {
		if op.External != nil && op.State == "completed" && s.mode == "before_receipt_save" {
			pause()
		}
	}
	if err := s.RecordStore.Save(ctx, r); err != nil {
		return err
	}
	for _, op := range r.Operations {
		if op.External != nil && (op.State == "intent" && s.mode == "after_intent" || op.State == "completed" && s.mode == "after_receipt_save") {
			pause()
		}
	}
	return nil
}
func TestExternalCrashHelper(t *testing.T) {
	home := os.Getenv("SBX_G6_CRASH_HOME")
	if home == "" {
		return
	}
	var plan PipelinePlan
	b, err := os.ReadFile(filepath.Join(home, "test-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	tsk, err := task.NewExecution(home, plan.Spec.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Task: tsk, TaskID: plan.Spec.TaskID, Mode: Headless, Prompt: plan.Spec.Prompt, Config: config.Default(), Pipeline: &plan}
	req.Config.DefaultAgent = "codex"
	store, err := NewGeneralFileStore(tsk)
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	d := &crashProducer{home: home, producingDriver: producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}}
	if _, err = NewService(d).WithStore(externalCrashStore{store, home, os.Getenv("SBX_G6_CRASH_MODE")}).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}
func TestExternalProcessDeathAtJournalBoundaries(t *testing.T) {
	for _, mode := range []string{"after_intent", "before_receipt_save", "after_receipt_save"} {
		t.Run(mode, func(t *testing.T) {
			server := &recordServer{}
			req, store, d := externalRequest(t, server)
			b, err := json.Marshal(struct {
				Spec   TaskSpec
				Policy PipelinePolicy
			}{req.Pipeline.Spec, req.Pipeline.Policy})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(req.Task.Home, "test-request.json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestExternalCrashHelper$", "-test.timeout=30s")
			child.Env = append(os.Environ(), "SBX_G6_CRASH_HOME="+req.Task.Home, "SBX_G6_CRASH_MODE="+mode)
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { child.Process.Kill(); child.Wait() })
			ready := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(req.Task.Home, "ready")); err == nil {
					ready = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !ready {
				t.Fatal("child did not reach durable journal boundary")
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			child.Wait()
			result, err := NewService(d).WithStore(store).Continue(context.Background(), req, "reconcile", "")
			if mode == "after_intent" {
				if err == nil || result.State != OutcomeUnknown {
					t.Fatalf("%+v %v", result, err)
				}
				NewService(d).WithStore(store).Continue(context.Background(), req, "apply", "")
			} else if err != nil || result.ValidationState != "passed" {
				t.Fatalf("%+v %v", result, err)
			}
			effects, err := os.ReadFile(filepath.Join(req.Task.Home, "effects"))
			if err != nil || string(effects) != "agent\n" || d.creates != 0 {
				t.Fatalf("agent replay %q %v", effects, err)
			}
			server.Lock()
			writes := server.writes
			server.Unlock()
			want := 1
			if mode == "after_intent" {
				want = 0
			}
			if writes != want {
				t.Fatalf("writes=%d want=%d", writes, want)
			}
			record, err := store.Load()
			if err != nil || len(record.Attempts) != 1 {
				t.Fatalf("attempt changed %v", err)
			}
		})
	}
}
