package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"sandx/internal/agent"
	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/execution"
	"sandx/internal/isolation"
	"sandx/internal/prep"
	"sandx/internal/protocol"
	"sandx/internal/proxy"
	"sandx/internal/source"
	"sandx/internal/task"
)

// runOpts 是一次 sbx run 的选项。
type runOpts struct {
	Base   string
	Detach bool
	Fresh  bool
	Prompt string // 非空 = headless（M3-7）
	JSONL  bool
}

// headless 报告这次是不是 headless 运行。
func (o runOpts) headless() bool { return o.Prompt != "" }

func (a *App) runCmd() *cobra.Command {
	var base, netMode, proxyMode, prompt, envelopePath, agentName string
	var detach, fresh, cloudMCP, jsonl bool
	cmd := &cobra.Command{
		Use:   "run [task]",
		Short: "Create or resume a task and start the agent in its sandbox (omit task for main)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("spec") && cmd.Flags().Changed("envelope") {
				return a.writeProtocolFailure(&protocol.CodedError{Code: "invalid_task", Err: errors.New("choose --spec only")})
			}
			if (cmd.Flags().Changed("spec") || cmd.Flags().Changed("envelope")) && envelopePath == "" {
				return a.writeProtocolFailure(&protocol.CodedError{Code: "invalid_task", Err: errors.New("--spec must name a file or -")})
			}
			if envelopePath != "" {
				for _, flag := range []string{"prompt", "base", "fresh", "jsonl", "detach", "net", "proxy", "cloud-mcp", "agent"} {
					if cmd.Flags().Changed(flag) {
						return a.writeProtocolFailure(&protocol.CodedError{Code: "invalid_task", Err: fmt.Errorf("--spec cannot be combined with --%s", flag)})
					}
				}
				if len(args) != 0 {
					return a.writeProtocolFailure(&protocol.CodedError{Code: "invalid_task", Err: errors.New("--spec accepts no positional task")})
				}
				spec, err := readEnvelope(cmd, envelopePath)
				if err != nil {
					return a.writeProtocolFailure(err)
				}
				return a.submitTask(cmd.Context(), spec, "run", "")
			}
			if err := cobra.MaximumNArgs(1)(cmd, args); err != nil {
				return err
			}
			if cmd.Flags().Changed("agent") && agentName != "claude" && agentName != "codex" {
				return fmt.Errorf("--agent must be claude or codex")
			}
			if err := a.load(); err != nil {
				return err
			}
			if cmd.Flags().Changed("agent") {
				a.setRunAgent(agentName)
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			t, err := a.task(name)
			if err != nil {
				return err
			}
			if a.Verbose {
				a.logf("effective config:\n%s", a.Cfg)
			}
			if netMode != "" {
				a.Cfg.Network.Mode = netMode
			}
			if proxyMode != "" {
				a.Cfg.Network.Proxy = proxyMode
			}
			if netMode != "" || proxyMode != "" {
				if err := a.Cfg.Validate(); err != nil {
					return err
				}
			}
			// --cloud-mcp 只能打开，不能关：关掉是默认值，想关就别加这个 flag（ADR 0015）
			if cloudMCP {
				a.Cfg.Network.CloudMCP = true
			}
			// design §10.1 第 3 步：配置合并之后、碰容器之前先过信任检查
			if err := a.requireTrust(); err != nil {
				return err
			}
			o := runOpts{Base: base, Detach: detach, Fresh: fresh, JSONL: jsonl}
			if cmd.Flags().Changed("prompt") {
				if o.Prompt, err = readPrompt(prompt, cmd.InOrStdin()); err != nil {
					return err
				}
			}
			runCtx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return a.run(runCtx, t, o)
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent for this run: claude or codex (overrides default_agent without changing config files)")
	cmd.Flags().StringVar(&base, "base", "", "starting point for the new branch (default: current HEAD)")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "start the task without attaching to it")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "start a new conversation instead of continuing this task's last one")
	cmd.Flags().StringVar(&netMode, "net", "", "network mode for this run: open (default, everything allowed) or allowlist")
	cmd.Flags().StringVar(&proxyMode, "proxy", "", "egress proxy for this task: shared (default, one squid for everyone) or dedicated (its own sidecar)")
	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "headless: run this prompt and exit instead of opening the session (\"-\" reads stdin)")
	cmd.Flags().BoolVar(&cloudMCP, "cloud-mcp", false, "let this task reach Claude's cloud connectors (off by default; see ADR 0015)")
	cmd.Flags().BoolVar(&jsonl, "jsonl", false, "write machine-readable execution events and result to stdout (headless only)")
	cmd.Flags().StringVar(&envelopePath, "envelope", "", "alias for --spec; accepts v2 tasks only")
	cmd.Flags().StringVar(&envelopePath, "spec", "", "run a v2 task from a file or - for stdin")
	return cmd
}

