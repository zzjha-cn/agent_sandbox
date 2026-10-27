package execution

import (
	"context"
	"fmt"
	"sandx/internal/agent"
	"sandx/internal/docker"
	"sandx/internal/isolation"
	"sandx/internal/task"
	"time"
)

// RuntimeDriver owns runtime observations and admission policy. The CLI supplies
// adapters and presents Result.Next; it does not decide execution ordering.
type RuntimeDriver struct {
	Isolation        *isolation.Guard
	Docker           *docker.Client
	Provisioner      *Provisioner
	CheckConcurrency func(task.Task) error
	WarnMemoryBudget func()
	Logf             func(string, ...any)
	PollInterval     time.Duration
	state            docker.State
}

func (d *RuntimeDriver) Inspect(ctx context.Context, req Request) (bool, bool, error) {
	st, exists, err := d.Docker.WithContext(ctx).Inspect(req.Task.Container())
	d.state = st
	return exists, st.Running, err
}

func (d *RuntimeDriver) Admit(ctx context.Context, req Request, exists, running bool) error {
	t := req.Task
	if err := isolation.CheckBoundary(req.Config.Isolation); err != nil {
		return err
	}
	if d.Isolation != nil {
		scope, err := d.Isolation.Status(ctx)
		if err != nil {
			if !isolation.IsRejection(err) {
				return &ExecutionError{Kind: EnvironmentFailed, Stage: "checking daemon identity", Cause: err}
			}
			return err
		}
		if err := checkTaskScope(t, scope); err != nil {
			return err
		}
	}
	if _, err := agent.ResolveProvider(req.Config.DefaultAgent); err != nil {
		return err
	}
	if exists {
		meta, ok, err := t.ReadMeta()
		if err != nil {
			return err
		}
		if ok {
			previous := meta.Agent
			if previous == "" {
				previous = "claude"
			}
			current := req.Config.DefaultAgent
			if current == "" {
				current = "claude"
			}
			if previous != current {
				return fmt.Errorf("task %s uses %s; cannot resume with %s (create a new task)", t.Name, previous, current)
			}
		}
	}
	if req.Mode == Headless && exists && running {
		return fmt.Errorf("task %s already has a running container; stop it before submitting another prompt", t.Name)
	}
	if !exists || !running {
		if err := d.CheckConcurrency(t); err != nil {
			return err
		}
		d.WarnMemoryBudget()
	}
	return nil
}

func checkTaskScope(t task.Task, scope isolation.Status) error {
	meta, ok, err := t.ReadMeta()
	if err != nil {
		return err
	}
	if ok && ((meta.Namespace != "" && meta.Namespace != scope.Namespace) || (meta.Owner != "" && meta.Owner != scope.Owner)) {
		return fmt.Errorf("task ownership does not match the selected daemon/operator; restore the original daemon/owner before resuming")
	}
	return nil
}

func (d *RuntimeDriver) Create(ctx context.Context, req Request) error {
	return d.Provisioner.Create(ctx, req)
}

func (d *RuntimeDriver) Resume(ctx context.Context, req Request, running bool) error {
	t := req.Task
	if req.Base != "" {
		d.Logf("task %s already exists; ignoring --base", t.Name)
	}
	return d.Provisioner.Resume(ctx, req, d.state)
}

func (d *RuntimeDriver) Wait(ctx context.Context, req Request) (AgentOutcome, error) {
	return WaitAgent(ctx, d.Docker.WithContext(ctx), req.Task, d.PollInterval)
}

func (d *RuntimeDriver) Stop(ctx context.Context, req Request) error {
	client := d.Docker.WithContext(ctx)
	st, exists, err := client.Inspect(req.Task.Container())
	if err != nil || !exists || !st.Running {
		return err
	}
	return client.Stop(req.Task.Container())
}
