package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sandx/internal/docker"
	"sandx/internal/fsutil"
	"sandx/internal/isolation"
	"sandx/internal/task"
)

// TaskSummary contains durable observations, not a claim that a runtime is live.
type TaskSummary struct {
	TaskID         string     `json:"task_id"`
	Recovery       string     `json:"recovery"`
	AttemptID      string     `json:"attempt_id,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	ExecutionState State      `json:"execution_state,omitempty"`
}

func ListTasks(home string) ([]TaskSummary, error) {
	root := filepath.Join(home, "executions", "tasks")
	// Never follow a redirected journal namespace, even during discovery.
	probe, err := task.NewExecution(home, "probe")
	if err != nil {
		return nil, err
	}
	store, err := NewFileStore(probe)
	if err != nil {
		return nil, err
	}
	if err := store.checkPath(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []TaskSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []TaskSummary{}
	for _, entry := range entries {
		t, err := task.NewExecution(home, entry.Name())
		if err != nil {
			continue
		}
		summary := TaskSummary{TaskID: t.Name, Recovery: "record_invalid"}
		s, _ := NewFileStore(t)
		record, err := s.Load()
		if err == nil && record != nil {
			summary.Recovery = record.Recovery()
			last := record.Attempts[len(record.Attempts)-1]
			summary.AttemptID, summary.ExecutionState = last.Result.AttemptID, last.Result.ExecutionState
			summary.UpdatedAt = &last.UpdatedAt
		} else if err == nil {
			summary.Recovery = "not_recorded"
		}
		out = append(out, summary)
	}
	return out, nil
}

// RequestStop is deliberately independent of the execution lock: its owner must
// observe cancellation while holding that lock through finalization. The marker
// is scoped to one attempt and lives outside agent-accessible mounts.
func RequestStop(t task.Task) (string, error) {
	s, err := NewFileStore(t)
	if err != nil {
		return "", err
	}
	r, err := s.Load()
	if err != nil {
		return "", err
	}
	if r == nil {
		return "", errors.New("task has no execution record")
	}
	control, err := readRegular(filepath.Join(s.dir, "controller"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	parts := strings.Split(string(control), "\n")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", ErrRecordInvalid
	}
	if err := fsutil.AtomicWrite(filepath.Join(s.dir, "stop-request"), control, 0600); err != nil {
		return "", err
	}
	return parts[0], nil
}

func watchTaskStop(parent context.Context, t task.Task, id string) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s, err := NewFileStore(t)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	token := id + "\n" + newAttemptID() + "\n"
	if err := fsutil.AtomicWrite(filepath.Join(s.dir, "controller"), []byte(token), 0600); err != nil {
		cancel()
		return nil, nil, err
	}
	go func() {
		defer close(done)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			if s != nil {
				b, err := readRegular(filepath.Join(s.dir, "stop-request"))
				if err == nil && string(b) == token {
					cancel()
					return
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					cancel()
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return ctx, func() { cancel(); <-done; _ = os.Remove(filepath.Join(s.dir, "controller")) }, nil
}

// AttemptLog returns a verified journal-owned archive, or the live log only for
// the current attempt. Old runs without archives are explicitly unavailable.
func AttemptLog(t task.Task, id string) (string, error) {
	s, err := NewFileStore(t)
	if err != nil {
		return "", err
	}
	r, err := s.Load()
	if err != nil {
		return "", err
	}
	if r == nil {
		return "", errors.New("task has no execution record")
	}
	last := r.Attempts[len(r.Attempts)-1]
	if id == "" {
		id = last.Result.AttemptID
	}
	found := false
	for _, a := range r.Attempts {
		if a.Result.AttemptID == id {
			found = true
		}
	}
	if !found {
		return "", errors.New("attempt is not recorded for this task")
	}
	if filepath.Base(id) != id || id == "." || id == ".." {
		return "", ErrRecordInvalid
	}
	archive := filepath.Join(s.dir, "logs", id+".log")
	if err := controlledDirectories(t.Home, filepath.Dir(archive)); err != nil {
		return "", err
	}
	if info, err := os.Lstat(archive); err == nil {
		if !info.Mode().IsRegular() {
			return "", ErrRecordInvalid
		}
		return archive, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if id != last.Result.AttemptID {
		return "", errors.New("this historical attempt has no retained log")
	}
	if err := controlledDirectories(t.Home, t.StateDir()); err != nil {
		return "", err
	}
	info, err := os.Lstat(t.RunLogPath())
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrRecordInvalid
	}
	if info.ModTime().Before(last.StartedAt) {
		return "", errors.New("this attempt has not produced a log")
	}
	return t.RunLogPath(), nil
}

func archiveAttemptLog(t task.Task, id string) error {
	if err := controlledDirectories(t.Home, t.StateDir()); err != nil {
		return err
	}
	src, err := privateOpen(t.RunLogPath(), os.O_RDONLY|syscall.O_NONBLOCK)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer src.Close()
	s, err := NewFileStore(t)
	if err != nil {
		return err
	}
	root := filepath.Join(s.dir, "logs")
	if err := controlledDirectories(t.Home, root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".log-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, copyErr := io.Copy(f, src)
	if err := errors.Join(copyErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(root, id+".log")); err != nil {
		return err
	}
	return syncDirectory(root)
}

// RuntimeManager never creates a runtime, submits tools or changes artifacts.
// Cleanup is repeatable after partial failure and only addresses scoped task
// resources. Shared credentials/caches and local workspaces are always retained.
type RuntimeManager struct {
	Docker    *docker.Client
	Isolation *isolation.Guard
}

func (m RuntimeManager) resources(ctx context.Context, t task.Task) (map[string][]docker.Item, error) {
	if t.ExecutionDir == "" {
		return nil, errors.New("general task required")
	}
	scope, err := m.Isolation.Status(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkTaskScope(t, scope); err != nil {
		return nil, err
	}
	selectors := []string{"sbx.ws=tasks", "sbx.task=" + t.Name, "sbx.execution-home=" + t.ExecutionHomeKey()}
	for k, v := range scope.Labels() {
		selectors = append(selectors, k+"="+v)
	}
	result := map[string][]docker.Item{}
	for _, kind := range []string{"container", "network", "volume"} {
		items, err := m.Docker.WithContext(ctx).ListByLabel(kind, selectors...)
		if err != nil {
			return nil, err
		}
		result[kind] = items
	}
	// Reject partially/foreign-labeled task resources instead of silently
	// retiring a runtime whose old sidecars would survive cleanup.
	for _, kind := range []string{"container", "network", "volume"} {
		all, err := m.Docker.WithContext(ctx).ListByLabel(kind, "sbx.ws=tasks", "sbx.task="+t.Name)
		if err != nil {
			return nil, err
		}
		if len(all) != len(result[kind]) {
			return nil, errors.New("task resource ownership mismatch; no resources changed")
		}
	}
	// Names alone never authorize mutation. A colliding unscoped runtime must
	// block recovery rather than be mistaken for an absent task.
	_, exists, err := m.Docker.WithContext(ctx).Inspect(t.Container())
	if err != nil {
		return nil, err
	}
	found := false
	for _, it := range result["container"] {
		if it.Str("Names") == t.Container() {
			found = true
		}
	}
	if exists && !found {
		return nil, errors.New("runtime ownership mismatch; no resources changed")
	}
	return result, nil
}

func (m RuntimeManager) Mutate(ctx context.Context, t task.Task, action string) error {
	if err := controlledDirectories(t.Home, t.StateDir()); err != nil {
		return err
	}
	if action != "stop" && action != "clean" && action != "recover" {
		return errors.New("unsupported runtime action")
	}
	unlock, err := (&RuntimeDriver{}).Lock(Request{Task: t})
	if err != nil {
		return err
	}
	defer unlock()
	store, err := NewFileStore(t)
	if err != nil {
		return err
	}
	release, err := store.Lock()
	if err != nil {
		return err
	}
	defer release()
	record, err := store.Load()
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("task has no execution record")
	}
	if action == "clean" && !record.Attempts[len(record.Attempts)-1].Closed {
		return errors.New("interrupted attempt requires task recover (or task stop for a live supervisor) before cleanup")
	}
	resources, err := m.resources(ctx, t)
	if err != nil {
		return err
	}
	client := m.Docker.WithContext(ctx)
	// Stop all task containers before touching the record or deleting resources.
	for _, it := range resources["container"] {
		id := it.Str("ID")
		if id == "" {
			return errors.New("container identity missing")
		}
		state, exists, err := client.Inspect(id)
		if err != nil {
			return err
		}
		if exists && state.Running {
			if err := client.Stop(id); err != nil {
				return err
			}
		}
		state, exists, err = client.Inspect(id)
		if err != nil {
			return err
		}
		if exists && state.Running {
			return errors.New("task container still running")
		}
	}
	if action == "clean" {
		// A live supervisor holds the same locks; its effects can never race this
		// cleanup. Unknown writes remain journaled and continue to block agent replay.
		for _, kind := range []string{"container", "network", "volume"} {
			for _, it := range resources[kind] {
				id := it.Str("ID")
				if kind == "volume" {
					id = it.Str("Name")
				}
				if id == "" {
					return fmt.Errorf("%s identity missing", kind)
				}
				switch kind {
				case "container":
					err = client.Rm(id)
				case "network":
					err = client.NetworkRm(id)
				case "volume":
					err = client.VolumeRm(id)
				}
				if err != nil && !docker.IsNotFound(err) {
					return err
				}
			}
		}
		// Retain meta identity/provider, but retire the runtime policy binding only
		// after all resources have been removed. New admission still checks owner.
		meta, ok, err := t.ReadMeta()
		if err != nil {
			return err
		}
		if ok {
			meta.RuntimeRetired = true
			if err := t.WriteMeta(meta); err != nil {
				return err
			}
		}
	}
	if action == "recover" || action == "stop" {
		last := &record.Attempts[len(record.Attempts)-1]
		if !last.Closed {
			for _, st := range last.Result.Stages {
				if st.Phase == ExecutePhase && st.State != ProgressPending && st.State != ProgressSkipped {
					if err := archiveAttemptLog(t, last.Result.AttemptID); err != nil {
						return err
					}
					break
				}
			}
			// A dead supervisor cannot prove completion from a stopped container or a
			// stale run.exit. Preserve every side-effect fact; never infer success.
			if last.Result.ExecutionState == "" || last.Result.ExecutionState == Ready || last.Result.ExecutionState == Running {
				last.Result.ExecutionState, last.Result.State = Cancelled, Cancelled
				last.ErrorKind = CancelledKind
			}
			for i := range last.Result.Stages {
				st := &last.Result.Stages[i]
				if st.State == ProgressRunning && (st.Phase == ExecutePhase || st.Phase == AdmissionPhase) {
					st.State = ProgressFailed
				}
			}
			last.Closed = true
			last.UpdatedAt = time.Now().UTC()
		}
		// CleanupPending/Failed refers to stopping the agent, now independently
		// observed. Preparation/artifact/operation uncertainty remains unchanged.
		for i := range record.Attempts {
			a := &record.Attempts[i]
			if a.Result.CleanupState == CleanupPending || a.Result.CleanupState == CleanupFailed {
				a.Result.CleanupState = CleanupComplete
				a.UpdatedAt = time.Now().UTC()
			}
			if i == len(record.Attempts)-1 {
				a.Result.ContainerState = string(ContainerStopped)
			}
		}
		if err := store.Save(ctx, record); err != nil {
			return err
		}
	}
	return nil
}
