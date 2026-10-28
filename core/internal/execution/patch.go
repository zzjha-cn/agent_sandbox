package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sandx/internal/artifact"
)

type patchWriter struct {
	file *os.File
	left int64
	err  error
}

func (w *patchWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.left {
		w.err = errors.New("patch exceeds artifact limit")
		return 0, w.err
	}
	n, err := w.file.Write(b)
	w.left -= int64(n)
	return n, err
}

// collectPatch uses Git's binary patch encoder against two private copies.
// External diffs/textconv are disabled; no project command runs on the host.
func (s *Service) collectPatch(ctx context.Context, req Request, root string) error {
	baseline := filepath.Join(root, "baseline")
	if _, err := artifact.Copy(ctx, filepath.Join(pipelineRoot(req), "inputs", s.facts.InputSnapshot), baseline, []string{"."}, req.Pipeline.Policy.Exclude, req.Pipeline.Policy.InputLimits); err != nil {
		return err
	}
	out := filepath.Join(root, "outputs", "changes.patch")
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "diff", "--no-index", "--no-prefix", "--binary", "--no-ext-diff", "--no-textconv", "--", "baseline", "workspace")
	cmd.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	w := &patchWriter{file: f, left: req.Pipeline.Policy.ArtifactLimits.FileBytes}
	cmd.Stdout = w
	cmd.Stderr = io.Discard
	err = cmd.Run()
	if w.err != nil {
		return w.err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return err
		}
	}
	return errors.Join(f.Sync(), f.Close(), syncDirectory(filepath.Dir(out)))
}
