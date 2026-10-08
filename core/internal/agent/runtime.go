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
func (r Runtime) Preseed() (string, error) {
	return r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent", Env: []string{"SBX_WORKTREE=" + r.Worktree}},
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
const ExitNotice = "[sbx] claude 已退出"

// ClaudeCmd 组装 tmux 窗口里跑的命令。
// claude 退出（Ctrl-D、/exit、崩溃）后不让窗口关闭：打印提示再 exec 一个 login shell。
// 否则窗口关闭会连带结束 tmux 会话，Task 就再也 attach 不回去了。
func ClaudeCmd(continueConv bool) string {
	cmd := "claude --dangerously-skip-permissions --settings /sbx/gen/settings.sbx.json"
	if continueConv {
		cmd += " --continue"
	}
	return cmd + "; printf '\n" + ExitNotice + "。Ctrl-b d 离开容器；sbx run 可以在这个会话里重新拉起。\n\n'; exec bash -l"
}

// StartClaude 新建 tmux 会话并在里面拉起 claude；会话已存在时什么都不做。
func (r Runtime) StartClaude(continueConv bool) (started bool, err error) {
	if r.HasSession() {
		return false, nil
	}
	_, err = r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent", Workdir: r.Worktree},
		"tmux", "new-session", "-d", "-s", Session, "-x", "220", "-y", "50",
		ClaudeCmd(continueConv))
	return err == nil, err
}

// RespawnClaude 在已存在的会话里重新拉起 claude：claude 退出后窗口里留着的是 shell，
// -k 杀掉它并在同一个窗口里重开，这样 attach 的人不用重新进。
func (r Runtime) RespawnClaude(continueConv bool) error {
	_, err := r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent", Workdir: r.Worktree},
		"tmux", "respawn-window", "-k", "-t", Session, "-c", r.Worktree, ClaudeCmd(continueConv))
	return err
}

// Capture 抓取 tmux 画面。
func (r Runtime) Capture() (string, error) {
	return r.exec("tmux", "capture-pane", "-p", "-t", Session)
}

// 已知会卡住交互模式的对话框（M0-5、R9）。
var dialogRe = regexp.MustCompile(`(?i)select login method|bypass permissions mode|choose the text style|do you trust the files|trust this folder`)

// SmokeCheck 轮询 tmux 画面和 status.json，确认 claude 已就绪且没有卡在对话框上。
// sessionStarted 和 agentExited 由调用方提供，用于读取宿主机上的 status.json。
// 会话现在不会随 claude 退出而消失，所以"启动后就退出"要靠 SessionEnd 或画面上的提示来判断。
func (r Runtime) SmokeCheck(timeout time.Duration, sessionStarted, agentExited func() bool) error {
	deadline := time.Now().Add(timeout)
	var screen string
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if !r.HasSession() {
			return fmt.Errorf("claude 启动后退出了（tmux 会话已结束）")
		}
		screen, _ = r.Capture()
		if agentExited() || strings.Contains(screen, ExitNotice) {
			return fmt.Errorf("claude 启动后立刻退出了，画面最后 20 行：\n%s", lastLines(screen, 20))
		}
		if m := dialogRe.FindString(screen); m != "" {
			return fmt.Errorf("claude 卡在首次启动对话框上（%q），预置字段可能已随版本变化（R9）：\n%s", m, lastLines(screen, 20))
		}
		if sessionStarted() {
			return nil
		}
	}
	return fmt.Errorf("%s 内没有等到 SessionStart，画面最后 20 行：\n%s", timeout, lastLines(screen, 20))
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
