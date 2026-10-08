package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"sandx/internal/agent"
	"sandx/internal/docker"
	"sandx/internal/proxy"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

const smokeTimeout = 30 * time.Second

func (a *App) runCmd() *cobra.Command {
	var base, netMode string
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
				a.logf("生效配置：\n%s", a.Cfg)
			}
			if netMode != "" {
				a.Cfg.Network.Mode = netMode
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
			return a.run(t, base, detach, fresh)
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "starting point for the new branch (default: current HEAD)")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "start the task without attaching to it")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "start a new conversation instead of continuing this task's last one")
	cmd.Flags().StringVar(&netMode, "net", "", "network mode for this run: open (default, everything allowed) or allowlist")
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

func (a *App) run(t task.Task, base string, detach bool, fresh bool) error {
	st, exists, err := a.Docker.Inspect(t.Container())
	if err != nil {
		return err
	}
	// 只有真要启动一个容器时才查并发和内存预算：attach 一个已经在跑的 Task 不新增占用
	if !exists || !st.Running {
		if err := a.checkConcurrency(t); err != nil {
			return err
		}
		a.warnMemoryBudget()
	}
	if exists {
		if base != "" {
			a.logf("Task %s 已存在，忽略 --base", t.Name)
		}
		if err := a.resume(t, st, fresh); err != nil {
			return err
		}
	} else if err := a.create(t, base, fresh); err != nil {
		return err
	}
	if detach {
		fmt.Fprintf(a.Out, "Task %s 已在后台运行。进入：sbx attach %s\n", t.Name, t.Name)
		return nil
	}
	return a.attach(t)
}

// resume 处理容器已存在的情况：已停止就启动，claude 不在就补拉起。
func (a *App) resume(t task.Task, st docker.State, fresh bool) error {
	meta, ok, err := t.ReadMeta()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("容器 %s 存在但缺少 %s/meta.json；请先 sbx done %s 再重新 run", t.Container(), t.StateDir(), t.Name)
	}
	p := a.proxy()
	if err := p.EnsureShared(); err != nil {
		return err
	}
	// 重新写入片段（token 复用），保证 proxy 被重建过时也能恢复
	if _, err := p.AttachTask(a.proxySpec(t, meta.TaskID)); err != nil {
		return err
	}
	if err := agent.RenderGen(t.GenDir(), a.hostClaude(), !a.Cfg.Network.CloudMCP); err != nil {
		return err
	}
	if !st.Running {
		a.logf("启动已停止的 Task %s …", t.Name)
		if err := a.Docker.Start(t.Container()); err != nil {
			return err
		}
	}
	return a.startAgent(t, fresh)
}

func (a *App) hostClaude() agent.HostClaude {
	h := agent.InspectHostClaude(hostClaudeDir())
	for _, w := range h.Warnings {
		a.logf("警告: %s", w)
	}
	return h
}

func (a *App) proxySpec(t task.Task, taskID string) proxy.TaskSpec {
	block := proxy.BuiltinList("policy-block")
	if a.Cfg.Network.CloudMCP {
		block = nil
	}
	return proxy.TaskSpec{
		TaskID:   taskID,
		Network:  t.Network(),
		CredFile: filepath.Join(t.StateDir(), "proxy.cred"),
		Allow:    proxy.RenderAllow(block, proxy.BuiltinList("builtin"), proxy.BuiltinList(a.Cfg.Profile), a.Cfg.Network.Allow),
		Block:    block,
		Open:     a.Cfg.Network.Mode == "open",
	}
}

