package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sandx/internal/agent"
	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/isolation"
	"sandx/internal/prep"
	"sandx/internal/proxy"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

// Undo records resources created by one attempt and returns every cleanup
// failure in reverse creation order.
type CleanupFailure struct {
	Kind     string
	Resource string
	Cause    error
}

func (e CleanupFailure) Error() string {
	return fmt.Sprintf("cleanup %s %s: %v", e.Kind, e.Resource, e.Cause)
}

func (e CleanupFailure) Unwrap() error { return e.Cause }

type undoAction struct {
	kind, resource string
	created        bool
	cleanup        func(context.Context) error
}

type Undo struct{ actions []undoAction }

func (u *Undo) Add(kind, resource string, created bool, cleanup func(context.Context) error) {
	u.actions = append(u.actions, undoAction{kind: kind, resource: resource, created: created, cleanup: cleanup})
}

func (u *Undo) Run(ctx context.Context) []error {
	var failures []error
	for i := len(u.actions) - 1; i >= 0; i-- {
		a := u.actions[i]
		if !a.created {
			continue
		}
		if err := a.cleanup(ctx); err != nil {
			failures = append(failures, CleanupFailure{Kind: a.kind, Resource: a.resource, Cause: err})
		}
	}
	return failures
}

type APIKey struct {
	Env    string
	Value  string
	Source string
}

// ProvisioningHost adapts existing domain operations to the execution
// workflow. It is implemented by the CLI composition root for now; lifecycle
// ordering and ownership remain in Provisioner.
type ProvisioningHost interface {
	DockerClient() *docker.Client
	DockerClientWithContext(context.Context) *docker.Client
	EffectiveConfig() config.Config
	Logf(format string, args ...any)
	PrepareWorktree(context.Context, task.Task, string) (string, error)
	AgentImage() (string, error)
	PrepLoad(task.Task) (string, *prep.Plan, error)
	MergePrepAllow([]string) error
	EgressOf(task.Task, string) proxy.Egress
	Egress(task.Task) proxy.Egress
	EgressWithContext(context.Context, task.Task, string) proxy.Egress
	ProxySpec(task.Task, string) proxy.TaskSpec
	WarnSkippedNotifyHosts()
	EnsureServices(task.Task, *Undo) error
	EnsureForwards(task.Task, *Undo, bool) ([]string, []task.Link, error)
	EnsureVolumes(task.Task, string, *Undo) (map[string]bool, error)
	PrepSeed(task.Task, string, *prep.Plan, map[string]bool) error
	HostClaude() agent.HostClaude
	GenInput(agent.HostClaude, Request) agent.GenInput
	APIKey() (APIKey, error)
	InstallCAs(task.Task, string, []string) error
	StartAgent(context.Context, task.Task, Request) error
	WarnForwardExposure([]task.Link)
}

type Provisioner struct {
	host      ProvisioningHost
	Isolation *isolation.Guard
}

func (p *Provisioner) ensureIsolation(ctx context.Context, t task.Task) error {
	if p.Isolation == nil {
		return nil
	}
	scope, err := p.Isolation.Ensure(ctx)
	if err != nil {
		if isolation.IsRejection(err) {
			return &ExecutionError{Kind: AdmissionRejected, Stage: "daemon isolation", Cause: err}
		}
		return err
	}
	p.host.DockerClient().ResourceLabels = scope.Labels()
	if err := checkTaskScope(t, scope); err != nil {
		return &ExecutionError{Kind: AdmissionRejected, Stage: "task isolation", Cause: err}
	}
	return nil
}

func NewProvisioner(host ProvisioningHost) *Provisioner { return &Provisioner{host: host} }

