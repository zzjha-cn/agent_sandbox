package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/agent"
	"sandx/internal/image"
)

// sharedHome 是放登录态和 Agent 配置的全局 volume（design §7.1、ADR 0006）。
// claude 的凭据在它的 .claude/ 下，codex 将来在 .codex/ 下，同一个 volume。
const sharedHome = "sbx-home"

// authCLI 描述一个 Agent CLI 的登录流程。各家 CLI 的子命令和输出格式都不一样，
// 所以这里只约定 sbx 需要的四件事：怎么查、怎么登、怎么退、怎么读登录态。
type authCLI struct {
	bin    string                                    // 容器里的可执行文件
	status []string                                  // 查登录态的子命令（输出交给 parse）
	login  func(console bool, email string) []string // 走登录流程的子命令
	logout []string                                  // 退出登录的子命令
	parse  func(out string, err error) (agent.Status, error)
}

// authCLIs 是已经实现的 Agent。加一个新的 Agent = 在这里加一条。
var authCLIs = map[string]authCLI{
	"claude": {
		bin:    "claude",
		status: []string{"auth", "status"},
		login: func(console bool, email string) []string {
			sub := []string{"auth", "login"}
			if console {
				sub = append(sub, "--console")
			}
			if email != "" {
				sub = append(sub, "--email", email)
			}
			return sub
		},
		logout: []string{"auth", "logout"},
		parse:  agent.ParseStatus,
	},
}

// pendingAgents 是设计里有、但还没实现的 Agent：和打错名字区分开，报错里给出原因。
var pendingAgents = map[string]string{
	"codex": "Codex 的登录（codex login --device-auth）还没验证，推迟到 M3-6",
}

// resolveAgent 把命令行上的 Agent 名解析成一条 authCLI。
func resolveAgent(name string) (string, authCLI, error) {
	if name == "" {
		name = "claude"
	}
	if cli, ok := authCLIs[name]; ok {
		return name, cli, nil
	}
	if why, ok := pendingAgents[name]; ok {
		return "", authCLI{}, errors.New(why + "；目前只有 " + strings.Join(knownAgents(), "、"))
	}
	return "", authCLI{}, fmt.Errorf("不认识的 Agent %q；目前只有 %s", name, strings.Join(knownAgents(), "、"))
}

func knownAgents() []string {
	names := make([]string, 0, len(authCLIs))
	for n := range authCLIs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (a *App) loginCmd() *cobra.Command {
	var status, logout, force, console bool
	var email string
	cmd := &cobra.Command{
		Use:   "login [claude|codex]",
		Short: "Log in to an agent inside the sandbox (default claude; credentials live in sbx-home, shared by every task)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			name, cli, err := resolveAgent(name)
			if err != nil {
				return err
			}
			if err := a.loadConfig(); err != nil {
				return err
			}
			tag, err := a.agentImage()
			if err != nil {
				return err
			}
			if err := a.ensureSharedVolumes(tag); err != nil {
				return err
			}
			st, err := a.authStatus(tag, cli)
			if err != nil {
				return err
			}
			switch {
			case status:
				fmt.Fprintln(a.Out, st.Describe())
				return nil
			case logout:
				if !st.LoggedIn {
					fmt.Fprintln(a.Out, "本来就没有登录")
					return nil
				}
				if err := a.Docker.Interactive(a.authArgs(tag, cli, false, cli.logout...)...); err != nil {
					return err
				}
				fmt.Fprintln(a.Out, "已退出登录；凭据从 sbx-home 里清掉了。运行中的 Task 要等下次启动 Agent 才受影响。")
				return nil
			case st.LoggedIn && !force:
				fmt.Fprintln(a.Out, st.Describe())
				fmt.Fprintf(a.Out, "要换账号：先 sbx login %s --logout，或者直接 sbx login %s --force 重新走一遍。\n", name, name)
				return nil
			}
			fmt.Fprintf(a.Err, "在临时容器里打开 %s 的登录流程；按提示打开链接、粘回授权码。Ctrl-C 可以中断。\n", name)
			if err := a.Docker.Interactive(a.authArgs(tag, cli, true, cli.login(console, email)...)...); err != nil {
				return fmt.Errorf("登录没有完成：%w", err)
			}
			after, err := a.authStatus(tag, cli)
			if err != nil {
				return err
			}
			if !after.LoggedIn {
				return fmt.Errorf("登录流程结束了，但 sbx-home 里还是没有凭据；重新执行 sbx login %s 再试一次", name)
			}
			fmt.Fprintln(a.Out, after.Describe())
			fmt.Fprintln(a.Out, "凭据存在 volume sbx-home 里，所有 Task 共享；sbx done 不会删除它。")
			return nil
		},
	}
	cmd.Flags().BoolVar(&status, "status", false, "only print who is logged in, do not log in")
	cmd.Flags().BoolVar(&logout, "logout", false, "log out, clearing the credentials in sbx-home")
	cmd.Flags().BoolVar(&force, "force", false, "run the login flow again even when already logged in")
	cmd.Flags().BoolVar(&console, "console", false, "claude: use an Anthropic Console account (usage billing) instead of a Claude subscription")
	cmd.Flags().StringVar(&email, "email", "", "claude: pre-fill this email address on the login page")
	return cmd
}

// agentImage 确保 Agent 镜像存在，返回 tag。
func (a *App) agentImage() (string, error) {
	in, err := a.imageInputs()
	if err != nil {
		return "", err
	}
	return image.Builder{Docker: a.Docker, Upstream: a.Cfg.Network.Upstream, Out: a.Err}.Ensure(in)
}

// authStatus 在一个临时容器里读 sbx-home 里的登录态。
func (a *App) authStatus(tag string, cli authCLI) (agent.Status, error) {
	out, err := a.Docker.Run(a.authArgs(tag, cli, false, cli.status...)...)
	return cli.parse(out, err)
}

// authArgs 组装"挂着 sbx-home 跑一次 Agent CLI"的 docker 参数。
// 这个容器没有 Task，也就没有 Task 网络和 sbx-proxy：它只连 Agent 的服务端，
// 配了上游代理就走上游，否则直连（design §7.1）。
func (a *App) authArgs(tag string, cli authCLI, tty bool, sub ...string) []string {
	args := []string{"run", "--rm"}
	if tty {
		args = append(args, "-it")
	}
	args = append(args, "--mount", "type=volume,source="+sharedHome+",target=/home/agent")
	if up := a.Cfg.Network.Upstream; up != "" {
		args = append(args, "-e", "HTTPS_PROXY="+up, "-e", "HTTP_PROXY="+up)
	}
	args = append(args, tag, cli.bin)
	return append(args, sub...)
}
