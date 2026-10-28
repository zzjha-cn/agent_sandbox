package execution

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnsupportedRequirementsRejectBeforeDriver(t *testing.T) {
	for _, required := range []Requirements{{SourceKind: "directory"}, {Collect: true}, {Evaluate: true}, {Deliver: true}, {Tools: []string{"read"}}} {
		d := &testDriver{}
		req := testRequest("secret")
		req.Requirements = required
		r, err := NewService(d).Run(context.Background(), req)
		if err == nil || r.State != Rejected || len(d.steps) != 0 {
			t.Fatalf("%+v %v %v", r, err, d.steps)
		}
	}
}

func TestAlreadyCancelledRequestDoesNotStartRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &testDriver{}
	r, err := NewService(d).Run(ctx, testRequest("never start"))
	if !errors.Is(err, context.Canceled) || r.State != Cancelled || len(d.steps) != 0 || r.CleanupState != CleanupNotNeeded {
		t.Fatalf("%+v %v %v", r, err, d.steps)
	}
}

func TestStartupCompletingAfterCancellationStillStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &cancelDuringCreateDriver{cancel: cancel}
	r, err := NewService(d).Run(ctx, testRequest("started before cancellation"))
	if !errors.Is(err, context.Canceled) || r.State != Cancelled || r.CleanupState != CleanupComplete || strings.Join(d.steps, ",") != "inspect,admit,create,stop" {
		t.Fatalf("%+v %v %v", r, err, d.steps)
	}
}

type cancelDuringCreateDriver struct {
	testDriver
	cancel context.CancelFunc
}

type cancelDuringSaveStore struct {
	RecordStore
	cancel context.CancelFunc
	phase  Phase
}

func (f cancelDuringSaveStore) Save(ctx context.Context, r *TaskRecord) error {
	if stageState(r.Attempts[len(r.Attempts)-1].Result, f.phase) == ProgressRunning {
		f.cancel()
		return ctx.Err()
	}
	return f.RecordStore.Save(ctx, r)
}

func TestCancellationDuringCheckpointIOPreservesClassification(t *testing.T) {
	for _, phase := range []Phase{PreparePhase, ExecutePhase} {
		req, store := durableRequest(t)
		ctx, cancel := context.WithCancel(context.Background())
		d := &testDriver{}
		r, err := NewService(d).WithStore(cancelDuringSaveStore{RecordStore: store, cancel: cancel, phase: phase}).Run(ctx, req)
		cancel()
		var classified *ExecutionError
		if !errors.As(err, &classified) || classified.Kind != CancelledKind || r.State != Cancelled {
			t.Fatalf("%+v %v", r, err)
		}
		want := "inspect,admit"
		if phase == ExecutePhase {
			want = ",create,stop"
			if r.CleanupState != CleanupComplete {
				t.Fatal(r)
			}
		}
		if !strings.HasSuffix(strings.Join(d.steps, ","), want) {
			t.Fatal(d.steps)
		}
	}
}

func (d *cancelDuringCreateDriver) Create(context.Context, Request) error {
	d.steps = append(d.steps, "create")
	d.cancel()
	return nil
}

func TestHandoffRemainsInspectableAndNewAttemptInspects(t *testing.T) {
	for _, tc := range []struct {
		mode   Mode
		detach bool
	}{{Interactive, false}, {Headless, true}} {
		req, store := durableRequest(t)
		req.Mode, req.Detach = tc.mode, tc.detach
		r, err := NewService(&testDriver{}).WithStore(store).Run(context.Background(), req)
		if err != nil || stageState(r, ExecutePhase) != ProgressHandedOff {
			t.Fatalf("%+v %v", r, err)
		}
		record, _ := store.Load()
		if record.Recovery() != "inspect_runtime" || !record.Attempts[0].Closed {
			t.Fatalf("%+v", record)
		}
		d := &testDriver{exists: true}
		if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if strings.Join(d.steps, ",") != "inspect,admit,resume" {
			t.Fatal(d.steps)
		}
		record, _ = store.Load()
		if len(record.Attempts) != 2 {
			t.Fatal("lost history")
		}
	}
}