func (p *Provisioner) Resume(ctx context.Context, req Request, state docker.State) (err error) {
	if err := p.ensureIsolation(ctx, req.Task); err != nil {
		return err
	}
	h, t := p.host, req.Task
	stopOnFailure := false
	defer func() {
		if err == nil || !stopOnFailure {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		stopErr := (&RuntimeDriver{Docker: h.DockerClientWithContext(cleanupCtx)}).Stop(cleanupCtx, req)
		var cleanup []error
		if stopErr != nil {
			cleanup = append(cleanup, stopErr)
		}
		kind := errorKind(err, EnvironmentFailed)
		var original *ExecutionError
		if errors.As(err, &original) {
			kind = original.Kind
			cleanup = append(original.Cleanup, cleanup...)
		}
		err = &ExecutionError{Kind: kind, Stage: "resuming task", Cause: err, Cleanup: cleanup, CleanupAttempted: true}
	}()
	meta, ok, err := t.ReadMeta()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("container %s exists but %s/meta.json is missing; run sbx done %s first, then run again", t.Container(), t.StateDir(), t.Name)
	}
	if len(meta.PrepAllow) > 0 {
		if err := h.MergePrepAllow(meta.PrepAllow); err != nil {
			return err
		}
	}
	egress := h.Egress(t)
	if err := egress.Ensure(); err != nil {
		return err
	}
	if _, err := egress.AttachTask(h.ProxySpec(t, meta.TaskID)); err != nil {
		return err
	}
	if err := h.EnsureServices(t, nil); err != nil {
		return err
	}
	if _, _, err := h.EnsureForwards(t, nil, false); err != nil {
		return err
	}
	h.WarnForwardExposure(meta.Forwards)
	if err := agent.RenderGen(t.GenDir(), h.GenInput(h.HostClaude(), req)); err != nil {
		return err
	}
	stopOnFailure = true
	if !state.Running {
		h.Logf("starting stopped task %s ...", t.Name)
		if err := h.DockerClient().Start(t.Container()); err != nil {
			return err
		}
	}
	if err := h.StartAgent(ctx, t, req); err != nil {
		return &ExecutionError{Kind: errorKind(err, AgentStartFailed), Stage: "starting agent", Cause: err}
	}

	return nil
}

