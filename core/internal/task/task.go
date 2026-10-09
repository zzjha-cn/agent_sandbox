// Package task 定义 Task 的命名、路径、meta 和状态推导（ADR 0002）。
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sandx/internal/docker"
	"sandx/internal/fsutil"
	"sandx/internal/workspace"
)

// Home 返回 sbx 数据目录：$SBX_HOME，默认 ~/.sbx。
func Home() (string, error) {
	if h := os.Getenv("SBX_HOME"); h != "" {
		return filepath.Abs(h)
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".sbx"), nil
}

// Task 是 Workspace 下的一个任务。
type Task struct {
	Name string
	WS   workspace.Workspace
	Home string
}

func New(home string, ws workspace.Workspace, name string) (Task, error) {
	if err := workspace.ValidateTaskName(name); err != nil {
		return Task{}, err
	}
	return Task{Name: name, WS: ws, Home: home}, nil
}

// IsMain 报告是否是直接使用仓库根的 main Task。
func (t Task) IsMain() bool { return t.Name == "main" }

func (t Task) prefix() string { return "sbx-" + t.WS.ID + "-" + t.Name }

func (t Task) Container() string { return t.prefix() }
func (t Task) Network() string   { return t.prefix() + "-net" }

// Proxy 是 dedicated 模式下这个 Task 独占的 squid 容器名（design §6.2）。
func (t Task) Proxy() string { return t.prefix() + "-proxy" }

// ProxyDir 是 dedicated 实例的配置目录，整个目录只读挂进容器。
func (t Task) ProxyDir() string { return filepath.Join(t.StateDir(), "proxy") }
func (t Task) DepVolume(i int) string {
	return fmt.Sprintf("%s-dep-%d", t.prefix(), i)
}

// ID 是 shared proxy 上的用户名：<ws>.<task>。
func (t Task) ID() string { return t.WS.ID + "." + t.Name }

// Branch 是 Task 的分支；main Task 没有专属分支。
func (t Task) Branch() string {
	if t.IsMain() {
		return ""
	}
	return "sbx/" + t.Name
}

// Worktree 返回 Task 的工作目录。
func (t Task) Worktree() string {
	if t.IsMain() {
		return t.WS.Root
	}
	return filepath.Join(t.Home, "worktrees", t.WS.ID, t.Name)
}

func (t Task) StateDir() string { return filepath.Join(t.Home, "state", t.WS.ID, t.Name) }
func (t Task) GenDir() string   { return filepath.Join(t.StateDir(), "gen") }

func (t Task) Labels() map[string]string {
	return map[string]string{"sbx.ws": t.WS.ID, "sbx.task": t.Name}
}

// Meta 是创建 Task 时的参数，存在 state/meta.json。
type Meta struct {
	Task      string    `json:"task"`
	WS        string    `json:"ws"`
	Root      string    `json:"root"`
	Base      string    `json:"base"`
	Profile   string    `json:"profile"`
	Image     string    `json:"image"`
	Proxy     string    `json:"proxy"`
	TaskID    string    `json:"task_id"`
	DepMasks  []string  `json:"dep_masks"`
	CreatedAt time.Time `json:"created_at"`
}

func (t Task) WriteMeta(m Meta) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return fsutil.AtomicWrite(filepath.Join(t.StateDir(), "meta.json"), append(b, '\n'), 0o644)
}

// ReadMeta 读取 meta；不存在时 ok=false。
func (t Task) ReadMeta() (m Meta, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(t.StateDir(), "meta.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	return m, true, json.Unmarshal(b, &m)
}

// AgentStatus 是 hooks 写入的 status.json。
type AgentStatus struct {
	State string `json:"state"`
	Event string `json:"event"`
	TS    int64  `json:"ts"`
}

// ReadStatus 读取 status.json；不存在时返回 nil。
func (t Task) ReadStatus() (*AgentStatus, error) {
	b, err := os.ReadFile(filepath.Join(t.StateDir(), "status.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s AgentStatus
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse status.json: %w", err)
	}
	return &s, nil
}

// RunLog 是 headless 的输出，RunExit 里是它的退出码（M3-7）。
func (t Task) RunLogPath() string  { return filepath.Join(t.StateDir(), "run.log") }
func (t Task) RunExitPath() string { return filepath.Join(t.StateDir(), "run.exit") }

// RunExit 返回上一次 headless 运行的退出码；没跑过或还没结束时返回 nil。
func (t Task) RunExit() *int {
	b, err := os.ReadFile(t.RunExitPath())
	if err != nil {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return nil
	}
	return &n
}

// HasPriorSession 报告这个 Task 以前跑过 claude（hooks 写过 events.log），
// 用于决定重新拉起时要不要带 --continue。
func (t Task) HasPriorSession() bool {
	_, err := os.Stat(filepath.Join(t.StateDir(), "events.log"))
	return err == nil
}

// Derive 根据容器状态、status.json 和 headless 的退出码推导 Task 状态：
// absent / stopped / exited(<code>) / exited(oom) / exited(agent) / starting / running / idle。
// runExit 非 nil 表示这个 Task 跑完过一次 headless（M3-7）。
func Derive(st docker.State, exists bool, s *AgentStatus, runExit *int) string {
	if !exists {
		return "absent"
	}
	if !st.Running {
		switch {
		case st.OOMKilled:
			return "exited(oom)"
		// headless 跑完是容器自己 kill 1 停的，容器的退出码只是 SIGTERM，
		// 真正有意义的是 claude 的退出码
		case runExit != nil:
			return fmt.Sprintf("exited(%d)", *runExit)
		case st.Status == "created" || (st.Status == "exited" && (st.ExitCode == 0 || st.ExitCode == 143 || st.ExitCode == 137)):
			// docker stop 会以 SIGTERM/SIGKILL 结束，视为正常停止
			return "stopped"
		case st.Status == "exited":
			return fmt.Sprintf("exited(%d)", st.ExitCode)
		default:
			return st.Status
		}
	}
	if s == nil {
		return "starting"
	}
	switch s.State {
	case "running", "idle":
		return s.State
	case "exited":
		// 容器还在，但容器里的 claude 已经退出（tmux 会话里现在是 shell）
		return "exited(agent)"
	default:
		return "starting"
	}
}
