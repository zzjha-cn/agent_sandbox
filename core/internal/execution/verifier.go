package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"sandx/internal/artifact"
	"sandx/internal/completion"
	"sandx/internal/docker"
	"sandx/internal/isolation"
)

// DockerTestRunner deliberately omits agent identity, credentials, shared
// volumes and network access. Tests can modify only a disposable snapshot copy.
type DockerTestRunner struct {
	Docker    *docker.Client
	Isolation *isolation.Guard
	TempRoot  string
	Limits    artifact.Limits
	Authorize func(completion.Rule) error
}

func (r DockerTestRunner) Test(ctx context.Context, dir string, rule completion.Rule) (code int, err error) {
	if r.Docker == nil || r.Isolation == nil || !filepath.IsAbs(r.TempRoot) {
		return 0, errors.New("verifier runtime is not configured")
	}
	if r.Authorize != nil {
		if err := r.Authorize(rule); err != nil {
			return 0, err
		}
	}
	scope, err := r.Isolation.Status(ctx)
	if err != nil {
		return 0, err
	}
	id := contentBinding(struct {
		Path string
		Rule completion.Rule
	}{dir, rule})[:32]
	name := "sbx-verify-" + id
	d := r.Docker.WithContext(ctx)
	d.ResourceLabels = scope.Labels()
	_, exists, err := d.Inspect(name)
	if err != nil {
		return 0, err
	}
	if exists {
		out, err := d.Run("inspect", "--format", "{{json .Config.Labels}}", name)
		if err != nil {
			return 0, err
		}
		var labels map[string]string
		if err := json.Unmarshal([]byte(out), &labels); err != nil {
			return 0, err
		}
		for k, v := range scope.Labels() {
			if labels[k] != v {
				return 0, errors.New("refusing to remove a foreign verifier")
			}
		}
		if labels["sbx.verification"] != id {
			return 0, errors.New("verifier identity mismatch")
		}
		if err := d.Rm(name); err != nil {
			return 0, err
		}
	}
	copyDir := filepath.Join(r.TempRoot, id)
	if info, err := os.Lstat(copyDir); err == nil {
		if !info.IsDir() {
			return 0, errors.New("invalid retained verifier workspace")
		}
		if err := os.RemoveAll(copyDir); err != nil {
			return 0, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if _, err := artifact.Copy(ctx, dir, copyDir, []string{"."}, nil, r.Limits); err != nil {
		return 0, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
		defer cancel()
		cleanup := r.Docker.WithContext(cleanupCtx)
		_, exists, inspectErr := cleanup.Inspect(name)
		if inspectErr != nil {
			err = errors.Join(err, inspectErr)
			return
		}
		if exists {
			if cleanupErr := cleanup.Rm(name); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
				return
			}
		}
		err = errors.Join(err, os.RemoveAll(copyDir))
	}()
	args := []string{"run", "--pull=never", "--name", name, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--cpus", "1", "--memory", "512m", "--memory-swap", "512m", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "--tmpfs", "/tmp:rw,nosuid,nodev,size=67108864", "--mount", "type=bind,source=" + copyDir + ",target=/workspace", "--workdir", "/workspace"}
	for k, v := range scope.Labels() {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, "--label", "sbx.role=verifier", "--label", "sbx.verification="+id, rule.Image)
	args = append(args, rule.Command...)
	bin := r.Docker.Bin
	if r.Authorize != nil {
		if err := r.Authorize(rule); err != nil {
			return 0, err
		}
	}
	if bin == "" {
		bin = "docker"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	start := time.Now()
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			return 0, runErr
		}
	}
	st, exists, inspectErr := d.Inspect(name)
	if inspectErr != nil {
		return 0, inspectErr
	}
	if !exists || st.Running {
		return 0, errors.New("verifier did not produce a terminal runtime state")
	}
	if st.OOMKilled {
		return 0, errors.New("verifier exceeded memory limit")
	}
	if st.Status != "exited" {
		return 0, fmt.Errorf("verifier did not exit (elapsed %s)", time.Since(start).Round(time.Second))
	}
	return st.ExitCode, nil
}