// Keep the effective config and later prep merges aligned. This changes only
// in-memory configuration; no configuration layer is written to disk.
func (a *App) setRunAgent(name string) {
	a.Cfg.DefaultAgent = name
	a.Loaded.Config.DefaultAgent = name
}

// readPrompt 取 -p 的值；"-" 表示从 stdin 读（喂长 prompt 用）。
func readPrompt(v string, stdin io.Reader) (string, error) {
	if v == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		v = string(b)
	}
	if strings.TrimSpace(v) == "" {
		return "", errors.New("-p is empty; drop -p entirely for interactive mode")
	}
	return v, nil
}

func (a *App) run(ctx context.Context, t task.Task, o runOpts) error {
	if o.JSONL && !o.headless() {
		return fmt.Errorf("--jsonl requires --prompt")
	}
	if err := a.constrainOrganization(); err != nil {
		return err
	}
	initial := a.Organization
	req := execution.Request{Task: t, Mode: execution.Interactive, Prompt: o.Prompt, Base: o.Base, Fresh: o.Fresh, Detach: o.Detach, Config: a.Cfg}
	if o.headless() {
		req.Mode = execution.Headless
	}

	req.Authorize = func(r execution.Request) error { return a.checkOrganization(initial, r) }
	if err := req.Authorize(req); err != nil {
		return &execution.ExecutionError{Kind: execution.AdmissionRejected, Stage: "organization_policy", Cause: err}
	}
	ctx, stopOrganization := a.organizationContext(ctx)
	defer stopOrganization()
	runApp := *a
	if o.JSONL {
		runApp.Out = a.Err
	} // stdout is reserved for structured records
	runApp.Docker = a.Docker.WithContext(ctx)
	defer func() {
		a.Cfg, a.Loaded = runApp.Cfg, runApp.Loaded
	}()
	driver := runApp.executionDriver()
	var service *execution.Service
	if o.JSONL {
		service = execution.NewService(driver, a.teamSink(execution.NewJSONLSink(a.Out), t.WS.ID))
	} else {
		service = execution.NewService(driver, a.teamSink(execution.NopEventSink{}, t.WS.ID))
	}
	result, err := service.Run(ctx, req)
	if err != nil {
		return err
	}
	switch result.Next {
	case execution.Completed:
		if !o.JSONL {
			if err := runApp.showHeadlessResult(t, result.AgentExitCode); err != nil {
				return err
			}
		}
		if result.State == execution.AgentFailed && result.AgentExitCode != nil {
			if o.JSONL {
				return &execution.ExecutionError{Kind: execution.AgentRunFailed, Stage: "agent exited", Cause: fmt.Errorf("agent exited with %d", *result.AgentExitCode)}
			}
			return fmt.Errorf("agent exited with %d (full output: sbx logs %s)", *result.AgentExitCode, t.Name)
		}
		return nil
	case execution.Detached:
		if o.JSONL {
			return nil
		}
		if o.headless() {
			fmt.Fprintf(a.Out, "task %s is running the prompt in the background. Follow it with: sbx logs -f %s\n", t.Name, t.Name)
		} else {
			fmt.Fprintf(a.Out, "task %s is already running in the background. Enter it with: sbx attach %s\n", t.Name, t.Name)
		}
		return nil
	case execution.Attach:
		if err := runApp.attach(t); err != nil {
			return contextExecutionError(ctx, err)
		}
		return nil
	default:
		return fmt.Errorf("execution returned invalid next step %q", result.Next)
	}
}

