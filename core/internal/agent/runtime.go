package agent

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"sandx/internal/docker"
)

const Session = "agent"

// Runtime 在一个运行中的 agent 容器里执行操作。
type Runtime struct {
	Docker    *docker.Client
	Container string
	Worktree  string
}

func (r Runtime) exec(cmd ...string) (string, error) {
	return r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent"}, cmd...)
}

// Preseed 预置首次启动状态并建立宿主机配置的软链接。
// apiKey 非空时顺带把它记成"已批准的自定义 API key"，否则交互模式会卡在确认框上。
func (r Runtime) Preseed(apiKey string) (string, error) {
	env := []string{"SBX_WORKTREE=" + r.Worktree}
	if v := APIKeyApproval(apiKey); v != "" {
		env = append(env, "SBX_API_KEY_APPROVE="+v)
	}
	return r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent", Env: env},
		"bash", "-c", Preseed()+"\n")
}

// LoggedIn 用 claude auth status 检查登录态。
func (r Runtime) LoggedIn() (bool, error) {
	out, err := r.exec("claude", "auth", "status")
	st, perr := ParseStatus(out, err)
	if perr != nil {
		return false, perr
	}
	return st.LoggedIn, nil
}

// HasSession 报告 tmux 会话是否存在。
func (r Runtime) HasSession() bool {
	_, err := r.exec("tmux", "has-session", "-t", Session)
	return err == nil
}

// ExitNotice 是 claude 退出后窗口里打印的提示，同时供 SmokeCheck 识别"启动后立刻退出"。
const ExitNotice = "[sbx] claude exited"

// ClaudeCmd 组装 tmux 窗口里跑的命令。
// claude 退出（Ctrl-D、/exit、崩溃）后不让窗口关闭：打印提示再 exec 一个 login shell。
// 否则窗口关闭会连带结束 tmux 会话，Task 就再也 attach 不回去了。
func ClaudeCmd(continueConv bool) string {
	cmd := "claude --dangerously-skip-permissions --settings /sbx/gen/settings.sbx.json"
	if continueConv {
		cmd += " --continue"
	}
	return cmd + "; printf '\n" + ExitNotice + ". Ctrl-b d leaves the container; sbx run brings it back up in this session.\n\n'; exec bash -l"
}

// StartSession 新建 tmux 会话并在里面跑 cmd；会话已存在时什么都不做。
func (r Runtime) StartSession(cmd string) (started bool, err error) {
	if r.HasSession() {
		return false, nil
	}
	_, err = r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent", Workdir: r.Worktree},
		"tmux", "new-session", "-d", "-s", Session, "-x", "220", "-y", "50", cmd)
	return err == nil, err
}

// Respawn 在已存在的会话里换一条命令：claude 退出后窗口里留着的是 shell，
// -k 杀掉它并在同一个窗口里重开，这样 attach 的人不用重新进。
func (r Runtime) Respawn(cmd string) error {
	_, err := r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent", Workdir: r.Worktree},
		"tmux", "respawn-window", "-k", "-t", Session, "-c", r.Worktree, cmd)
	return err
}

// StartClaude / RespawnClaude 是交互模式的薄包装。
func (r Runtime) StartClaude(continueConv bool) (bool, error) {
	return r.StartSession(ClaudeCmd(continueConv))
}

func (r Runtime) RespawnClaude(continueConv bool) error { return r.Respawn(ClaudeCmd(continueConv)) }

// StartHeadless / RespawnHeadless 同上，跑的是 claude -p（M3-7）。
func (r Runtime) StartHeadless(o HeadlessOpts) (bool, error) {
	return r.StartSession(HeadlessCmd(o))
}

func (r Runtime) RespawnHeadless(o HeadlessOpts) error { return r.Respawn(HeadlessCmd(o)) }

// Capture 抓取 tmux 画面。
func (r Runtime) Capture() (string, error) {
	return r.exec("tmux", "capture-pane", "-p", "-t", Session)
}

// 已知会卡住交互模式的对话框（M0-5、R9）。
var dialogRe = regexp.MustCompile(`(?i)select login method|bypass permissions mode|choose the text style|do you trust the files|trust this folder`)

// Smoke 描述一次冒烟检查。Exited 为 nil 表示"退出不算失败"——headless 的任务
// 可能几秒就跑完，不能把正常结束当成启动失败。
type Smoke struct {
	Timeout time.Duration
	Ready   func() bool // 就绪判定（读宿主机上的 status.json）
	Exited  func() bool // 异常退出判定；nil 时跳过
}

// DialogIn 返回画面上出现的首次启动对话框（没有就返回空）。
// sbx doctor 和启动冒烟用的是同一份判据（R9）。
func DialogIn(screen string) string { return dialogRe.FindString(screen) }

// SmokeCheck 轮询 tmux 画面和 status.json，确认 claude 已就绪且没有卡在对话框上。
// 会话现在不会随 claude 退出而消失，所以"启动后就退出"要靠 SessionEnd 或画面上的提示来判断。
func (r Runtime) SmokeCheck(sm Smoke) error {
	deadline := time.Now().Add(sm.Timeout)
	var screen string
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if !r.HasSession() {
			// headless 跑完会 kill 1 停掉容器，tmux 会话跟着消失：那是正常结束，不是启动失败
			if sm.Exited == nil {
				return nil
			}
			return fmt.Errorf("claude exited after starting (the tmux session has ended)")
		}
		screen, _ = r.Capture()
		if sm.Exited != nil && (sm.Exited() || strings.Contains(screen, ExitNotice)) {
			return fmt.Errorf("claude exited immediately after starting; last 20 lines of the screen:\n%s", lastLines(screen, 20))
		}
		if m := DialogIn(screen); m != "" {
			return fmt.Errorf("claude is stuck on a first-run dialog (%q); the preseeded fields may have changed between versions (R9):\n%s", m, lastLines(screen, 20))
		}
		if sm.Ready() {
			return nil
		}
	}
	return fmt.Errorf("SessionStart did not arrive within %s; last 20 lines of the screen:\n%s", sm.Timeout, lastLines(screen, 20))
}

func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
