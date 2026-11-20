package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sandx/internal/fsutil"
	"sandx/internal/projectmemory"
)

// EffectiveMemoryMode preserves personal provider-native behavior. Managed
// organization runs default to off unless host configuration selects managed.
func EffectiveMemoryMode(req Request) string {
	if req.Config.Memory.Mode != "" {
		return req.Config.Memory.Mode
	}
	if req.OrganizationPolicyDigest != "" {
		return "off"
	}
	return "native"
}
func validateMemory(req Request) error {
	return validateMemoryContext(context.Background(), req)
}

func validateMemoryContext(ctx context.Context, req Request) error {
	mode := EffectiveMemoryMode(req)
	if req.Config.Memory.Extract.Auto && req.OrganizationPolicyDigest != "" {
		return errors.New("automatic memory extraction requires personal review; organization mutation is not yet supported")
	}
	if mode != "native" && mode != "managed" && mode != "off" {
		return errors.New("memory.mode must be native, managed or off")
	}
	if req.OrganizationPolicyDigest != "" && mode == "native" {
		return errors.New("organization-managed runs cannot use unreviewed native memory; choose managed or off")
	}
	if mode != "managed" {
		return nil
	}
	store := projectmemory.Store{Home: req.Task.Home}
	id, e := store.Resolve(req.Config.Memory.Project, req.Task.WS.Root)
	if e != nil {
		return fmt.Errorf("project memory: %w", e)
	}
	for _, p := range req.Config.Memory.Paths {
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Contains(p, "\\") {
			return errors.New("memory.paths must contain relative paths")
		}
	}
	_, e = selectMemoryContext(ctx, req, store, id)
	return e
}

func selectMemory(req Request, store projectmemory.Store, id string) (projectmemory.Snapshot, error) {
	return selectMemoryContext(context.Background(), req, store, id)
}

func selectMemoryContext(ctx context.Context, req Request, store projectmemory.Store, id string) (projectmemory.Snapshot, error) {
	q, err := memorySelection(req)
	if err != nil {
		return projectmemory.Snapshot{}, err
	}
	if req.Config.Memory.Snapshot != "" {
		if len(req.Config.Memory.Paths) > 0 || len(req.Config.Memory.Tags) > 0 || req.Config.Memory.Query != "" {
			return projectmemory.Snapshot{}, errors.New("pinned memory cannot be combined with paths/tags/query")
		}
		v, e := store.Pinned(id, req.Config.Memory.Snapshot)
		if e == nil {
			e = store.Queries().ValidateSnapshotFacts(ctx, v, q.Facts)
		}
		return v, e
	}
	return store.Queries().SearchWithin(ctx, projectmemory.QueryContext{Project: id, Scope: q}, projectmemory.Query{Text: q.Query})
}

func memorySelection(req Request) (projectmemory.Selection, error) {
	observation, err := (projectmemory.FileObservationProvider{Root: req.Task.Worktree(), Watch: req.Config.Memory.Watch, Facts: req.Config.Memory.Facts}).Observe(context.Background())
	facts := observation.Values
	return projectmemory.Selection{Paths: req.Config.Memory.Paths, Tags: req.Config.Memory.Tags, Query: req.Config.Memory.Query, Facts: facts}, err
}

type MemorySession struct {
	RecallID string                  `json:"recall_id,omitempty"`
	Pinned   string                  `json:"pinned,omitempty"`
	Schema   string                  `json:"schema_version"`
	TaskKey  string                  `json:"task_key"`
	Provider string                  `json:"provider"`
	Mode     string                  `json:"mode"`
	Snapshot *projectmemory.Snapshot `json:"snapshot,omitempty"`
}

func MemoryTaskDir(home, key string) string {
	return filepath.Join(home, "task-memory", strings.TrimPrefix(projectmemory.Digest(key), "sha256:"))
}
func ReadMemorySession(home, key string) (MemorySession, error) {
	var v MemorySession
	b, e := projectmemory.ReadFile(filepath.Join(MemoryTaskDir(home, key), "session.json"), 1<<20)
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(b, &v)
	if e == nil && (v.Schema != "sbx.memory-session/v1" || v.TaskKey != key || (v.Mode != "native" && v.Mode != "managed" && v.Mode != "off") || v.Mode == "managed" && v.Snapshot == nil || v.Mode != "managed" && v.Snapshot != nil) {
		e = errors.New("invalid memory session")
	}
	return v, e
}

// prepareMemory runs under execution locks before launch, after Source preparation.
// Per-session context remains frozen even when a new CLI call records a new
// attempt. A fresh conversation selects a new snapshot; old snapshots survive done.
func prepareMemory(req Request, active bool) (MemorySession, error) {
	return prepareMemoryContext(context.Background(), req, active)
}

