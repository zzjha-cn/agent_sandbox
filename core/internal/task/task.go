// Package task 定义 Task 的命名、路径、meta 和状态推导（ADR 0002）。
package task

import (
	"crypto/sha256"
	"encoding/hex"
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
	// ExecutionDir is set only by the general-task application adapter. Legacy
	// repository tasks keep their original main/worktree path rules.
	ExecutionDir string
}

// NewExecution names a general task without discovering or initializing Git.
func NewExecution(home, name string) (Task, error) {
	if err := workspace.ValidateTaskName(name); err != nil {
		return Task{}, err
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return Task{}, err
	}
	dir := filepath.Join(abs, "workspaces", "tasks", name)
	return Task{Name: name, Home: abs, WS: workspace.Workspace{ID: "tasks", Root: dir}, ExecutionDir: dir}, nil
}

func New(home string, ws workspace.Workspace, name string) (Task, error) {
	if err := workspace.ValidateTaskName(name); err != nil {
		return Task{}, err
	}
	return Task{Name: name, WS: ws, Home: home}, nil
}

// IsMain 报告是否是直接使用仓库根的 main Task。
func (t Task) IsMain() bool { return t.Name == "main" && t.ExecutionDir == "" }

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
	if t.IsMain() || t.ExecutionDir != "" {
		return ""
	}
	return "sbx/" + t.Name
}

// Worktree 返回 Task 的工作目录。
func (t Task) Worktree() string {
	if t.ExecutionDir != "" {
		return t.ExecutionDir
	}
	if t.IsMain() {
		return t.WS.Root
	}
	return filepath.Join(t.Home, "worktrees", t.WS.ID, t.Name)
}

func (t Task) StateDir() string { return filepath.Join(t.Home, "state", t.WS.ID, t.Name) }
func (t Task) GenDir() string   { return filepath.Join(t.StateDir(), "gen") }

func (t Task) Labels() map[string]string {
	labels := map[string]string{"sbx.ws": t.WS.ID, "sbx.task": t.Name}
	if t.ExecutionDir != "" {
		labels["sbx.execution-home"] = t.ExecutionHomeKey()
	}
	return labels
}

// Meta 是创建 Task 时的参数，存在 state/meta.json。
type Meta struct {
	RuntimeRetired           bool     `json:"runtime_retired,omitempty"`
	OrganizationPolicyDigest string   `json:"organization_policy_digest,omitempty"`
	Namespace                string   `json:"namespace,omitempty"`
	Owner                    string   `json:"owner,omitempty"`
	Agent                    string   `json:"agent,omitempty"`
	Task                     string   `json:"task"`
	WS                       string   `json:"ws"`
	Root                     string   `json:"root"`
	Base                     string   `json:"base"`
	Profile                  string   `json:"profile"`
	Image                    string   `json:"image"`
	Proxy                    string   `json:"proxy"`
	TaskID                   string   `json:"task_id"`
	DepMasks                 []string `json:"dep_masks"`
	// Services 是跟这个 Task 一起起来的伴生容器名（M4-11）。
	Services []string `json:"services,omitempty"`
	// Forwards 记下这个 Task 接过哪些内部服务（M4-14、boundary.md §3.4）。
	// 事后 review 这条分支时，这就是"该用什么尺度看"的依据——预期管理要落成
	// 可查的记录，不能只存在于当时那一屏输出里。
	Forwards []Link `json:"forwards,omitempty"`
	// PrepTag 是播种这个 Task 的依赖 volume 时用的 prep 镜像（M4-8）。
	// 空表示没有 prep，或者 volume 是在 prep 之前建的。
	PrepTag string `json:"prep_tag,omitempty"`
	// PrepAllow 是 prep 镜像声明过的放行域名（M4-15）。记在这里，resume 就不必
	// 再起一个容器把镜像读一遍，镜像被删掉也不会让白名单悄悄少几条。
	PrepAllow []string  `json:"prep_allow,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Link 是一条生效过的 forward。
type Link struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	To   string `json:"to"`             // 解析之后的实际地址
	Host string `json:"host,omitempty"` // --add-host 给的域名
	// Internal 为 true 表示目标是内网共享资源（boundary.md §3.1 的 🔴），
	// false 是宿主机自己（🟡）。
	Internal bool `json:"internal"`
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

// ExecutionHomeKey separates equal general task names in different local homes.
func (t Task) ExecutionHomeKey() string {
	abs, _ := filepath.Abs(t.Home)
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(sum[:])
}
