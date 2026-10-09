package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sandx/internal/agent"
	"sandx/internal/docker"
	"sandx/internal/proxy"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

const smokeTimeout = 30 * time.Second

// runOpts 是一次 sbx run 的选项。
type runOpts struct {
	Base   string
	Detach bool
	Fresh  bool
	Prompt string // 非空 = headless（M3-7）
}

// headless 报告这次是不是 headless 运行。
func (o runOpts) headless() bool { return o.Prompt != "" }

func (a *App) runCmd() *cobra.Command {
	var base, netMode, proxyMode, prompt string
	var detach, fresh, cloudMCP bool
	cmd := &cobra.Command{
		Use:   "run [task]",
		Short: "Create or resume a task and start the agent in its sandbox (omit task for main)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
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
			o := runOpts{Base: base, Detach: detach, Fresh: fresh}
			if cmd.Flags().Changed("prompt") {
				if o.Prompt, err = readPrompt(prompt, cmd.InOrStdin()); err != nil {
					return err
				}
			}
			return a.run(t, o)
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "starting point for the new branch (default: current HEAD)")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "start the task without attaching to it")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "start a new conversation instead of continuing this task's last one")
	cmd.Flags().StringVar(&netMode, "net", "", "network mode for this run: open (default, everything allowed) or allowlist")
	cmd.Flags().StringVar(&proxyMode, "proxy", "", "egress proxy for this task: shared (default, one squid for everyone) or dedicated (its own sidecar)")
	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "headless: run this prompt and exit instead of opening the session (\"-\" reads stdin)")
	cmd.Flags().BoolVar(&cloudMCP, "cloud-mcp", false, "let this task reach Claude's cloud connectors (off by default; see ADR 0015)")
	return cmd
}

// undo 记录本次新建的资源，失败时逆序清理。
type undo []func()

func (u *undo) add(f func()) { *u = append(*u, f) }
func (u undo) run() {
	for i := len(u) - 1; i >= 0; i-- {
		u[i]()
	}
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

func (a *App) run(t task.Task, o runOpts) error {
	st, exists, err := a.Docker.Inspect(t.Container())
	if err != nil {
		return err
	}
	// design §10.1 第 4 步：-p 不能插队到一个活着的会话里，两个 claude 会抢同一棵 worktree
	if o.headless() && exists && st.Running {
		s, _ := t.ReadStatus()
		if d := task.Derive(st, exists, s, t.RunExit()); d == "running" || d == "idle" || d == "starting" {
			return fmt.Errorf("task %s already has a session running (%s). Look at it with sbx attach %s, or sbx stop %s before using -p",
				t.Name, d, t.Name, t.Name)
		}
	}
	// 只有真要启动一个容器时才查并发和内存预算：attach 一个已经在跑的 Task 不新增占用
	if !exists || !st.Running {
		if err := a.checkConcurrency(t); err != nil {
			return err
		}
		a.warnMemoryBudget()
	}
	if exists {
		if o.Base != "" {
			a.logf("task %s already exists; ignoring --base", t.Name)
		}
		if err := a.resume(t, st, o); err != nil {
			return err
		}
	} else if err := a.create(t, o); err != nil {
		return err
	}
	if o.headless() {
		return a.afterHeadless(t, o.Detach)
	}
	if o.Detach {
		fmt.Fprintf(a.Out, "task %s is already running in the background. Enter it with: sbx attach %s\n", t.Name, t.Name)
		return nil
	}
	return a.attach(t)
}

// resume 处理容器已存在的情况：已停止就启动，claude 不在就补拉起。
func (a *App) resume(t task.Task, st docker.State, o runOpts) error {
	meta, ok, err := t.ReadMeta()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("container %s exists but %s/meta.json is missing; run sbx done %s first, then run again", t.Container(), t.StateDir(), t.Name)
	}
	p := a.egress(t)
	if err := p.Ensure(); err != nil {
		return err
	}
	// 重新写入配置（shared 下 token 复用），保证代理被重建过时也能恢复
	if _, err := p.AttachTask(a.proxySpec(t, meta.TaskID)); err != nil {
		return err
	}
	if err := agent.RenderGen(t.GenDir(), a.genInput(a.hostClaude(), o)); err != nil {
		return err
	}
	if !st.Running {
		a.logf("starting stopped task %s ...", t.Name)
		if err := a.Docker.Start(t.Container()); err != nil {
			return err
		}
	}
	return a.startAgent(t, o)
}