func (p *Provisioner) Create(ctx context.Context, req Request) (err error) {
	if err := p.ensureIsolation(ctx, req.Task); err != nil {
		return err
	}
	h, t := p.host, req.Task
	var undo Undo
	step := "preparing the worktree"
	defer func() {
		if err != nil {
			h.Logf("failed at step [%s]; cleaning up what this run created (the worktree is kept) ...", step)
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			cleanupErrors := undo.Run(cleanupCtx)
			cancel()
			kind := EnvironmentFailed
			var original *ExecutionError
			if errors.As(err, &original) {
				kind = original.Kind
			}
			kind = errorKind(err, kind)
			err = &ExecutionError{
				Kind: kind, Stage: step, Cause: fmt.Errorf("%s: %w", step, err), Cleanup: cleanupErrors, CleanupAttempted: true,
			}
		}
	}()

	baseSHA, err := h.PrepareWorktree(ctx, t, req.Base)
	if err != nil {
		return err
	}
	step = "preparing the image"
	tag, err := h.AgentImage()
	if err != nil {
		return err
	}
	step = "reading the prep image"
	prepTag, prepPlan, err := h.PrepLoad(t)
	if err != nil {
		return err
	}
	// PrepLoad may merge its manifest into the effective config. Keep later
	// resource construction aligned with the pre-refactor CLI order.
	cfg := h.EffectiveConfig()
	if err := os.MkdirAll(t.GenDir(), 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(t.StateDir(), "status.json"))

	step = "preparing the egress proxy"
	egress := h.EgressOf(t, cfg.Network.Proxy)
	if err := egress.Ensure(); err != nil {
		return err
	}
	step = "creating the task network"
	d := h.DockerClient()
	if ok, err := d.NetworkExists(t.Network()); err != nil {
		return err
	} else if !ok {
		labels := t.Labels()
		labels["sbx.kind"] = "net"
		if err := d.NetworkCreate(t.Network(), true, labels); err != nil {
			return err
		}
		undo.Add("network", t.Network(), true, func(cleanupCtx context.Context) error {
			return h.DockerClientWithContext(cleanupCtx).NetworkRm(t.Network())
		})
	}
	step = "attaching to the egress proxy"
	h.WarnSkippedNotifyHosts()
	undo.Add("proxy_attachment", t.ID(), true, func(cleanupCtx context.Context) error {
		cleanupEgress := h.EgressWithContext(cleanupCtx, t, cfg.Network.Proxy)
		detachErr := cleanupEgress.DetachTask(t.ID(), t.Network())
		_, stopErr := cleanupEgress.StopIfIdle()
		return errors.Join(detachErr, stopErr)
	})
	proxyURL, err := egress.AttachTask(h.ProxySpec(t, t.ID()))
	if err != nil {
		return err
	}
	step = "starting the declared services"
	if err := h.EnsureServices(t, &undo); err != nil {
		return err
	}
	step = "opening the declared forwards"
	addHosts, links, err := h.EnsureForwards(t, &undo, true)
	if err != nil {
		return err
	}
	step = "preparing volumes"
	fresh, err := h.EnsureVolumes(t, tag, &undo)
	if err != nil {
		return err
	}
	step = "seeding dependencies from the prep image"
	if err := h.PrepSeed(t, prepTag, prepPlan, fresh); err != nil {
		return err
	}
	step = "rendering generated files"
	hostClaude := h.HostClaude()
	if err := agent.RenderGen(t.GenDir(), h.GenInput(hostClaude, req)); err != nil {
		return err
	}
	step = "creating the container"
	gitDir := t.WS.GitDir
	if t.IsMain() {
		gitDir = ""
	}
	name, _ := workspace.GitContext(ctx, t.WS.Root, "config", "user.name")
	mail, _ := workspace.GitContext(ctx, t.WS.Root, "config", "user.email")
	key, err := h.APIKey()
	if err != nil {
		return err
	}
	if key.Env != "" {
		h.Logf("starting with an API key (from %s), not subscription login", key.Source)
	}
	labels := t.Labels()
	labels["sbx.role"] = "agent"
	labels["sbx.proxy"] = cfg.Network.Proxy
	spec := docker.RunSpec{
		Name: t.Container(), Image: tag, Labels: labels, Network: t.Network(),
		Mounts: agent.Mounts(agent.MountInput{
			Worktree: t.Worktree(), GitDir: gitDir, DepMasks: cfg.Deps.Mask,
			DepVolume: t.DepVolume, StateDir: t.StateDir(), GenDir: t.GenDir(),
			Host: hostClaude, GitCommon: t.WS.GitDir, GitProtect: cfg.Git.Protect,
		}),
		Env: agent.Env(agent.EnvInput{
			ProxyURL: proxyURL, GitName: name, GitMail: mail, WS: t.WS.ID,
			Task: t.Name, TZ: agent.HostTZ(), APIKeyEnv: key.Env,
			APIKey: key.Value, Extra: cfg.Env,
		}),
		Workdir: t.Worktree(), Resources: docker.Resources{CPUs: cfg.Resources.CPUs, Memory: cfg.Resources.Memory, Pids: cfg.Resources.Pids},
		LogMaxSize: "10m", AddHosts: addHosts,
	}
	if err := createOwnedContainer(d, spec, &undo); err != nil {
		return err
	}
	if prepPlan != nil && prepPlan.Manifest != nil {
		step = "installing the CA certificates declared by prep"
		if err := h.InstallCAs(t, prepTag, prepPlan.Manifest.CA); err != nil {
			return err
		}
	}
	step = "writing meta"
	if err := t.WriteMeta(task.Meta{
		Namespace: d.ResourceLabels["sbx.namespace"], Owner: d.ResourceLabels["sbx.owner"],
		Task: t.Name, WS: t.WS.ID, Root: t.WS.Root, Base: baseSHA,
		Agent: cfg.DefaultAgent, Profile: cfg.Profile, Image: tag, Proxy: cfg.Network.Proxy, TaskID: t.ID(),
		DepMasks: cfg.Deps.Mask, PrepTag: prepTag, PrepAllow: prepAllow(prepPlan),
		Services: serviceNames(cfg.Services), Forwards: links, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	undo.Add("meta", filepath.Join(t.StateDir(), "meta.json"), true, func(context.Context) error {
		err := os.Remove(filepath.Join(t.StateDir(), "meta.json"))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
	step = "starting the agent"
	if err := h.StartAgent(ctx, t, req); err != nil {
		return &ExecutionError{Kind: errorKind(err, AgentStartFailed), Stage: "starting agent", Cause: err}
	}
	return nil
}

func prepAllow(p *prep.Plan) []string {
	if p == nil || p.Manifest == nil {
		return nil
	}
	return p.Manifest.Allow
}

func serviceNames(svcs []config.Service) []string {
	var out []string
	for _, svc := range svcs {
		out = append(out, svc.Name)
	}
	return out
}

// Register ownership before issuing docker run: the daemon can create a
// container even when the host CLI is interrupted before returning its ID.
func createOwnedContainer(d *docker.Client, spec docker.RunSpec, undo *Undo) error {
	owner := newAttemptID()
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	spec.Labels["sbx.provision"] = owner
	undo.Add("container", spec.Name, true, func(ctx context.Context) error {
		client := d.WithContext(ctx)
		items, err := client.ListByLabel("container", "sbx.provision="+owner)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Str("Names") == spec.Name {
				return client.Rm(spec.Name)
			}
		}
		return nil
	})
	_, err := d.RunContainer(spec)
	return err
}