func TestRecoveryBlocksUnresolvedPrepareCleanupAndOperations(t *testing.T) {
	for _, kind := range []string{"prepare", "cleanup", "tool", "delivery"} {
		t.Run(kind, func(t *testing.T) {
			req, store, record := successfulRecord(t)
			switch kind {
			case "prepare":
				record.Attempts[0].Result.setStage(PreparePhase, ProgressRunning)
				record.Attempts[0].Closed = false
			case "cleanup":
				record.Attempts[0].Result.CleanupState = CleanupFailed
			default:
				record.Operations = []OperationRecord{{Key: "operation-1", AttemptID: record.Attempts[0].Result.AttemptID, Kind: kind, State: "unknown"}}
			}
			unlock, _ := store.Lock()
			err := store.Save(context.Background(), record)
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			d := &testDriver{}
			r, err := NewService(d).WithStore(store).Run(context.Background(), req)
			if r.State != Rejected || !errors.Is(err, ErrRecoveryRequired) || len(d.steps) != 0 {
				t.Fatalf("%+v %v %v", r, err, d.steps)
			}
			after, _ := store.Load()
			if len(after.Attempts) != 1 {
				t.Fatal("overwrote unresolved attempt")
			}
		})
	}
}

type failingStore struct {
	RecordStore
	fail func(*TaskRecord) bool
	err  error
}

func (f failingStore) Save(ctx context.Context, r *TaskRecord) error {
	if f.fail(r) {
		return f.err
	}
	return f.RecordStore.Save(ctx, r)
}

func TestRecordFailureStopsBeforeNextSideEffect(t *testing.T) {
	for _, phase := range []Phase{AdmissionPhase, PreparePhase, ExecutePhase, FinalizePhase} {
		t.Run(string(phase), func(t *testing.T) {
			req, store := durableRequest(t)
			fail := errors.New("injected record failure")
			fs := failingStore{RecordStore: store, err: fail, fail: func(r *TaskRecord) bool {
				return stageState(r.Attempts[len(r.Attempts)-1].Result, phase) == ProgressRunning
			}}
			d := &testDriver{outcome: AgentOutcome{ExitCode: new(int)}}
			var out bytes.Buffer
			r, err := NewService(d, NewJSONLSink(&out)).WithStore(fs).Run(context.Background(), req)
			if !errors.Is(err, fail) || r.State != InternalErr {
				t.Fatalf("%+v %v", r, err)
			}
			steps := strings.Join(d.steps, ",")
			if phase == AdmissionPhase && steps != "" {
				t.Fatal(steps)
			}
			if phase == PreparePhase && steps != "inspect,admit" {
				t.Fatal(steps)
			}
			if phase == ExecutePhase && steps != "inspect,admit,create,stop" {
				t.Fatal(steps)
			}
			if phase == FinalizePhase && strings.Contains(out.String(), `"type":"completed"`) {
				t.Fatal("reported success despite failed finalization")
			}
		})
	}
}

func TestDurableExecutionRetainsOutcomeDespiteWriterFailure(t *testing.T) {
	req, store := durableRequest(t)
	r, err := NewService(&testDriver{outcome: AgentOutcome{ExitCode: new(int)}}, failedSink{err: errors.New("secret-backend-message")}).WithStore(store).Run(context.Background(), req)
	if err == nil || r.State != DeliveryFailed || r.ExecutionState != Finished {
		t.Fatalf("%+v %v", r, err)
	}
	record, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	last := record.Attempts[0]
	if last.Result.ExecutionState != Finished || last.Result.EventDeliveryState != "failed" || last.ErrorKind != DeliveryFailedKind {
		t.Fatalf("%+v", last)
	}
	b, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	if bytes.Contains(b, []byte("secret-backend-message")) {
		t.Fatal("stored backend error")
	}
}

