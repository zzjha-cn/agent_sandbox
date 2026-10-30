package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"sandx/internal/task"
	"sandx/internal/workspace"
)

// PruneArtifacts is an explicit operator operation, independent of grants that
// may already have been revoked. It deletes no input baseline or sink target.
func PruneArtifacts(ctx context.Context, t task.Task, attemptID string) error {
	if err := workspace.ValidateTaskName(attemptID); err != nil {
		return err
	}
	store, err := NewFileStore(t)
	if err != nil {
		return err
	}
	unlock, err := store.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	record, err := store.Load()
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("task has no retained artifacts")
	}
	for _, op := range record.Operations {
		if op.State == "intent" || op.State == "unknown" {
			return ErrRecoveryRequired
		}
	}
	var selected *AttemptRecord
	for i := range record.Attempts {
		if record.Attempts[i].Result.AttemptID == attemptID {
			selected = &record.Attempts[i]
			break
		}
	}
	if selected == nil || selected.Pipeline == nil || selected.Pipeline.Artifact == nil {
		return errors.New("attempt has no artifact")
	}
	facts := selected.Pipeline
	ref := facts.Artifact
	if !selected.Closed || selected.Result.CleanupState == CleanupFailed || selected.Result.CleanupState == CleanupPending || facts.Validation != nil && facts.Validation.State == "needs_review" {
		return ErrRecoveryRequired
	}
	if facts.Sink != nil && selected.Result.ProductDeliveryState != "delivered" {
		return errors.New("delivery must be reconciled before pruning")
	}
	if ref.ID != attemptID {
		return ErrRecordInvalid
	}
	if ref.Retention == "pruned" {
		return nil
	}
	dir := filepath.Join(t.Home, "artifacts", t.WS.ID, t.Name, "snapshots", attemptID)
	if err := controlledDirectories(t.Home, dir); err != nil {
		return err
	}
	ref.Retention = "pruning"
	if err := store.Save(ctx, record); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for _, name := range facts.Quarantines {
		if filepath.Base(name) != name || !strings.HasPrefix(name, attemptID+"-incomplete-") {
			return ErrRecordInvalid
		}
		quarantine := filepath.Join(filepath.Dir(dir), name)
		if err := controlledDirectories(t.Home, quarantine); err != nil {
			return err
		}
		if err := os.RemoveAll(quarantine); err != nil {
			return err
		}
	}
	if err := syncDirectory(filepath.Dir(dir)); err != nil {
		return err
	}
	ref.Retention = "pruned"
	return store.Save(ctx, record)
}

func controlledDirectories(home, dir string) error {
	rel, err := filepath.Rel(home, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrRecordInvalid
	}
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && !info.IsDir() {
			return ErrRecordInvalid
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if current == home {
			break
		}
	}
	return nil
}
