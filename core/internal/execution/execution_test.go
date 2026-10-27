package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"sandx/internal/task"
	"sandx/internal/workspace"
)

type testDriver struct {
	exists, running bool
	code            *int
	outcome         AgentOutcome
	err             error
	steps           []string
}

type failedSink struct{ err error }

func (s failedSink) Emit(context.Context, Event) error { return s.err }

func (d *testDriver) Inspect(context.Context, Request) (bool, bool, error) {
	d.steps = append(d.steps, "inspect")
	return d.exists, d.running, nil
}
func (d *testDriver) Admit(context.Context, Request, bool, bool) error {
	d.steps = append(d.steps, "admit")
	return nil
}
func (d *testDriver) Create(context.Context, Request) error {
	d.steps = append(d.steps, "create")
	if d.err != nil {
		return &ExecutionError{Kind: EnvironmentFailed, Stage: "creating", Cause: d.err}
	}
	return d.err
}
func (d *testDriver) Resume(context.Context, Request, bool) error {
	d.steps = append(d.steps, "resume")
	return d.err
}
func (d *testDriver) Wait(context.Context, Request) (AgentOutcome, error) {
	d.steps = append(d.steps, "wait")
	return d.outcome, d.err
}

func testRequest(prompt string) Request {
	return Request{Task: task.Task{Name: "test", WS: workspace.Workspace{ID: "ws-test"}}, Mode: Headless, Prompt: prompt}
}

func TestServiceRunHeadlessEmitsJSONLResultAndSafeEvents(t *testing.T) {
	var output bytes.Buffer
	code := 0
	driver := &testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}
	secretPrompt := "prompt-secret-do-not-log"
	result, err := NewService(driver, NewJSONLSink(&output)).Run(context.Background(), testRequest(secretPrompt))
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentExitCode == nil || *result.AgentExitCode != 0 || result.ContainerState != string(ContainerStopped) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.ArtifactState != ArtifactNotProduced || result.CleanupState != CleanupNotNeeded {
		t.Fatalf("unexpected lifecycle dimensions: %+v", result)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("want admitted/start/finish/agent-started/exited/result events, got %d: %s", len(lines), output.String())
	}
	var events []Event
	for _, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", line, err)
		}
		if event.TaskID != result.TaskID || event.AttemptID != result.AttemptID || event.Time.IsZero() {
			t.Errorf("missing event identity/time: %+v", event)
		}
		events = append(events, event)
	}
	last := events[len(events)-1]
	if last.Type != EventCompleted || last.Result == nil || last.Result.AgentExitCode == nil || *last.Result.AgentExitCode != 0 {
		t.Fatalf("final event must carry structured result: %+v", last)
	}
	if strings.Contains(output.String(), secretPrompt) {
		t.Fatal("prompt leaked into machine event stream")
	}
	if strings.Join(driver.steps, ",") != "inspect,admit,create,wait" {
		t.Fatalf("unexpected lifecycle: %v", driver.steps)
	}
}

func TestServiceHeadlessNonzeroExitIsAgentFailure(t *testing.T) {
	code := 17
	driver := &testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}
	result, err := NewService(driver).Run(context.Background(), testRequest("safe"))
	if err != nil {
		t.Fatal(err)
	}
	if result.State != AgentFailed || result.AgentExitCode == nil || *result.AgentExitCode != 17 {
		t.Fatalf("wrong agent outcome: %+v", result)
	}
}

func TestServiceAttemptIDsAreUnique(t *testing.T) {
	first, err := NewService(&testDriver{outcome: AgentOutcome{ExitCode: new(int)}}).Run(context.Background(), testRequest("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewService(&testDriver{outcome: AgentOutcome{ExitCode: new(int)}}).Run(context.Background(), testRequest("two"))
	if err != nil {
		t.Fatal(err)
	}
	if first.AttemptID == "" || first.AttemptID == second.AttemptID {
		t.Fatalf("attempt IDs must be nonempty and unique: %q %q", first.AttemptID, second.AttemptID)
	}
}

func TestServiceFailureEventDoesNotExposeErrorText(t *testing.T) {
	var output bytes.Buffer
	driver := &testDriver{err: errors.New("credential=secret-value")}
	_, err := NewService(driver, NewJSONLSink(&output)).Run(context.Background(), testRequest("hidden prompt"))
	if err == nil {
		t.Fatal("expected create error")
	}
	if strings.Contains(output.String(), "secret-value") || strings.Contains(output.String(), "hidden prompt") {
		t.Fatalf("sensitive value leaked: %s", output.String())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	var sawFailure bool
	for _, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == EventPhaseFinished && event.Summary == "task environment failed" {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatalf("missing safe provisioning failure event: %s", output.String())
	}
	if !strings.Contains(output.String(), `"error_kind":"environment_failed"`) || !strings.Contains(output.String(), `"result":{"task_id"`) {
		t.Fatalf("machine failure event must contain typed failure and result: %s", output.String())
	}
	if !strings.Contains(output.String(), `"cleanup_state":"complete"`) {
		t.Fatalf("create failure must report completed rollback: %s", output.String())
	}
}

func TestEventSinkFailureIsReportedAsDeliveryFailure(t *testing.T) {
	driver := &testDriver{outcome: AgentOutcome{ExitCode: new(int)}}
	result, err := NewService(driver, failedSink{err: errors.New("stdout closed")}).Run(context.Background(), testRequest("safe"))
	if err == nil {
		t.Fatal("expected event delivery error")
	}
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Kind != DeliveryFailedKind {
		t.Fatalf("wrong error classification: %v", err)
	}
	if result.State != DeliveryFailed {
		t.Fatalf("wrong result state: %+v", result)
	}
}

func (d *testDriver) Stop(context.Context, Request) error {
	d.steps = append(d.steps, "stop")
	return nil
}