func (a *App) create(t task.Task, base string, fresh bool) (err error) {
	if a.Cfg.Network.Proxy != "shared" {
		return fmt.Errorf("M1 只支持 network.proxy = \"shared\"")
	}
	var u undo
	step := "准备 worktree"
	defer func() {
		if err != nil {
			a.logf("在「%s」这一步失败，清理本次新建的资源（worktree 保留）…", step)
			u.run()
			err = fmt.Errorf("%s：%w", step, err)
		}
	}()

	// 1. worktree
	baseSHA, err := a.prepareWorktree(t, base)
	if err != nil {
		return err
	}

	// 2. 镜像
	step = "准备镜像"
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
	step = "启动 shared proxy"
	p := a.proxy()
	if err := p.EnsureShared(); err != nil {
		return err
	}
	step = "创建 Task 网络"
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
	step = "接入 shared proxy"
	u.add(func() { p.DetachTask(t.ID(), t.Network()); p.StopIfIdle() })
	proxyURL, err := p.AttachTask(a.proxySpec(t, t.ID()))
	if err != nil {
		return err
	}

	// 5. volume
	step = "准备 volume"
	if err := a.ensureVolumes(t, tag, &u); err != nil {
		return err
	}

	// 6. gen 目录
	step = "渲染生成文件"
	host := a.hostClaude()
	if err := agent.RenderGen(t.GenDir(), host, !a.Cfg.Network.CloudMCP); err != nil {
		return err
	}

	// 7. 容器
	step = "创建容器"
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
		a.logf("用 API key 启动（来自%s），不走订阅登录", key.Source)
	}
	labels := t.Labels()
	labels["sbx.role"] = "agent"
	labels["sbx.proxy"] = "shared"
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
	step = "写入 meta"
	if err := t.WriteMeta(task.Meta{
		Task: t.Name, WS: t.WS.ID, Root: t.WS.Root, Base: baseSHA, Profile: a.Cfg.Profile, Image: tag,
		Proxy: "shared", TaskID: t.ID(), DepMasks: a.Cfg.Deps.Mask, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	u.add(func() { os.Remove(filepath.Join(t.StateDir(), "meta.json")) })

	// 9. 预置、登录检查、启动 Agent
	step = "启动 Agent"
	return a.startAgent(t, fresh)
}

func (a *App) prepareWorktree(t task.Task, base string) (string, error) {
	if t.IsMain() {
		if base != "" {
			a.logf("main Task 直接使用仓库根，忽略 --base")
		}
		return t.WS.HeadRef()
	}
	ref := base
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := workspace.Git(t.WS.Root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("--base %q 不是有效的提交：%w", ref, err)
	}
	created, note, err := t.WS.EnsureWorktree(t.Worktree(), t.Branch(), sha)
	if err != nil {
		return "", err
	}
	if note != "" {
		a.logf("%s", note)
	}
	if created {
		a.logf("worktree：%s（分支 %s）", t.Worktree(), t.Branch())
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
	}
	for _, vol := range []string{sharedHome, "sbx-cache"} {
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
func (a *App) startAgent(t task.Task, fresh bool) error {
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
		return fmt.Errorf("预置首次启动状态失败：%w", err)
	} else if out != "" {
		a.logf("%s", out)
	}
	if err := a.importMemory(t.Container()); err != nil {
		a.logf("警告: 导入项目记忆失败：%v", err)
	}
	// 配了 API key 就不查订阅登录态：key 在建容器时注进了环境变量（design §7.1）
	if err := a.checkAuth(t, rt, key); err != nil {
		return err
	}
	cont := !fresh && t.HasPriorSession()
	statusFile := filepath.Join(t.StateDir(), "status.json")
	os.Remove(statusFile)
	if hasSession {
		a.logf("claude 已退出，正在原会话里重新拉起 …")
		if err := rt.RespawnClaude(cont); err != nil {
			return err
		}
	} else if _, err := rt.StartClaude(cont); err != nil {
		return err
	}
	if cont {
		a.logf("接上 Task %s 的上次对话（--continue；要新开一段用 --fresh）", t.Name)
	}
	a.logf("等待 claude 就绪 …")
	return rt.SmokeCheck(smokeTimeout, func() bool {
		s, err := t.ReadStatus()
		return err == nil && s != nil && s.Event == "SessionStart"
	}, func() bool {
		return agentExited(t)
	})
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
			return fmt.Errorf("配置里有 API key，但容器 %s 是在那之前建的，里面没有 %s。\n"+
				"环境变量只能在建容器时注入：sbx done %s 之后重新 run", t.Container(), key.Env, t.Name)
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
	return "沙箱里的 claude 还没有登录（sbx-home 里没有凭据）。先执行一次：\n\n  sbx login\n\n登录完成后重新执行 sbx run " + t.Name + "。"
}
