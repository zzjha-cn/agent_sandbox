package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func durableRequest(t *testing.T) (Request, *FileStore) {
	t.Helper()
	req := testRequest("prompt-secret-never-store")
	req.Task.Home = t.TempDir()
	store, err := NewFileStore(req.Task)
	if err != nil {
		t.Fatal(err)
	}
	return req, store
}

func successfulRecord(t *testing.T) (Request, *FileStore, *TaskRecord) {
	t.Helper()
	req, store := durableRequest(t)
	result, err := NewService(&testDriver{outcome: AgentOutcome{ExitCode: new(int), ContainerState: ContainerStopped}}).WithStore(store).Run(context.Background(), req)
	if err != nil || result.State != Finished {
		t.Fatalf("run: %+v %v", result, err)
	}
	r, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return req, store, r
}

func stageState(r Result, phase Phase) ProgressState {
	for _, stage := range r.Stages {
		if stage.Phase == phase {
			return stage.State
		}
	}
	return ""
}

func TestDurableLifecycleHistoryAndSafeFields(t *testing.T) {
	req, store, before := successfulRecord(t)
	d := &testDriver{exists: true, outcome: AgentOutcome{ExitCode: new(int), ContainerState: ContainerStopped}}
	second, err := NewService(d).WithStore(store).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Load()
	if err != nil || len(r.Attempts) != 2 || r.Sequence <= before.Sequence {
		t.Fatalf("%+v %v", r, err)
	}
	if strings.Join(d.steps, ",") != "inspect,admit,resume,wait" {
		t.Fatal(d.steps)
	}
	if r.Attempts[0].Result.AttemptID == second.AttemptID {
		t.Fatal("overwrote attempt")
	}
	for _, a := range r.Attempts {
		if !a.Closed || a.Result.ExecutionState != Finished || a.Result.EventDeliveryState != "complete" || a.Result.ValidationState != "not_requested" || a.Result.ProductDeliveryState != "not_requested" {
			t.Fatalf("%+v", a)
		}
		for _, phase := range []Phase{AdmissionPhase, PreparePhase, ExecutePhase, FinalizePhase} {
			if stageState(a.Result, phase) != ProgressSucceeded {
				t.Fatalf("%s: %+v", phase, a.Result.Stages)
			}
		}
		for _, phase := range []Phase{CollectPhase, EvaluatePhase, DeliverPhase} {
			if stageState(a.Result, phase) != ProgressSkipped {
				t.Fatal(a.Result.Stages)
			}
		}
	}
	b, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	if bytes.Contains(b, []byte(req.Prompt)) || bytes.Contains(b, []byte("prompt")) || bytes.Contains(b, []byte("config")) {
		t.Fatalf("sensitive data: %s", b)
	}
	for _, name := range []string{"initialized", "record.json", "record.lock"} {
		info, err := os.Stat(filepath.Join(store.dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
	info, _ := os.Stat(store.dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal(info.Mode())
	}
}

func resealRecord(t *testing.T, store *FileStore, r *TaskRecord) {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	disk, _ := json.Marshal(diskRecord{Record: b, SHA256: hex.EncodeToString(h[:])})
	if err := os.WriteFile(filepath.Join(store.dir, "record.json"), disk, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreRejectsCorruptionAndMissingFacts(t *testing.T) {
	for _, kind := range []string{"truncated", "digest", "version", "identity", "stage", "missing", "marker", "symlink", "fifo", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			req, store, r := successfulRecord(t)
			path := filepath.Join(store.dir, "record.json")
			switch kind {
			case "truncated":
				_ = os.WriteFile(path, []byte("{"), 0600)
			case "digest":
				b, _ := os.ReadFile(path)
				b = bytes.Replace(b, []byte("finished"), []byte("modified"), 1)
				_ = os.WriteFile(path, b, 0600)
			case "version":
				r.SchemaVersion = "future"
				resealRecord(t, store, r)
			case "identity":
				r.TaskKey = "other.task"
				resealRecord(t, store, r)
			case "stage":
				r.Attempts[0].Result.Stages[0].State = "invented"
				resealRecord(t, store, r)
			case "missing":
				_ = os.Remove(path)
			case "marker":
				_ = os.Remove(filepath.Join(store.dir, "initialized"))
			case "symlink":
				_ = os.Rename(path, path+".old")
				if err := os.Symlink(path+".old", path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				_ = os.Remove(path)
				makeFIFO(t, path)
			case "oversized":
				if err := os.Truncate(path, MaxRecordBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Load(); !errors.Is(err, ErrRecordInvalid) {
				t.Fatalf("did not reject: %v", err)
			}
			d := &testDriver{}
			result, err := NewService(d).WithStore(store).Run(context.Background(), req)
			if err == nil || result.State != InternalErr || len(d.steps) != 0 {
				t.Fatalf("unsafe run: %+v %v %v", result, err, d.steps)
			}
		})
	}
}

func TestFileStoreReadIsNonMutatingAndControlPathsRejectLinks(t *testing.T) {
	req, store := durableRequest(t)
	if r, err := store.Load(); r != nil || err != nil {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(req.Task.Home, "executions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(req.Task.Home, "executions")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrRecordInvalid) {
		t.Fatal(err)
	}
	if _, err := store.Lock(); err == nil {
		t.Fatal("followed control directory link")
	}
	files, _ := os.ReadDir(outside)
	if len(files) != 0 {
		t.Fatal("wrote through symlink")
	}
}

func TestFileStoreAtomicReadersAndSequence(t *testing.T) {
	_, store, r := successfulRecord(t)
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	stale := *r
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			if _, err := store.Load(); err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
		}
	}()
	for range 25 {
		if err := store.Save(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
	if err := store.Save(context.Background(), &stale); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("stale writer: %v", err)
	}
}

func TestCancelledStoreWriteDoesNotInitializeRecord(t *testing.T) {
	_, store := durableRequest(t)
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(ctx, &TaskRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.dir, "initialized")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled write initialized record", err)
	}
}