func TestFinalAcknowledgementWriteFailureIsNotSuccess(t *testing.T) {
	req, store := durableRequest(t)
	failure := errors.New("acknowledgement storage failure")
	fs := failingStore{RecordStore: store, err: failure, fail: func(r *TaskRecord) bool { return r.Attempts[len(r.Attempts)-1].Result.EventDeliveryState == "complete" }}
	var out bytes.Buffer
	r, err := NewService(&testDriver{outcome: AgentOutcome{ExitCode: new(int)}}, NewJSONLSink(&out)).WithStore(fs).Run(context.Background(), req)
	if !errors.Is(err, failure) || r.State != InternalErr || r.ExecutionState != Finished {
		t.Fatalf("%+v %v", r, err)
	}
	record, err := store.Load()
	if err != nil || record.Attempts[0].Result.EventDeliveryState != "pending" {
		t.Fatalf("%+v %v", record, err)
	}
	if !strings.Contains(out.String(), `"type":"completed"`) {
		t.Fatal("expected emitted completion followed by failed durable acknowledgement")
	}
}

func TestPrimaryFailureRetainsStorageAndWriterErrors(t *testing.T) {
	req, store := durableRequest(t)
	primary, storage, writer := errors.New("private-agent-error"), errors.New("private-storage-error"), errors.New("private-writer-error")
	fs := failingStore{RecordStore: store, err: storage, fail: func(r *TaskRecord) bool {
		return stageState(r.Attempts[len(r.Attempts)-1].Result, FinalizePhase) == ProgressRunning
	}}
	r, err := NewService(&testDriver{err: primary}, failedSink{err: writer}).WithStore(fs).Run(context.Background(), req)
	if r.State != EnvironmentErr || !errors.Is(err, primary) || !errors.Is(err, storage) || !errors.Is(err, writer) {
		t.Fatalf("%+v %v", r, err)
	}
	record, err := store.Load()
	if err != nil || record.Attempts[0].ErrorKind != EnvironmentFailed || len(record.Attempts[0].SecondaryErrors) < 2 {
		t.Fatalf("%+v %v", record, err)
	}
	b, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	if bytes.Contains(b, []byte("private-")) {
		t.Fatal("raw errors in journal")
	}
}

type cancelDriver struct {
	testDriver
	stopErr          error
	stopContextValid bool
}

func (d *cancelDriver) Wait(ctx context.Context, _ Request) (AgentOutcome, error) {
	<-ctx.Done()
	return AgentOutcome{ContainerState: ContainerRunning}, ctx.Err()
}
func (d *cancelDriver) Stop(ctx context.Context, _ Request) error {
	deadline, ok := ctx.Deadline()
	d.stopContextValid = ctx.Err() == nil && ok && time.Until(deadline) > 0 && time.Until(deadline) <= CleanupTimeout
	return d.stopErr
}

func TestCancellationFinalizesWithIndependentBudgetAndPreservesStopFailure(t *testing.T) {
	for _, failStop := range []bool{false, true} {
		req, store := durableRequest(t)
		d := &cancelDriver{}
		if failStop {
			d.stopErr = errors.New("stop-secret-error")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r, err := NewService(d).WithStore(store).Run(ctx, req)
		cancel()
		if err == nil || r.State != TimedOut || !d.stopContextValid {
			t.Fatalf("%+v %v stop=%v", r, err, d.stopContextValid)
		}
		record, loadErr := store.Load()
		if loadErr != nil || !record.Attempts[0].Closed {
			t.Fatalf("%+v %v", record, loadErr)
		}
		want := CleanupComplete
		if failStop {
			want = CleanupFailed
			if !errors.Is(err, d.stopErr) {
				t.Fatal(err)
			}
		}
		if r.CleanupState != want {
			t.Fatalf("%+v", r)
		}
	}
}
