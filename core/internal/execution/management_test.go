package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sandx/internal/docker"
	"sandx/internal/isolation"
	"sandx/internal/task"
)

func completedManagedFixture(t *testing.T) (Request, *FileStore) {
	t.Helper()
	req, store := generalRequest(t)
	code := 0
	d := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	return req, store
}

func TestTaskDiscoveryAndAttemptLogsRetainEvidence(t *testing.T) {
	req, store := completedManagedFixture(t)
	r, _ := store.Load()
	id := r.Attempts[0].Result.AttemptID
	if err := os.MkdirAll(req.Task.StateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(req.Task.RunLogPath(), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := archiveAttemptLog(req.Task, id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(req.Task.RunLogPath(), []byte("later\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := AttemptLog(req.Task, id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "first\n" {
		t.Fatal(string(b))
	}
	if _, err := AttemptLog(req.Task, "../../secret"); err == nil {
		t.Fatal("unrecorded attempt accepted")
	}
	bad, _ := task.NewExecution(req.Task.Home, "broken")
	broken, _ := NewFileStore(bad)
	os.MkdirAll(broken.dir, 0700)
	os.WriteFile(filepath.Join(broken.dir, "initialized"), []byte(RecordSchema+"\n"), 0600)
	list, err := ListTasks(req.Task.Home)
	if err != nil || len(list) != 2 || list[0].Recovery != "record_invalid" || list[1].AttemptID != id {
		t.Fatalf("%+v %v", list, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(req.Task.RunLogPath(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := AttemptLog(req.Task, id); err == nil {
		t.Fatal("symlink log accepted")
	}
}

func TestLatestLogCannotBorrowPreviousAttemptOutput(t *testing.T) {
	req, store := completedManagedFixture(t)
	os.MkdirAll(req.Task.StateDir(), 0700)
	if err := os.WriteFile(req.Task.RunLogPath(), []byte("previous output"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(req.Task.RunLogPath(), old, old); err != nil {
		t.Fatal(err)
	}
	r, _ := store.Load()
	if _, err := AttemptLog(req.Task, r.Attempts[0].Result.AttemptID); err == nil {
		t.Fatal("borrowed old output")
	}
}

func TestStopRequestsAreScopedToOneActivity(t *testing.T) {
	req, store := completedManagedFixture(t)
	r, _ := store.Load()
	id := r.Attempts[0].Result.AttemptID
	ctx, stop, err := watchTaskStop(context.Background(), req.Task, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RequestStop(req.Task); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stop not observed")
	}
	stop()
	// A continuation shares its attempt ID but must ignore the previous stop.
	next, finish, err := watchTaskStop(context.Background(), req.Task, newAttemptID())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	select {
	case <-next.Done():
		t.Fatal("stale request cancelled new activity")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := RequestStop(req.Task); err != nil {
		t.Fatal(err)
	}
	select {
	case <-next.Done():
	case <-time.After(time.Second):
		t.Fatal("continuation not cancelled")
	}
}

func managerShim(t *testing.T, taskName string, foreign bool) RuntimeManager {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "docker")
	sum := sha256.Sum256([]byte("test-daemon"))
	namespace := "daemon-" + hex.EncodeToString(sum[:])[:24]
	script := `#!/bin/sh
case "$1" in
 info) echo test-daemon;;
 volume) if [ "$2" = inspect ]; then
 if [ "$5" = sbx-daemon-ownership ]; then echo '{"sbx.isolation":"sbx.isolation/v1","sbx.owner":"test-owner","sbx.namespace":"NAMESPACE"}'
 else echo 'No such volume' >&2; exit 1; fi
 fi;;
 container) echo 'No such container' >&2; exit 1;;
 ps) case "$*" in *sbx.owner=*) :;; *) FOREIGN;; esac;;
esac
`
	script = strings.ReplaceAll(script, "NAMESPACE", namespace)
	output := ":"
	if foreign {
		output = `echo '{"ID":"foreign","Names":"sbx-tasks-` + taskName + `"}'`
	}
	script = strings.ReplaceAll(script, "FOREIGN", output)
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	client := &docker.Client{Bin: bin}
	return RuntimeManager{Docker: client, Isolation: isolation.New(client, isolation.Policy{Owner: "test-owner"})}
}

func TestOrphanRecoveryPreservesUnknownEffectsAndRefusesReplay(t *testing.T) {
	req, store := completedManagedFixture(t)
	unlock, _ := store.Lock()
	r, _ := store.Load()
	r.Attempts[0].Closed = false
	r.Attempts[0].Result.CleanupState = CleanupFailed
	r.Operations = append(r.Operations, OperationRecord{Key: "unknown-write", AttemptID: r.Attempts[0].Result.AttemptID, Kind: "tool", State: "unknown"})
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	unlock()
	manager := managerShim(t, req.Task.Name, false)
	if err := manager.Mutate(context.Background(), req.Task, "clean"); err == nil {
		t.Fatal("clean bypassed orphan recovery")
	}
	if err := manager.Mutate(context.Background(), req.Task, "recover"); err != nil {
		t.Fatal(err)
	}
	r, err := store.Load()
	if err != nil || !r.Attempts[0].Closed || r.Operations[0].State != "unknown" || r.Recovery() != "reconcile_operations" || r.Attempts[0].Pipeline.Artifact == nil {
		t.Fatalf("%+v %v", r, err)
	}
	if err := manager.Mutate(context.Background(), req.Task, "clean"); err != nil {
		t.Fatal(err)
	}
	r, _ = store.Load()
	if r.Recovery() != "reconcile_operations" {
		t.Fatal(r.Recovery())
	}
	d := &producingDriver{body: "{}"}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); !errors.Is(err, ErrRecoveryRequired) || d.creates != 0 {
		t.Fatalf("agent replayed: %v %d", err, d.creates)
	}
}

func TestRuntimeMaintenanceRejectsForeignAndBusyWithoutRewritingFacts(t *testing.T) {
	req, store := completedManagedFixture(t)
	before, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	manager := managerShim(t, req.Task.Name, true)
	if err := manager.Mutate(context.Background(), req.Task, "recover"); err == nil || !strings.Contains(err.Error(), "ownership mismatch") {
		t.Fatalf("foreign accepted: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	if string(before) != string(after) {
		t.Fatal("changed foreign record")
	}
	unlock, err := (&RuntimeDriver{}).Lock(req)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := manager.Mutate(context.Background(), req.Task, "clean"); !errors.Is(err, ErrRecordBusy) {
		t.Fatal(err)
	}
}

func TestGeneralOrphanCannotBeHiddenByNewAttempt(t *testing.T) {
	req, store := completedManagedFixture(t)
	unlock, _ := store.Lock()
	r, _ := store.Load()
	r.Attempts[0].Closed = false
	// Remove completion facts to model an interrupted execute phase.
	r.Attempts[0].Pipeline.Artifact = nil
	r.Attempts[0].Pipeline.Outputs = nil
	r.Attempts[0].Result.ExecutionState = Running
	r.Attempts[0].Result.setStage(ExecutePhase, ProgressRunning)
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	unlock()
	before, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	if _, err := NewService(&producingDriver{}).WithStore(store).Run(context.Background(), req); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	if string(before) != string(after) {
		t.Fatal("orphan hidden")
	}
	if err := managerShim(t, req.Task.Name, false).Mutate(context.Background(), req.Task, "recover"); err != nil {
		t.Fatal(err)
	}
	r, _ = store.Load()
	if r.Recovery() != "new_attempt_allowed" || r.Attempts[0].Result.ExecutionState != Cancelled {
		t.Fatal(r.Recovery())
	}
}

func TestPartialCleanupDoesNotRetirePolicyBinding(t *testing.T) {
	req, store := completedManagedFixture(t)
	meta := task.Meta{Task: req.Task.Name, WS: "tasks", Agent: "codex", OrganizationPolicyDigest: "original-policy"}
	if err := req.Task.WriteMeta(meta); err != nil {
		t.Fatal(err)
	}
	manager := managerShim(t, req.Task.Name, false)
	bin := manager.Docker.Bin
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	start := strings.Index(text, " ps)")
	end := strings.Index(text[start:], "\nesac") + start
	text = text[:start] + ` ps) echo '{"ID":"owned","Names":"` + req.Task.Container() + `"}';;
 rm) echo 'controlled remove failure' >&2; exit 1;;` + text[end:]
	if err := os.WriteFile(bin, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	if err := manager.Mutate(context.Background(), req.Task, "clean"); err == nil {
		t.Fatal("cleanup failure ignored")
	}
	actual, _, err := req.Task.ReadMeta()
	if err != nil || actual.RuntimeRetired || actual.OrganizationPolicyDigest != "original-policy" {
		t.Fatalf("%+v %v", actual, err)
	}
	after, _ := os.ReadFile(filepath.Join(store.dir, "record.json"))
	if string(before) != string(after) {
		t.Fatal("cleanup changed retained execution")
	}
}

func TestRuntimeIdentityPreservesCancellationClassification(t *testing.T) {
	req, _ := generalRequest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := docker.New(false).WithContext(ctx)
	_, err := NewService(&RuntimeDriver{Docker: client}).Run(ctx, req)
	var failure *ExecutionError
	if !errors.As(err, &failure) || failure.Kind != CancelledKind {
		t.Fatalf("cancellation mislabeled: %v", err)
	}
}