func contextExecutionError(ctx context.Context, err error) error {
	switch ctx.Err() {
	case context.Canceled:
		return &execution.ExecutionError{Kind: execution.CancelledKind, Stage: "run cancelled", Cause: err}
	case context.DeadlineExceeded:
		return &execution.ExecutionError{Kind: execution.TimedOutKind, Stage: "run timed out", Cause: err}
	default:
		return err
	}
}

// executionDriver is the composition root for the execution adapters.
func (a *App) executionDriver() *execution.RuntimeDriver {
	guard := isolation.New(a.Docker, a.Cfg.Isolation)
	provisioner := execution.NewProvisioner(runHost{app: a})
	provisioner.Isolation = guard
	return &execution.RuntimeDriver{Docker: a.Docker, Isolation: guard, Provisioner: provisioner,
		CheckConcurrency: a.checkConcurrency, WarnMemoryBudget: a.warnMemoryBudget, Logf: a.logf, PollInterval: followTick}
}

type runHost struct{ app *App }

func (h runHost) DockerClient() *docker.Client { return h.app.Docker }
func (h runHost) DockerClientWithContext(ctx context.Context) *docker.Client {
	return h.app.Docker.WithContext(ctx)
}
func (h runHost) EffectiveConfig() config.Config  { return h.app.Cfg }
func (h runHost) Logf(format string, args ...any) { h.app.logf(format, args...) }
func (h runHost) PrepareWorktree(ctx context.Context, t task.Task, base string) (string, error) {
	if t.ExecutionDir != "" {
		info, err := os.Stat(t.Worktree())
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("prepared execution workspace is not a directory")
		}
		return "", nil
	}
	return h.app.prepareWorktree(ctx, t, base)
}
func (h runHost) AgentImage() (string, error) { return h.app.agentImage() }
func (h runHost) PrepLoad(t task.Task) (string, *prep.Plan, error) {
	if t.ExecutionDir != "" {
		return "", nil, nil
	}
	return h.app.prepLoad(t)
}
func (h runHost) MergePrepAllow(allow []string) error {
	a := h.app
	if err := a.Loaded.MergePrep(allow, nil); err != nil {
		return err
	}
	a.Cfg = a.Loaded.Config
	return a.constrainOrganization()
}
func (h runHost) EgressOf(t task.Task, mode string) proxy.Egress { return h.app.egressOf(t, mode) }
func (h runHost) Egress(t task.Task) proxy.Egress                { return h.app.egress(t) }
func (h runHost) EgressWithContext(ctx context.Context, t task.Task, mode string) proxy.Egress {
	app := *h.app
	app.Docker = h.app.Docker.WithContext(ctx)
	return app.egressOf(t, mode)
}
func (h runHost) ProxySpec(t task.Task, taskID string) proxy.TaskSpec {
	return h.app.proxySpec(t, taskID)
}
func (h runHost) WarnSkippedNotifyHosts() {
	a := h.app
	if _, skipped := proxy.HostsIn(a.Cfg.OnIdle, a.Cfg.OnExit); len(skipped) > 0 {
		a.logf("note: %s in the notify command contains a variable or is an IP and cannot be allowed automatically; run sbx net allow <host> if you need it", strings.Join(skipped, "、"))
	}
}
func (h runHost) EnsureServices(t task.Task, u *execution.Undo) error {
	return h.app.ensureServices(t, u)
}
func (h runHost) EnsureForwards(t task.Task, u *execution.Undo, create bool) ([]string, []task.Link, error) {
	return h.app.ensureForwards(t, u, create)
}
func (h runHost) EnsureVolumes(t task.Task, tag string, u *execution.Undo) (map[string]bool, error) {
	return h.app.ensureVolumes(t, tag, u)
}
func (h runHost) PrepSeed(t task.Task, tag string, plan *prep.Plan, fresh map[string]bool) error {
	return h.app.prepSeed(t, tag, plan, fresh)
}
func (h runHost) HostClaude() agent.HostClaude { return h.app.hostClaude() }
func (h runHost) GenInput(host agent.HostClaude, req execution.Request) agent.GenInput {
	return h.app.genInput(host, req)
}
func (h runHost) APIKey() (execution.APIKey, error) {
	key, err := h.app.apiKey("")
	return execution.APIKey{Env: key.Env, Value: key.Value, Source: key.Source}, err
}
func (h runHost) InstallCAs(t task.Task, tag string, cas []string) error {
	return h.app.installCAs(t, tag, cas)
}
func (h runHost) StartAgent(ctx context.Context, t task.Task, req execution.Request) error {
	return h.app.startAgent(ctx, t, req)
}
func (h runHost) WarnForwardExposure(links []task.Link) { h.app.warnForwardExposure(links) }

