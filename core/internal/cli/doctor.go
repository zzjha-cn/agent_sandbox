package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/agent"
	"sandx/internal/docker"
	"sandx/internal/proxy"
)

// level 是一项检查的结论。
type level int

const (
	lvOK   level = iota // 没问题
	lvWarn              // 能用，但该改
	lvFail              // 用不了
	lvSkip              // 这台机器上跑不了这项（没 docker、不在仓库里、--quick）
)

func (l level) mark() string {
	switch l {
	case lvOK:
		return "✓"
	case lvWarn:
		return "!"
	case lvFail:
		return "✗"
	}
	return "-"
}

// Check 是一项检查的结果。Fix 是建议的修复动作，ok 时为空。
type Check struct {
	Name   string
	Level  level
	Detail string
	Fix    string
}

func ok(name, detail string) Check { return Check{Name: name, Level: lvOK, Detail: detail} }
func skip(name, why string) Check  { return Check{Name: name, Level: lvSkip, Detail: why} }
func warn(n, d, fix string) Check  { return Check{Name: n, Level: lvWarn, Detail: d, Fix: fix} }
func fail(n, d, fix string) Check  { return Check{Name: n, Level: lvFail, Detail: d, Fix: fix} }

// checker 是一项检查。Need* 决定它在当前环境下跑不跑得了。
type checker struct {
	Name       string
	NeedDocker bool
	NeedRepo   bool
	Slow       bool // --quick 时跳过
	Run        func(a *App, env docEnv) Check
}

// docEnv 是这次 doctor 跑在什么环境里。docker info 只取一次，几项检查共用。
type docEnv struct {
	Docker bool // docker is reachable
	Repo   bool // 当前在一个 git 仓库里，配置已经合并了四层
	Info   docker.Item
}

// checks 的顺序就是输出顺序：先是跑不起来就什么都别谈的，再是具体功能。
var checks = []checker{
	{Name: "docker", Run: checkDocker},
	{Name: "vm-memory", NeedDocker: true, Run: checkVMMemory},
	{Name: "upstream", NeedDocker: true, Run: checkUpstream},
	{Name: "sbx-proxy", NeedDocker: true, Run: checkProxy},
	{Name: "login", NeedDocker: true, Run: checkLogin},
	{Name: "trust", NeedRepo: true, Run: checkTrust},
	{Name: "notify", Run: checkNotify},
	{Name: "smoke", NeedDocker: true, Slow: true, Run: checkSmoke},
}

func (a *App) doctorCmd() *cobra.Command {
	var quick, strict bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check docker, the proxy, login, trust and the agent's first-run state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 不要求在 git 仓库里（和 login / upgrade 一致）；在仓库里就多查几项
			if err := a.loadConfig(); err != nil {
				return err
			}
			env := docEnv{}
			if err := a.load(); err == nil {
				env.Repo = true
			}
			info, err := a.Docker.Info()
			env.Docker, env.Info = err == nil, info
			results := a.runChecks(env, quick)
			return a.printChecks(results, strict)
		},
	}
	cmd.Flags().BoolVar(&quick, "quick", false, "skip the slow checks (the first-run smoke test)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero on warnings too (for scripts)")
	return cmd
}

func (a *App) runChecks(env docEnv, quick bool) []Check {
	var out []Check
	for _, c := range checks {
		switch {
		case c.NeedDocker && !env.Docker:
			out = append(out, skip(c.Name, "docker is unavailable, skipping this check"))
		case c.NeedRepo && !env.Repo:
			out = append(out, skip(c.Name, "not in a git repository, skipping this check"))
		case c.Slow && quick:
			out = append(out, skip(c.Name, "skipped (--quick)"))
		default:
			out = append(out, c.Run(a, env))
		}
	}
	return out
}

