package execution

import (
	"context"
	"encoding/json"
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
	if err := d.checkExecutionHome(ctx, req); err != nil {
		return err
	}
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
	// Provider ownership belongs to the logical Task, even if its container was lost.
	meta, ok, err := t.ReadMeta()
	if err != nil {
		return err
	}
	if ok {
		if exists && meta.RuntimeRetired {
			return fmt.Errorf("retired runtime still exists; clean before reuse")
		}
		if err := checkManagedRuntime(req, meta); err != nil {
			return err
		}
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
	if err := validateRequest(req); err != nil {
		return err
	}
	return d.Provisioner.Create(ctx, req)
}

func (d *RuntimeDriver) Resume(ctx context.Context, req Request, running bool) error {
	if err := validateRequest(req); err != nil {
		return err
	}
	t := req.Task
	meta, ok, err := t.ReadMeta()
	if err != nil {
		return err
	}
	if ok {
		if meta.RuntimeRetired {
			return fmt.Errorf("retired runtime cannot resume")
		}
		if err := checkManagedRuntime(req, meta); err != nil {
			return err
		}
	}
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

// Do not reactivate a runtime whose existing proxy, forwards or mounted state
// were created under another policy. Continue does not start a runtime and is
// independently authorized against current grants, including query-only repair.
func checkManagedRuntime(req Request, meta task.Meta) error {
	if !meta.RuntimeRetired && req.OrganizationPolicyDigest != "" && req.OrganizationPolicyDigest != meta.OrganizationPolicyDigest {
		return &ExecutionError{Kind: AdmissionRejected, Stage: "organization_runtime", Cause: fmt.Errorf("existing runtime belongs to another or unmanaged policy; reconcile retained facts before creating a new runtime/task")}
	}
	return nil
}

// Check before appending a new attempt so a policy refusal cannot hide a prior
// retained artifact from continuation. Admit repeats it under execution locks.
func (d *RuntimeDriver) ValidateRuntime(req Request) error {
	if req.OrganizationPolicyDigest == "" && req.Task.ExecutionDir == "" {
		return nil
	}
	if err := d.checkExecutionHome(d.dockerContext(), req); err != nil {
		return err
	}
	meta, ok, err := req.Task.ReadMeta()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if meta.RuntimeRetired {
		_, exists, err := d.Docker.Inspect(req.Task.Container())
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("retired runtime still exists; clean before reuse")
		}
	}
	return checkManagedRuntime(req, meta)
}

func (d *RuntimeDriver) checkExecutionHome(ctx context.Context, req Request) error {
	if req.Task.ExecutionDir == "" || d.Docker == nil {
		return nil
	}
	_, exists, err := d.Docker.WithContext(ctx).Inspect(req.Task.Container())
	if err != nil {
		return &ExecutionError{Kind: errorKind(err, EnvironmentFailed), Stage: "runtime_identity", Cause: err}
	}
	if !exists {
		return nil
	}
	raw, err := d.Docker.WithContext(ctx).Run("container", "inspect", "--format", "{{json .Config.Labels}}", req.Task.Container())
	if err != nil {
		return err
	}
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return err
	}
	if labels["sbx.execution-home"] != req.Task.ExecutionHomeKey() {
		return fmt.Errorf("runtime belongs to another home or lacks a home identity; preserve records and inspect original runtime before reuse")
	}
	return nil
}

func (d *RuntimeDriver) dockerContext() context.Context {
	if d.Docker == nil {
		return context.Background()
	}
	return d.Docker.Context()
}
