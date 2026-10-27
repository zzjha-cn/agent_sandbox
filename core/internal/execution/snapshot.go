package execution

import (
	"context"
	"path/filepath"
	"sandx/internal/fsutil"
	"sandx/internal/task"
)

func preservePath(path string, u *Undo) error {
	restore, err := fsutil.SnapshotPath(path)
	if err != nil {
		return err
	}
	u.Add("state", path, true, func(context.Context) error { return restore() })
	return nil
}

// Launch markers drive readiness and conversation selection. Logs remain as
// diagnostics of the failed attempt; they are not used to select a session.
func preserveLaunchState(t task.Task, u *Undo) error {
	for _, name := range []string{"status.json", "run.exit", "run.exit.tmp", "codex-session", "codex-session.tmp", "codex-launch"} {
		if err := preservePath(filepath.Join(t.StateDir(), name), u); err != nil {
			return err
		}
	}
	return nil
}