func (a *App) printChecks(results []Check, strict bool) error {
	var warns, fails int
	for _, c := range results {
		fmt.Fprintf(a.Out, "  %s %s  %s\n", c.Level.mark(), padCJK(c.Name, 10), c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(a.Out, "      → %s\n", c.Fix)
		}
		switch c.Level {
		case lvWarn:
			warns++
		case lvFail:
			fails++
		}
	}
	fmt.Fprintf(a.Out, "\n%d warning(s), %d failure(s)\n", warns, fails)
	// warn 的语义是"能用，但该改"。doctor 会被写进脚本，让 warn 也挂掉会逼人去 || true。
	if fails > 0 || (strict && warns > 0) {
		return fmt.Errorf("some checks did not pass")
	}
	return nil
}

func checkDocker(a *App, env docEnv) Check {
	if !env.Docker {
		return fail("docker", "cannot reach the docker daemon", "start Docker Desktop (or colima start), then retry")
	}
	ver, _ := env.Info["ServerVersion"].(string)
	mem, _ := env.Info["MemTotal"].(float64)
	return ok("docker", fmt.Sprintf("Docker %s, VM memory %s", ver, humanBytes(int64(mem))))
}

func checkVMMemory(a *App, env docEnv) Check {
	total, _ := env.Info["MemTotal"].(float64)
	per := a.Cfg.Resources.MemoryBytes()
	if total <= 0 || per <= 0 {
		return skip("vm-memory", "cannot determine VM memory or resources.memory")
	}
	if msg := budgetWarning(a.Cfg.MaxRunning, a.Cfg.Resources.Memory, per, int64(total)); msg != "" {
		// 只取第一句，修复建议本来就单独一行
		head, _, _ := strings.Cut(msg, "。")
		return warn("vm-memory", head, "lower max_running or resources.memory, or give Docker more memory")
	}
	return ok("vm-memory", fmt.Sprintf("max_running(%d) × %s = %s ≤ VM memory %s × %.0f%%",
		a.Cfg.MaxRunning, a.Cfg.Resources.Memory, humanBytes(int64(a.Cfg.MaxRunning)*per),
		humanBytes(int64(total)), vmBudget*100))
}

func checkUpstream(a *App, env docEnv) Check {
	up := strings.TrimSpace(a.Cfg.Network.Upstream)
	if up == "" {
		return ok("upstream", "no upstream proxy configured; containers connect directly")
	}
	tag, err := a.agentImage()
	if err != nil {
		return skip("upstream", "agent image not built yet; run sbx run once")
	}
	// 必须从容器里探：host.docker.internal 在宿主机上是另一回事
	out, err := a.Docker.Run("run", "--rm", "-e", "HTTPS_PROXY="+up, "-e", "HTTP_PROXY="+up, tag,
		"curl", "-sS", "-m", "8", "-o", "/dev/null", "-w", "%{http_code}", "https://api.anthropic.com/")
	if err != nil || strings.TrimSpace(out) == "000" {
		return fail("upstream", up+" is unreachable", "check that your local proxy is running, or clear network.upstream to connect directly")
	}
	return ok("upstream", up+" is reachable")
}

func checkProxy(a *App, _ docEnv) Check {
	running, tasks, detail, err := a.proxy().Health()
	if err != nil {
		return fail("sbx-proxy", "cannot check: "+err.Error(), "")
	}
	switch {
	case running && strings.Contains(detail, "the config check failed"):
		return fail("sbx-proxy", detail, "docker rm -f "+proxy.SharedName+", then sbx run will rebuild it")
	case running:
		return ok("sbx-proxy", fmt.Sprintf("%s, %d task(s) attached", detail, tasks))
	case tasks > 0:
		// 有 Task 在跑却没有代理：这些 Task 现在是断网的（R8）
		return fail("sbx-proxy", fmt.Sprintf("%s, but %d shared task(s) are running (they have no egress right now)", detail, tasks),
			"docker rm -f "+proxy.SharedName+", then run sbx run again")
	default:
		return ok("sbx-proxy", detail+" (no shared task is running, which is fine)")
	}
}