// genInput 把生效配置和本次运行的参数汇成 gen 目录的输入。
func (a *App) genInput(host agent.HostClaude, req execution.Request) agent.GenInput {
	return agent.GenInput{
		Host:          host,
		BlockCloudMCP: !a.Cfg.Network.CloudMCP,
		OnIdle:        a.Cfg.OnIdle,
		OnExit:        a.Cfg.OnExit,
		Throttle:      a.Cfg.NotifyThrottle,
		Prompt:        req.Prompt,
	}
}

func (a *App) hostClaude() agent.HostClaude {
	h := agent.InspectHostClaude(hostClaudeDir())
	for _, w := range h.Warnings {
		a.logf("warning: %s", w)
	}
	return h
}

func (a *App) proxySpec(t task.Task, taskID string) proxy.TaskSpec {
	block := proxy.BuiltinList("policy-block")
	if a.Cfg.Network.CloudMCP {
		block = nil
	}
	// 通知域名：on_idle/on_exit 里的 webhook 主机自动放行（design §6.3）。
	// 无条件加，open 模式下也加——模式随时可能切回 allowlist。
	notify, _ := proxy.HostsIn(a.Cfg.OnIdle, a.Cfg.OnExit)
	allow := proxy.RenderAllow(block, proxy.BuiltinList("builtin"), proxy.BuiltinList(a.Cfg.Profile), a.Cfg.Network.Allow, notify)
	if a.Organization.Managed {
		allow = proxy.RenderAllow(block, a.Cfg.Network.Allow)
	}
	return proxy.TaskSpec{
		TaskID:   taskID,
		Network:  t.Network(),
		CredFile: filepath.Join(t.StateDir(), "proxy.cred"),
		Allow:    allow,
		Block:    block,
		Open:     a.Cfg.Network.Mode == "open",
	}
}

func (a *App) prepareWorktree(ctx context.Context, t task.Task, base string) (string, error) {
	return source.PrepareLegacy(ctx, t, base, a.logf)
}

// initVol 在一个临时容器里以 root 初始化 volume 的内容和属主。
func (a *App) initVol(tag, vol, script string) error {
	_, err := a.Docker.Run("run", "--rm", "-u", "0", "--network", "none",
		"--mount", "type=volume,source="+vol+",target=/v", "--entrypoint", "sh", tag, "-c", script)
	return err
}

