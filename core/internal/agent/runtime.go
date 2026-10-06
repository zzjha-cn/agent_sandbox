package agent

import (
	"encoding/json"
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
	var st struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if jerr := json.Unmarshal([]byte(out), &st); jerr != nil {
		if err != nil {
			return false, err
		}
		return false, fmt.Errorf("无法解析 claude auth status 输出：%s", out)
	}
	return st.LoggedIn, nil
}

// HasSession 报告 tmux 会话是否存在。
func (r Runtime) HasSession() bool {
	_, err := r.exec("tmux", "has-session", "-t", Session)
	return err == nil
}

// StartClaude 在 tmux 会话里拉起 claude；会话已存在时什么都不做。
func (r Runtime) StartClaude() (started bool, err error) {
	if r.HasSession() {
		return false, nil
	}
	_, err = r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent", Workdir: r.Worktree},
		"tmux", "new-session", "-d", "-s", Session, "-x", "220", "-y", "50",
		"claude --dangerously-skip-permissions --settings /sbx/gen/settings.sbx.json")
	return err == nil, err
}

// Capture 抓取 tmux 画面。
func (r Runtime) Capture() (string, error) {
	return r.exec("tmux", "capture-pane", "-p", "-t", Session)
}

// 已知会卡住交互模式的对话框（M0-5、R9）。
var dialogRe = regexp.MustCompile(`(?i)select login method|bypass permissions mode|choose the text style|do you trust the files|trust this folder`)

// SmokeCheck 轮询 tmux 画面和 status.json，确认 claude 已就绪且没有卡在对话框上。
// sessionStarted 由调用方提供，用于读取宿主机上的 status.json。
func (r Runtime) SmokeCheck(timeout time.Duration, sessionStarted func() bool) error {
	deadline := time.Now().Add(timeout)
	var screen string
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		if !r.HasSession() {
			return fmt.Errorf("claude 启动后退出了（tmux 会话已结束）")
		}
		screen, _ = r.Capture()
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