func prepareMemoryContext(ctx context.Context, req Request, active bool) (MemorySession, error) {
	if active && req.Fresh {
		return MemorySession{}, errors.New("stop the active task before starting a fresh conversation")
	}
	if e := validateMemoryContext(ctx, req); e != nil {
		return MemorySession{}, e
	}
	mode := EffectiveMemoryMode(req)
	t := req.Task
	v := MemorySession{Schema: "sbx.memory-session/v1", TaskKey: t.ID(), Provider: req.Config.DefaultAgent, Mode: mode, Pinned: req.Config.Memory.Snapshot}
	dir := MemoryTaskDir(t.Home, t.ID())
	prior, e := ReadMemorySession(t.Home, t.ID())
	known := e == nil
	if e != nil && !os.IsNotExist(e) {
		return v, e
	}
	_, sidErr := os.Stat(filepath.Join(t.StateDir(), "codex-session"))
	hasHistory := active || t.HasPriorSession() || sidErr == nil
	// A deleted Git task can reuse its logical name. Its archived memory session
	// is not proof that a newly created provider conversation already has history.
	resume := !req.Fresh && hasHistory
	store := projectmemory.Store{Home: t.Home}
	if resume {
		if known {
			if prior.Mode != mode || prior.Provider != v.Provider || prior.Pinned != v.Pinned || (prior.RecallID != "") != req.Config.Memory.Recall {
				return v, errors.New("memory mode/provider changed; use --fresh or a new task")
			}
			v = prior
		} else if mode != "native" {
			return v, errors.New("existing conversation has untracked memory; use --fresh or a new task")
		}
	}
	if !resume && req.Config.Memory.Recall {
		v.RecallID = projectmemory.ID()
	}
	if mode == "managed" {
		id, e := store.Resolve(req.Config.Memory.Project, t.WS.Root)
		if e != nil {
			return v, e
		}
		q, err := memorySelection(req)
		if err != nil {
			return v, err
		}
		if resume && known {
			if v.Snapshot.Project != id || v.Pinned == "" && projectmemory.Digest(v.Snapshot.Selection) != projectmemory.Digest(q) {
				return v, errors.New("memory project/selectors changed; use --fresh or a new task")
			}
			if e = store.Queries().ValidateSnapshotFacts(ctx, *v.Snapshot, q.Facts); e != nil {
				return v, e
			}
		} else {
			snap, e := selectMemoryContext(ctx, req, store, id)
			if e != nil {
				return v, e
			}
			if e = store.Pin(snap); e != nil {
				return v, e
			}
			v.Snapshot = &snap
		}
	}
	if v.RecallID != "" && !resume {
		ledger, err := memoryLedger(req, v)
		if err != nil {
			return v, err
		}
		if err = ledger.Initialize(ctx); err != nil {
			return v, err
		}
	}
	if resume && v.RecallID != "" {
		if _, err := projectmemory.ReadFile(filepath.Join(dir, "recalls", v.RecallID+".json"), 4<<20); err != nil {
			return v, fmt.Errorf("memory recall history missing or unreadable: %w", err)
		}
		recall, err := memoryRecall(req, v)
		if err != nil {
			return v, err
		}
		if err = recall.ValidateHistory(ctx); err != nil {
			return v, fmt.Errorf("memory recalls: %w", err)
		}
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return v, e
	}
	// Attempts are execution-owned random IDs; never allow a path supplied by task JSON.
	if req.MemoryAttemptID != "" {
		if !projectmemory.ValidID(req.MemoryAttemptID) {
			return v, errors.New("invalid memory attempt identity")
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		name := filepath.Join(dir, req.MemoryAttemptID+".json")
		if _, e = os.Lstat(name); e == nil {
			return v, errors.New("attempt memory record already exists")
		} else if !os.IsNotExist(e) {
			return v, e
		}
		if e = fsutil.AtomicWrite(name, b, 0600); e != nil {
			return v, e
		}
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if e = fsutil.AtomicWrite(filepath.Join(dir, "session.json"), b, 0600); e != nil {
		return v, e
	}
	context := ""
	if v.Snapshot != nil {
		context = v.Snapshot.Context + "\nEnd of project memory references.\n\n"
	}
	if !active {
		if e = fsutil.AtomicWrite(filepath.Join(t.GenDir(), "memory-context.md"), []byte(context), 0600); e != nil {
			return v, e
		}
		if e = fsutil.AtomicWrite(filepath.Join(t.GenDir(), "memory-prompt.txt"), []byte(context+req.Prompt), 0600); e != nil {
			return v, e
		}
	}
	return v, nil
}