// genInput 把生效配置和本次运行的参数汇成 gen 目录的输入。
func (a *App) genInput(host agent.HostClaude, o runOpts) agent.GenInput {
	return agent.GenInput{
		Host:          host,
		BlockCloudMCP: !a.Cfg.Network.CloudMCP,
		OnIdle:        a.Cfg.OnIdle,
		OnExit:        a.Cfg.OnExit,
		Throttle:      a.Cfg.NotifyThrottle,
		Prompt:        o.Prompt,
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
	return proxy.TaskSpec{
		TaskID:   taskID,
		Network:  t.Network(),
		CredFile: filepath.Join(t.StateDir(), "proxy.cred"),
		Allow:    proxy.RenderAllow(block, proxy.BuiltinList("builtin"), proxy.BuiltinList(a.Cfg.Profile), a.Cfg.Network.Allow, notify),
		Block:    block,
		Open:     a.Cfg.Network.Mode == "open",
	}
}

func (a *App) create(t task.Task, o runOpts) (err error) {
	var u undo
	step := "preparing the worktree"
	defer func() {
		if err != nil {
			a.logf("failed at step [%s]; cleaning up what this run created (the worktree is kept) ...", step)
			u.run()
			err = fmt.Errorf("%s：%w", step, err)
		}
	}()

	// 1. worktree
	baseSHA, err := a.prepareWorktree(t, o.Base)
	if err != nil {
		return err
	}

	// 2. 镜像
	step = "preparing the image"
	tag, err := a.agentImage()
	if err != nil {
		return err
	}

	// 3. state 目录
	if err := os.MkdirAll(t.GenDir(), 0o755); err != nil {
		return err
	}
	os.Remove(filepath.Join(t.StateDir(), "status.json"))

	// 4. 网络与 proxy
	step = "preparing the egress proxy"
	p := a.egressOf(t, a.Cfg.Network.Proxy)
	if err := p.Ensure(); err != nil {
		return err
	}
	step = "creating the task network"
	if ok, err := a.Docker.NetworkExists(t.Network()); err != nil {
		return err
	} else if !ok {
		labels := t.Labels()
		labels["sbx.kind"] = "net"
		if err := a.Docker.NetworkCreate(t.Network(), true, labels); err != nil {
			return err
		}
		u.add(func() { a.Docker.NetworkRm(t.Network()) })
	}
	step = "attaching to the egress proxy"
	if _, skipped := proxy.HostsIn(a.Cfg.OnIdle, a.Cfg.OnExit); len(skipped) > 0 {
		a.logf("note: %s in the notify command contains a variable or is an IP and cannot be allowed automatically; run sbx net allow <host> if you need it",
			strings.Join(skipped, "、"))
	}
	u.add(func() { p.DetachTask(t.ID(), t.Network()); p.StopIfIdle() })
	proxyURL, err := p.AttachTask(a.proxySpec(t, t.ID()))
	if err != nil {
		return err
	}

	// 5. volume
	step = "preparing volumes"
	if err := a.ensureVolumes(t, tag, &u); err != nil {
		return err
	}

	// 6. gen 目录
	step = "rendering generated files"
	host := a.hostClaude()
	if err := agent.RenderGen(t.GenDir(), a.genInput(host, o)); err != nil {
		return err
	}

	// 7. 容器
	step = "creating the container"
	gitDir := t.WS.GitDir
	if t.IsMain() {
		gitDir = ""
	}
	name, _ := workspace.Git(t.WS.Root, "config", "user.name")
	mail, _ := workspace.Git(t.WS.Root, "config", "user.email")
	key, err := a.apiKey("")
	if err != nil {
		return err
	}
	if key.Env != "" {
		a.logf("starting with an API key (from %s), not subscription login", key.Source)
	}
	labels := t.Labels()
	labels["sbx.role"] = "agent"
	// 这个 label 是 Shared.StopIfIdle 数"还有没有 shared Task 在跑"的依据，
	// dedicated 的 Task 不能混进去，否则共享代理永远停不掉
	labels["sbx.proxy"] = a.Cfg.Network.Proxy
	spec := docker.RunSpec{
		Name:    t.Container(),
		Image:   tag,
		Labels:  labels,
		Network: t.Network(),
		Mounts: agent.Mounts(agent.MountInput{
			Worktree: t.Worktree(), GitDir: gitDir,
			DepMasks: a.Cfg.Deps.Mask, DepVolume: t.DepVolume,
			StateDir: t.StateDir(), GenDir: t.GenDir(), Host: host,
		}),
		Env:     agent.Env(agent.EnvInput{ProxyURL: proxyURL, GitName: name, GitMail: mail, WS: t.WS.ID, Task: t.Name, TZ: agent.HostTZ(), APIKeyEnv: key.Env, APIKey: key.Value}),
		Workdir: t.Worktree(),
		Resources: docker.Resources{
			CPUs: a.Cfg.Resources.CPUs, Memory: a.Cfg.Resources.Memory, Pids: a.Cfg.Resources.Pids,
		},
	}
	if _, err := a.Docker.RunContainer(spec); err != nil {
		return err
	}
	u.add(func() { a.Docker.Rm(t.Container()) })

	// 8. meta（先写，startAgent 失败时 resume 还能用）
	step = "writing meta"
	if err := t.WriteMeta(task.Meta{
		Task: t.Name, WS: t.WS.ID, Root: t.WS.Root, Base: baseSHA, Profile: a.Cfg.Profile, Image: tag,
		Proxy: a.Cfg.Network.Proxy, TaskID: t.ID(), DepMasks: a.Cfg.Deps.Mask, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	u.add(func() { os.Remove(filepath.Join(t.StateDir(), "meta.json")) })

	// 9. 预置、登录检查、starting the agent
	step = "starting the agent"
	return a.startAgent(t, o)
}

func (a *App) prepareWorktree(t task.Task, base string) (string, error) {
	if t.IsMain() {
		if base != "" {
			a.logf("the main task uses the repo root directly; ignoring --base")
		}
		return t.WS.HeadRef()
	}
	ref := base
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := workspace.Git(t.WS.Root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("--base %q is not a valid commit: %w", ref, err)
	}
	created, note, err := t.WS.EnsureWorktree(t.Worktree(), t.Branch(), sha)
	if err != nil {
		return "", err
	}
	if note != "" {
		a.logf("%s", note)
	}
	if created {
		a.logf("worktree: %s (branch %s)", t.Worktree(), t.Branch())
	}
	if note != "" || !created {
		// 分支是已有的：base 取它和 HEAD 的分叉点
		if mb, err := workspace.Git(t.WS.Root, "merge-base", sha, t.Branch()); err == nil {
			sha = mb
		}
	}
	return sha, nil
}

// initVol 在一个临时容器里以 root 初始化 volume 的内容和属主。
func (a *App) initVol(tag, vol, script string) error {
	_, err := a.Docker.Run("run", "--rm", "-u", "0", "--network", "none",
		"--mount", "type=volume,source="+vol+",target=/v", "--entrypoint", "sh", tag, "-c", script)
	return err
}

// ensureSharedVolumes 创建全局共享的 sbx-home 和 sbx-cache，新建时按宿主机的 UID/GID 设属主。
// sbx run 和 sbx login 都要先过这一步。
func (a *App) ensureSharedVolumes(tag string) error {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	scripts := map[string]string{
		sharedHome:  "chown " + uid + ":" + gid + " /v",
		"sbx-cache": "mkdir -p /v/npm /v/pip /v/uv /v/go-mod /v/go-build /v/cargo && chown -R " + uid + ":" + gid + " /v",
		"sbx-mise":  "chown " + uid + ":" + gid + " /v",
	}
	for _, vol := range []string{sharedHome, "sbx-cache", "sbx-mise"} {
		created, err := a.Docker.VolumeCreate(vol, map[string]string{"sbx.kind": "shared"})
		if err != nil {
			return err
		}
		if created {
			if err := a.initVol(tag, vol, scripts[vol]); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureVolumes 准备共享 volume 和这个 Task 的依赖 volume。
func (a *App) ensureVolumes(t task.Task, tag string, u *undo) error {
	if err := a.ensureSharedVolumes(tag); err != nil {
		return err
	}
	own := "chown " + strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()) + " /v"
	for i := range a.Cfg.Deps.Mask {
		vol := t.DepVolume(i)
		labels := t.Labels()
		labels["sbx.kind"] = "dep"
		created, err := a.Docker.VolumeCreate(vol, labels)
		if err != nil {
			return err
		}
		if created {
			u.add(func() { a.Docker.VolumeRm(vol) })
			if err := a.initVol(tag, vol, own); err != nil {
				return err
			}
		}
	}
	return nil
}

// startAgent 预置首次启动状态、检查登录、在 tmux 里拉起 claude 并做冒烟检查。
// 会话还在但 claude 已经退出时（Ctrl-D、/exit），在原会话里重新拉起。
func (a *App) startAgent(t task.Task, o runOpts) error {
	rt := agent.Runtime{Docker: a.Docker, Container: t.Container(), Worktree: t.Worktree()}
	hasSession := rt.HasSession()
	if hasSession && !agentExited(t) {
		return nil
	}
	key, err := a.apiKey("")
	if err != nil {
		return err
	}
	if out, err := rt.Preseed(key.Value); err != nil {
		return fmt.Errorf("failed to preseed the first-run state: %w", err)
	} else if out != "" {
		a.logf("%s", out)
	}
	if err := a.importMemory(t.Container()); err != nil {
		a.logf("warning: failed to import project memory: %v", err)
	}
	a.installRuntimes(t, rt)
	// 配了 API key 就不查订阅登录态：key 在建容器时注进了环境变量（design §7.1）
	if err := a.checkAuth(t, rt, key); err != nil {
		return err
	}
	cont := !o.Fresh && t.HasPriorSession()
	os.Remove(filepath.Join(t.StateDir(), "status.json"))
	os.Remove(filepath.Join(t.StateDir(), "run.exit"))
	if o.headless() {
		if err := rotateRunLog(filepath.Join(t.StateDir(), "run.log"), runLogLimit); err != nil {
			return err
		}
	}
	cmdOf := func() string {
		if o.headless() {
			return agent.HeadlessCmd(agent.HeadlessOpts{Continue: cont, Notify: a.Cfg.OnExit != ""})
		}
		return agent.ClaudeCmd(cont)
	}
	if hasSession {
		a.logf("claude exited; bringing it back up in the same session ...")
		if err := rt.Respawn(cmdOf()); err != nil {
			return err
		}
	} else if _, err := rt.StartSession(cmdOf()); err != nil {
		return err
	}
	if cont {
		a.logf("resuming the last conversation of task %s (--continue; use --fresh to start a new one)", t.Name)
	}
	a.logf("waiting for claude to be ready ...")
	sm := agent.Smoke{
		Timeout: smokeTimeout,
		Ready: func() bool {
			s, err := t.ReadStatus()
			return err == nil && s != nil && s.Event == "SessionStart"
		},
		Exited: func() bool { return agentExited(t) },
	}
	if o.headless() {
		// headless 的任务可能几秒就跑完，正常结束不算启动失败；
		// 跑完了也算"就绪"，别再等 SessionStart（那条可能已经被 SessionEnd 覆盖了）
		sm.Exited = nil
		ready := sm.Ready
		sm.Ready = func() bool { return ready() || t.RunExit() != nil }
	}
	return rt.SmokeCheck(sm)
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

// agentExited 报告 hooks 写下的状态是不是"claude 已退出"。
func agentExited(t task.Task) bool {
	s, err := t.ReadStatus()
	return err == nil && s != nil && s.State == "exited"
}

// checkAuth 在拉起 claude 前确认它拿得到凭据：配了 API key 就只核对容器里真有那个
// 环境变量（容器是建的时候注入的，配置后来才改的话这里能提前说清楚），
// 否则查 sbx-home 里的订阅登录态。
func (a *App) checkAuth(t task.Task, rt agent.Runtime, key apiKey) error {
	if key.Env != "" {
		if has, err := a.Docker.HasEnv(t.Container(), key.Env); err == nil && !has {
			return fmt.Errorf("an API key is configured, but container %s was created before that and does not have %s.\n"+
				"environment variables can only be injected when the container is created: run sbx done %s, then run again", t.Container(), key.Env, t.Name)
		}
		return nil
	}
	ok, err := rt.LoggedIn()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New(a.loginHelp(t))
	}
	return nil
}

func (a *App) loginHelp(t task.Task) string {
	return "claude is not logged in inside the sandbox (no credentials in sbx-home). Run this once:\n\n  sbx login\n\nThen run sbx run " + t.Name + "."
}

// afterHeadless 收尾 headless 这一次运行：前台就跟着 run.log 看到结束，
// --detach 就只打一行怎么回来看。
func (a *App) afterHeadless(t task.Task, detach bool) error {
	if detach {
		fmt.Fprintf(a.Out, "task %s is running the prompt in the background. Follow it with: sbx logs -f %s\n", t.Name, t.Name)
		return nil
	}
	if err := a.showLogs(t, true, 0); err != nil {
		return err
	}
	code := 0
	if c := t.RunExit(); c != nil {
		code = *c
	}
	fmt.Fprintf(a.Out, "\ntask %s finished (exit=%d) and its container is stopped.\n", t.Name, code)
	fmt.Fprintf(a.Out, "see changes: cd $(sbx path %s) | continue: sbx run %s | finish: sbx done %s\n", t.Name, t.Name, t.Name)
	if code != 0 {
		// claude 的退出码往上透，脚本里 sbx run -p ... && next 才有意义
		return fmt.Errorf("claude exited with %d (full output: sbx logs %s)", code, t.Name)
	}
	return nil
}