func checkLogin(a *App, _ docEnv) Check {
	name := a.Cfg.DefaultAgent
	if key, err := a.apiKey(name); err != nil {
		return fail("login", "API key configured but its value cannot be read: "+err.Error(), "check api_key_env / api_key_file")
	} else if key.Env != "" {
		return ok("login", "using an API key (from "+key.Source+"), not subscription login")
	}
	_, cli, err := resolveAgent(name)
	if err != nil {
		return skip("login", err.Error())
	}
	in, err := a.imageInputs()
	if err != nil {
		return skip("login", err.Error())
	}
	// 镜像不在就别现场构建：doctor 不该一跑几分钟
	if exists, err := a.Docker.ImageExists(in.Tag()); err != nil || !exists {
		return warn("login", "agent image not built yet; cannot check login state", "run sbx run or sbx login once")
	}
	st, err := a.authStatus(in.Tag(), cli)
	if err != nil || !st.LoggedIn {
		return fail("login", "not logged in inside the sandbox", "sbx login")
	}
	return ok("login", st.Describe())
}

func checkTrust(a *App, _ docEnv) Check {
	cur, old, had, err := a.trustState()
	if err != nil {
		return fail("trust", err.Error(), "")
	}
	switch {
	case cur.Empty() && !had:
		return ok("trust", "this repo has no .sbx/, nothing to confirm")
	case !had:
		return warn("trust", ".sbx/ has not been confirmed yet; sbx run will stop", "sbx trust")
	case old.Hash != cur.Hash:
		return warn("trust", ".sbx/ has changed since you last confirmed it", "sbx trust")
	}
	return ok("trust", ".sbx/ matches what you confirmed")
}

func checkNotify(a *App, _ docEnv) Check {
	if a.Cfg.OnIdle == "" && a.Cfg.OnExit == "" {
		return ok("notify", "on_idle / on_exit not configured")
	}
	hosts, skipped := proxy.HostsIn(a.Cfg.OnIdle, a.Cfg.OnExit)
	// $SBX_TASK 落在 shell 单引号里不会展开，收到的是字面量——这个坑设计稿里自己踩过
	if m := singleQuotedVar(a.Cfg.OnIdle, a.Cfg.OnExit); m != "" {
		return warn("notify", "the notify command has "+m+" inside single quotes, so it will not be expanded (you get the literal text)",
			"switch that part to double quotes and escape inner quotes as \\\"")
	}
	detail := "no hosts can be allowed automatically"
	if len(hosts) > 0 {
		detail = strings.Join(hosts, ", ") + " added to the allowlist automatically"
	}
	if len(skipped) > 0 {
		return warn("notify", detail+"；"+strings.Join(skipped, ", ")+" contain variables or are IPs and cannot be allowed automatically",
			"run sbx net allow <host> if you need them")
	}
	return ok("notify", detail)
}

// singleQuotedVar 找出落在单引号里的 $SBX_* 变量。
func singleQuotedVar(cmds ...string) string {
	for _, c := range cmds {
		parts := strings.Split(c, "'")
		// 奇数下标是单引号内的部分
		for i := 1; i < len(parts); i += 2 {
			if j := strings.Index(parts[i], "$SBX_"); j >= 0 {
				name := parts[i][j:]
				if k := strings.IndexFunc(name, func(r rune) bool {
					return !(r == '$' || r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
				}); k > 0 {
					name = name[:k]
				}
				return name
			}
		}
	}
	return ""
}

func checkSmoke(a *App, env docEnv) Check {
	// 先找一个正在跑的 Task：对它抓一次画面是零成本的
	items, err := a.Docker.Running("sbx.role=agent")
	if err == nil && len(items) > 0 {
		name := items[0].Str("Names")
		rt := agent.Runtime{Docker: a.Docker, Container: name}
		screen, err := rt.Capture()
		if err == nil {
			if m := agent.DialogIn(screen); m != "" {
				return fail("smoke", "container "+name+" shows a first-run dialog: "+m,
					"claude's internal state fields may have changed between versions (R9); see agent/preseed.go")
			}
			return ok("smoke", "running container "+name+" looks normal, not stuck on a dialog")
		}
	}
	return skip("smoke", "no task is running; start one to check (sbx run <task> runs this smoke check itself)")
}
