// Package agent 生成 Agent 需要的文件（hooks、settings、宿主机配置快照），
// 计算容器的挂载和环境变量，并在容器里预置首次启动状态、拉起 tmux（ADR 0004、0006）。
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"sandx/assets"
	"sandx/internal/docker"
	"sandx/internal/fsutil"
)

// HostClaude 描述宿主机 ~/.claude 下要带进容器的内容。
type HostClaude struct {
	Dir      string   // 宿主机 ~/.claude
	HasMD    bool     // 存在 CLAUDE.md
	Dirs     []string // 存在的子目录：skills / agents / commands
	Warnings []string
}

var hostDirs = []string{"skills", "agents", "commands"}

// InspectHostClaude 检查宿主机 ~/.claude。skills 等目录里指向目录外的软链接在容器里会失效，
// 只给出警告（WP7.2）。
func InspectHostClaude(dir string) HostClaude {
	h := HostClaude{Dir: dir}
	if st, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err == nil && st.Mode().IsRegular() {
		h.HasMD = true
	}
	for _, name := range hostDirs {
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			continue
		}
		h.Dirs = append(h.Dirs, name)
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if real != p {
			h.Warnings = append(h.Warnings, fmt.Sprintf("~/.claude/%s 本身是软链接（→ %s），按实际目录挂载", name, real))
		}
		filepath.WalkDir(real, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil || !strings.HasPrefix(target, real+string(filepath.Separator)) {
				rel, _ := filepath.Rel(real, path)
				h.Warnings = append(h.Warnings, fmt.Sprintf("~/.claude/%s/%s 是指向外部的软链接，在容器里不可用", name, rel))
			}
			return nil
		})
	}
	return h
}

// hookEvents 是状态 hooks（M0-5、design §7.2）。
var hookEvents = []struct{ event, state, matcher string }{
	{"SessionStart", "idle", ""},
	{"UserPromptSubmit", "running", ""},
	{"PreToolUse", "running", "*"},
	{"Stop", "idle", ""},
	{"Notification", "idle", ""},
}

// SettingsJSON 生成通过 --settings 注入的 settings.sbx.json。
func SettingsJSON() []byte {
	hooks := map[string]any{}
	for _, h := range hookEvents {
		entry := map[string]any{"hooks": []any{map[string]string{
			"type": "command", "command": "/sbx/gen/hooks/status.sh " + h.state,
		}}}
		if h.matcher != "" {
			entry["matcher"] = h.matcher
		}
		hooks[h.event] = []any{entry}
	}
	b, _ := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	return append(b, '\n')
}

// RenderGen 原子写入 gen 目录（容器内只读挂载到 /sbx/gen）。
func RenderGen(genDir string, h HostClaude) error {
	if err := fsutil.AtomicWrite(filepath.Join(genDir, "hooks", "status.sh"), assets.Read("agent-layer/hooks/status.sh"), 0o755); err != nil {
		return err
	}
	if err := fsutil.AtomicWrite(filepath.Join(genDir, "settings.sbx.json"), SettingsJSON(), 0o644); err != nil {
		return err
	}
	md := filepath.Join(genDir, "host-claude", "CLAUDE.md")
	if !h.HasMD {
		if err := os.Remove(md); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return os.MkdirAll(filepath.Dir(md), 0o755)
	}
	data, err := os.ReadFile(filepath.Join(h.Dir, "CLAUDE.md"))
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(md, data, 0o644)
}

// MountInput 是计算挂载表需要的信息。
type MountInput struct {
	Worktree  string
	GitDir    string // worktree Task 需要；main Task 留空（已包含在仓库根里）
	DepMasks  []string
	DepVolume func(i int) string
	StateDir  string
	GenDir    string
	Host      HostClaude
}

// Mounts 生成 agent 容器的挂载表（design §4.2）。依赖 volume 排在 worktree 之后才能遮盖。
func Mounts(in MountInput) []docker.Mount {
	m := []docker.Mount{{Source: in.Worktree, Target: in.Worktree}}
	if in.GitDir != "" {
		m = append(m, docker.Mount{Source: in.GitDir, Target: in.GitDir})
	}
	for i, p := range in.DepMasks {
		m = append(m, docker.Mount{Source: in.DepVolume(i), Target: filepath.Join(in.Worktree, p), Volume: true})
	}
	m = append(m,
		docker.Mount{Source: "sbx-home", Target: "/home/agent", Volume: true},
		docker.Mount{Source: "sbx-cache", Target: "/sbx/cache", Volume: true},
		docker.Mount{Source: in.StateDir, Target: "/sbx/state"},
		docker.Mount{Source: in.GenDir, Target: "/sbx/gen", ReadOnly: true},
	)
	for _, d := range in.Host.Dirs {
		src := filepath.Join(in.Host.Dir, d)
		if real, err := filepath.EvalSymlinks(src); err == nil {
			src = real
		}
		m = append(m, docker.Mount{Source: src, Target: "/sbx/host-claude/" + d, ReadOnly: true})
	}
	return m
}

// EnvInput 是计算环境变量需要的信息。
type EnvInput struct {
	ProxyURL         string
	GitName, GitMail string
	WS, Task         string
}

// Env 生成 agent 容器的环境变量。
func Env(in EnvInput) []string {
	env := []string{
		"HTTP_PROXY=" + in.ProxyURL, "HTTPS_PROXY=" + in.ProxyURL,
		"http_proxy=" + in.ProxyURL, "https_proxy=" + in.ProxyURL,
		"NO_PROXY=localhost,127.0.0.1", "no_proxy=localhost,127.0.0.1",
		"SBX_WS=" + in.WS, "SBX_TASK=" + in.Task,
	}
	if in.GitName != "" {
		env = append(env, "GIT_AUTHOR_NAME="+in.GitName, "GIT_COMMITTER_NAME="+in.GitName)
	}
	if in.GitMail != "" {
		env = append(env, "GIT_AUTHOR_EMAIL="+in.GitMail, "GIT_COMMITTER_EMAIL="+in.GitMail)
	}
	return env
}
