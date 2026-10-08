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
	// claude 退出（Ctrl-D、/exit、崩溃）后把状态标成 exited，
	// 否则 sbx ls 会一直停在最后一次 hook 写下的 idle。
	{"SessionEnd", "exited", ""},
}

// SettingsJSON 生成通过 --settings 注入的 settings.sbx.json。
// blockCloudMCP 为真时写入 disableClaudeAiConnectors：代理那边已经拦了
// mcp-proxy.anthropic.com（ADR 0015），不从源头关掉的话 claude 会反复重试，
// 实测一分钟内撞出三百多次 403（R11）。
func SettingsJSON(blockCloudMCP bool) []byte {
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
	out := map[string]any{"hooks": hooks}
	// 状态栏脚本和 hooks 一样从 /sbx/gen 注入：沙箱里没有宿主机的 ~/.claude/scripts。
	out["statusLine"] = map[string]string{"type": "command", "command": "/sbx/gen/statusline.sh"}
	if blockCloudMCP {
		out["disableClaudeAiConnectors"] = true
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return append(b, '\n')
}

// RenderGen 原子写入 gen 目录（容器内只读挂载到 /sbx/gen）。
func RenderGen(genDir string, h HostClaude, blockCloudMCP bool) error {
	if err := fsutil.AtomicWrite(filepath.Join(genDir, "hooks", "status.sh"), assets.Read("agent-layer/hooks/status.sh"), 0o755); err != nil {
		return err
	}
	if err := fsutil.AtomicWrite(filepath.Join(genDir, "statusline.sh"), assets.Read("agent-layer/statusline.sh"), 0o755); err != nil {
		return err
	}
	if err := fsutil.AtomicWrite(filepath.Join(genDir, "settings.sbx.json"), SettingsJSON(blockCloudMCP), 0o644); err != nil {
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
	TZ               string // 宿主机时区名，空则留给容器默认（UTC）
	APIKeyEnv        string // 注入 API key 的环境变量名，空表示走订阅登录（M2-13）
	APIKey           string
}

// Env 生成 agent 容器的环境变量。
func Env(in EnvInput) []string {
	env := []string{
		"HTTP_PROXY=" + in.ProxyURL, "HTTPS_PROXY=" + in.ProxyURL,
		"http_proxy=" + in.ProxyURL, "https_proxy=" + in.ProxyURL,
		"NO_PROXY=localhost,127.0.0.1", "no_proxy=localhost,127.0.0.1",
		"SBX_WS=" + in.WS, "SBX_TASK=" + in.Task,
	}
	// 容器默认 UTC，和宿主机差几个小时。events.log、git 提交时间、构建日志
	// 都会对不上，事后判读很容易读错（实测差 8 小时）。镜像里已经有 tzdata。
	if in.TZ != "" {
		env = append(env, "TZ="+in.TZ)
	}
	if in.GitName != "" {
		env = append(env, "GIT_AUTHOR_NAME="+in.GitName, "GIT_COMMITTER_NAME="+in.GitName)
	}
	if in.GitMail != "" {
		env = append(env, "GIT_AUTHOR_EMAIL="+in.GitMail, "GIT_COMMITTER_EMAIL="+in.GitMail)
	}
	// API key 配了就注入，优先级高于 sbx-home 里的订阅登录（design §7.1）。
	// 注意它会留在容器的 Config 里，docker inspect 看得到——这是环境变量注入的固有代价。
	if in.APIKeyEnv != "" && in.APIKey != "" {
		env = append(env, in.APIKeyEnv+"="+in.APIKey)
	}
	return env
}

// HostTZ 返回宿主机的时区名（如 Asia/Shanghai）。取不到时返回空字符串。
// macOS 的 /etc/localtime 指向 /var/db/timezone/zoneinfo/<zone>，Linux 指向 /usr/share/zoneinfo/<zone>。
func HostTZ() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	link, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	if _, zone, ok := strings.Cut(link, "zoneinfo/"); ok {
		return zone
	}
	return ""
}
