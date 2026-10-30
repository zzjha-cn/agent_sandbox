package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sandx/internal/execution"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

type contractDriver struct{ code int }

func (d contractDriver) Inspect(context.Context, execution.Request) (bool, bool, error) {
	return false, false, nil
}
func (d contractDriver) Admit(context.Context, execution.Request, bool, bool) error { return nil }
func (d contractDriver) Create(context.Context, execution.Request) error            { return nil }
func (d contractDriver) Resume(context.Context, execution.Request, bool) error      { return nil }
func (d contractDriver) Wait(context.Context, execution.Request) (execution.AgentOutcome, error) {
	return execution.AgentOutcome{ExitCode: &d.code, ContainerState: execution.ContainerStopped}, nil
}

func TestDecodeTaskV2AndDefaults(t *testing.T) {
	env, err := Decode(strings.NewReader(`{"schema_version":"sbx.execution/v2","task_id":"ci-42","prompt":"run checks","agent":{"provider":"codex"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if env.TaskID != "ci-42" || env.Limits.TimeoutSeconds != 0 {
		t.Fatalf("unexpected decoded envelope: %+v", env)
	}
}

func TestDecodeRejectsInvalidOrAmbiguousEnvelope(t *testing.T) {
	cases := []string{
		`{"schema_version":"v9","task_id":"ci","prompt":"x","agent":{"provider":"codex"}}`,
		`{"schema_version":"sbx.execution/v2","task_id":"../oops","prompt":"x","agent":{"provider":"codex"}}`,
		`{"schema_version":"sbx.execution/v2","task_id":"ci","prompt":""}`,
		`{"schema_version":"sbx.execution/v2","task_id":"ci","prompt":"x","agent":{"provider":"codex"},"extra":true}`,
		`{"schema_version":"sbx.execution/v2","task_id":"ci","task_id":"other","prompt":"x","agent":{"provider":"codex"}}`,
		`{"schema_version":"sbx.execution/v2","task_id":"ci","prompt":"x","agent":{"provider":"codex"}}{}`,
		`{"schema_version":"sbx.execution/v2","task_id":"ci","prompt":"x","agent":{"provider":"codex"},"timeout_seconds":86401}`,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := Decode(strings.NewReader(input))
			if err == nil {
				t.Fatal("expected invalid envelope")
			}
		})
	}
}

func TestEnvelopeRejectsNonCanonicalAndNullFields(t *testing.T) {
	base := `{"schema_version":"sbx.execution/v2","task_id":"ci","prompt":"x","agent":{"provider":"codex"}`
	for _, extra := range []string{`,"Fresh":true}`, `,"fresh":true,"Fresh":false}`, `,"fresh":null}`, `,"base":null}`, `,"timeout_seconds":null}`} {
		if _, err := Decode(strings.NewReader(base + extra)); err == nil {
			t.Fatal("accepted input outside advertised schema:", base+extra)
		}
	}
	if _, err := Decode(bytes.NewReader(append([]byte(base+`,"base":"`), 0xff, '"', '}'))); err == nil {
		t.Fatal("accepted non-UTF-8 envelope")
	}
}

func TestEnvelopeSizeLimitAndUnicode(t *testing.T) {
	env, err := Decode(strings.NewReader(`{"schema_version":"sbx.execution/v2","task_id":"ci","prompt":"测试","agent":{"provider":"codex"}}`))
	if err != nil || env.Prompt != "测试" {
		t.Fatalf("valid UTF-8: %+v %v", env, err)
	}
	if _, err := Decode(strings.NewReader(strings.Repeat(" ", MaxEnvelopeBytes+1))); err == nil {
		t.Fatal("oversized envelope accepted")
	}
}

func TestTimeoutAcceptsExactlyBoundedJSONSchemaIntegers(t *testing.T) {
	base := `{"schema_version":"sbx.execution/v2","task_id":"ci","prompt":"x","agent":{"provider":"codex"},"limits":{"timeout_seconds":`
	for literal, want := range map[string]int{"1.0": 1, "1e2": 100, "0.01e2": 1, "10e-1": 1, "86400.00": 86400, "0e99999999999999999999999": 0} {
		env, err := Decode(strings.NewReader(base + literal + "}}"))
		if err != nil || env.Limits.TimeoutSeconds != want {
			t.Fatalf("%s: %+v %v", literal, env, err)
		}
	}
	for _, literal := range []string{"1.1", "1e-999999", "1.00000000000000000000001", "86401.0", "1e99999999999999999999999", "true", `"12"`, "-1"} {
		if _, err := Decode(strings.NewReader(base + literal + "}}")); err == nil {
			t.Fatal("accepted invalid timeout", literal)
		}
	}
}

func TestEventSinkWritesVersionedResultJSONL(t *testing.T) {
	var out bytes.Buffer
	code := 7
	sink := NewEventSink(&out)
	err := sink.Emit(context.Background(), execution.Event{Type: execution.EventCompleted, TaskID: "ws.task", AttemptID: "attempt", Time: time.Unix(1, 0).UTC(), Result: &execution.Result{TaskID: "ws.task", AttemptID: "attempt", State: execution.AgentFailed, AgentExitCode: &code, ArtifactState: execution.ArtifactNotProduced, CleanupState: execution.CleanupComplete}})
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != SchemaVersion || event.Result == nil || event.Result.AgentExitCode == nil || *event.Result.AgentExitCode != 7 {
		t.Fatalf("bad protocol record: %+v", event)
	}
}

func TestProtocolServiceEventStreamIsConsumableRecordByRecord(t *testing.T) {
	var out bytes.Buffer
	driver := contractDriver{code: 0}
	taskValue := task.Task{Name: "build", WS: workspace.Workspace{ID: "repo"}}
	_, err := execution.NewService(driver, NewEventSink(&out)).Run(context.Background(), execution.Request{Task: taskValue, TaskID: "build-1842", Mode: execution.Headless, Prompt: "do work"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected event stream and terminal result: %s", out.String())
	}
	for _, line := range lines {
		var record Event
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid event record: %v: %s", err, line)
		}
		if record.SchemaVersion != SchemaVersion || record.TaskID != "build-1842" || record.AttemptID == "" {
			t.Fatalf("missing protocol identity: %+v", record)
		}
	}
	var last Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Type != execution.EventCompleted || last.Result == nil || last.Result.State != execution.Finished {
		t.Fatalf("terminal record missing result: %+v", last)
	}
}

func TestProtocolEventStageUsesStableVocabulary(t *testing.T) {
	var out bytes.Buffer
	err := NewEventSink(&out).Emit(context.Background(), execution.Event{Type: execution.EventFailed, TaskID: "t", AttemptID: "a", Time: time.Now(), Phase: "creating task", Stage: "creating task"})
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Phase != "creating task" || event.Stage != "creating task" {
		t.Fatalf("internal stage leaked through protocol: %+v", event)
	}
}

func TestVersionListsProtocolCapabilities(t *testing.T) {
	got := Version("test")
	if got.ApplicationVersion != "test" || got.SchemaVersion != SchemaVersion || len(got.Capabilities) < 3 || got.ArtifactDelivery != "local_directory,git_local" || strings.Join(got.Providers, ",") != "claude,codex" {
		t.Fatalf("incomplete capability response: %+v", got)
	}
}

func (d contractDriver) Stop(context.Context, execution.Request) error { return nil }