// ensureSharedVolumes 创建全局共享的 sbx-home 和 sbx-cache，新建时按宿主机的 UID/GID 设属主。
// sbx run 和 sbx login 都要先过这一步。
func (a *App) ensureSharedVolumes(tag string, fresh map[string]bool) error {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	scripts := map[string]string{
		sharedHome:  "chown " + uid + ":" + gid + " /v",
		"sbx-cache": "mkdir -p /v/npm /v/pip /v/uv /v/go-mod /v/go-build /v/cargo && chown -R " + uid + ":" + gid + " /v",
		"sbx-mise":  "chown " + uid + ":" + gid + " /v",
	}
	for _, vol := range []string{sharedHome, "sbx-cache", "sbx-mise"} {
		classes := map[string]string{sharedHome: "identity", "sbx-cache": "cache", "sbx-mise": "runtimes"}
		created, err := a.Docker.VolumeCreate(vol, map[string]string{"sbx.kind": "shared", "sbx.class": classes[vol], "sbx.retention": "manual"})
		if err != nil {
			return err
		}
		if created {
			fresh[vol] = true
			if err := a.initVol(tag, vol, scripts[vol]); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureVolumes 准备共享 volume 和这个 Task 的依赖 volume，
// 返回本次**新建**的 volume 名集合——prep 只播种它们，不覆盖已有内容（M4-8）。
func (a *App) ensureVolumes(t task.Task, tag string, u *execution.Undo) (map[string]bool, error) {
	fresh := map[string]bool{}
	if err := a.ensureSharedVolumes(tag, fresh); err != nil {
		return nil, err
	}
	own := "chown " + strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()) + " /v"
	for i := range a.Cfg.Deps.Mask {
		vol := t.DepVolume(i)
		labels := t.Labels()
		labels["sbx.kind"] = "dep"
		labels["sbx.class"] = "dependencies"
		labels["sbx.retention"] = "task"
		created, err := execution.CreateOwnedVolume(a.Docker, vol, labels, u)
		if err != nil {
			return nil, err
		}
		if created {
			fresh[vol] = true
			if err := a.initVol(tag, vol, own); err != nil {
				return nil, err
			}
		}
	}
	return fresh, nil
}

// startAgent wires infrastructure adapters; execution owns launch policy.
func (a *App) startAgent(ctx context.Context, t task.Task, req execution.Request) error {
	starter := execution.AgentStarter{
		Docker: a.Docker, APIKey: runHost{app: a}.APIKey,
		ImportMemory: a.importMemory, InstallRuntimes: a.installRuntimes, Logf: a.logf,
		RotateLog: func(path string) error { return rotateRunLog(path, runLogLimit) },
	}
	return starter.Start(ctx, t, req)
}

// installRuntimes 按项目里的版本文件装运行时（M3-3）。装不上只警告不拦：
// 项目可能声明了一个 mise 装不了的运行时，但别的活照样能干。
func (a *App) installRuntimes(t task.Task, rt agent.Runtime) {
	files := agent.VersionFiles(t.Worktree())
	if len(files) == 0 {
		return
	}
	a.logf("installing runtimes from %s (mise; already-installed ones are reused) ...", strings.Join(files, ", "))
	if out, err := rt.MiseInstall(); err != nil {
		a.logf("warning: mise install did not succeed, continuing anyway: %v\n%s", err, lastLines(out, 10))
		return
	}
	if sum := rt.MiseSummary(); sum != "" {
		a.logf("runtimes: %s", sum)
	}
}

// lastLines 取输出的最后 n 行，错误信息不要刷屏。
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// agentExited reports the provider status marker for an ended session.
func agentExited(t task.Task) bool {
	s, err := t.ReadStatus()
	return err == nil && s != nil && s.State == "exited"
}

// showHeadlessResult displays the completed headless output and summary.
func (a *App) showHeadlessResult(t task.Task, code *int) error {
	if err := a.showLogs(t, false, 0); err != nil {
		return err
	}
	exitCode := 0
	if code != nil {
		exitCode = *code
	}
	fmt.Fprintf(a.Out, "\ntask %s finished (exit=%d) and its container is stopped.\n", t.Name, exitCode)
	fmt.Fprintf(a.Out, "see changes: cd $(sbx path %s) | continue: sbx run %s | finish: sbx done %s\n", t.Name, t.Name, t.Name)
	return nil
}
