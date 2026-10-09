package agent

import (
	"os"
	"path/filepath"
	"strings"

	"sandx/internal/docker"
)

// MiseDir 是 mise 的数据目录，挂的是全局共享的 sbx-mise volume（design §4.2）。
const MiseDir = "/home/agent/.local/share/mise"

// versionFiles 是会触发 mise 安装的项目文件（design §5.2）。
var versionFiles = []string{
	".tool-versions", "mise.toml", ".mise.toml",
	".nvmrc", ".python-version", ".ruby-version", "rust-toolchain.toml", "rust-toolchain",
}

// VersionFiles 返回 worktree 里存在的版本文件（按 versionFiles 的顺序）。
// 一个都没有时返回 nil——绝大多数仓库是这样的，这时整条 mise 的路都不走。
func VersionFiles(worktree string) []string {
	var out []string
	for _, f := range versionFiles {
		if st, err := os.Stat(filepath.Join(worktree, f)); err == nil && !st.IsDir() {
			out = append(out, f)
		}
	}
	return out
}

// MiseInstall 在容器里按项目的版本文件装运行时，结果落在共享的 sbx-mise 里。
// 返回 mise 的输出，失败时连同错误一起返回——装不上不该挡住 Task 启动
// （项目可能只是声明了一个 mise 装不了的运行时），由调用方降级成警告。
func (r Runtime) MiseInstall() (string, error) {
	return r.Docker.Exec(r.Container,
		docker.ExecOpts{User: "agent", Workdir: r.Worktree},
		"mise", "install", "--yes")
}

// MiseSummary 把 mise ls --current 的输出压成一行，给启动日志用。
func (r Runtime) MiseSummary() string {
	out, err := r.Docker.Exec(r.Container, docker.ExecOpts{User: "agent", Workdir: r.Worktree},
		"mise", "ls", "--current", "--no-header")
	if err != nil {
		return ""
	}
	var parts []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			parts = append(parts, f[0]+"@"+f[1])
		}
	}
	return strings.Join(parts, " ")
}
