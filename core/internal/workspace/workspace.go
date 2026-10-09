// Package workspace 解析 Workspace（一个 git 仓库）并管理 Task 的 worktree（ADR 0003）。
package workspace

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Trace 非空时打印执行的 git 命令（--verbose）。
var Trace io.Writer

// Workspace 是一个主仓库。在 worktree 里解析也会回到主仓库。
type Workspace struct {
	Root   string // 主仓库工作目录的绝对路径
	GitDir string // 主仓库 .git 的绝对路径
	ID     string // <basename>-<sha1(root)[:6]>
}

// Git 在 dir 下执行 git，返回去掉首尾空白的 stdout。
func Git(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	if Trace != nil {
		fmt.Fprintf(Trace, "+ git %s\n", strings.Join(full, " "))
	}
	cmd := exec.Command("git", full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(full, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Resolve 从 cwd 找到主仓库。
func Resolve(cwd string) (Workspace, error) {
	if _, err := Git(cwd, "rev-parse", "--show-toplevel"); err != nil {
		return Workspace{}, fmt.Errorf("the current directory is not inside a git repository: %w", err)
	}
	common, err := Git(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Workspace{}, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return Workspace{}, err
	}
	if filepath.Base(common) != ".git" {
		return Workspace{}, fmt.Errorf("unsupported repository layout (bare, or a custom GIT_DIR): %s", common)
	}
	root := filepath.Dir(common)
	return Workspace{Root: root, GitDir: common, ID: ID(root)}, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9-]+`)

// ID = <规范化 basename>-<sha1(abs_root)[:6]>。
func ID(root string) string {
	base := nonSlug.ReplaceAllString(strings.ToLower(filepath.Base(root)), "-")
	base = strings.Trim(base, "-")
	if len(base) > 24 {
		base = strings.TrimRight(base[:24], "-")
	}
	if base == "" {
		base = "repo"
	}
	sum := sha1.Sum([]byte(root))
	return base + "-" + hex.EncodeToString(sum[:])[:6]
}

var taskRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ValidateTaskName 校验 task 名。main 合法（表示仓库根），但不能用作分支型 Task。
func ValidateTaskName(name string) error {
	if !taskRe.MatchString(name) {
		return fmt.Errorf("invalid task name: %q (lowercase letters, digits and - only, starting with a letter or digit, 40 characters at most)", name)
	}
	return nil
}

// HeadRef 返回当前 HEAD 的提交号。
func (w Workspace) HeadRef() (string, error) {
	return Git(w.Root, "rev-parse", "HEAD")
}

// BranchExists 报告本地分支是否存在。
func (w Workspace) BranchExists(branch string) bool {
	_, err := Git(w.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// isWorktree 报告 path 是否是本仓库已登记的 worktree。
func (w Workspace) isWorktree(path string) (bool, error) {
	out, err := Git(w.Root, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	real := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		real = r
	}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && (p == path || p == real) {
			return true, nil
		}
	}
	return false, nil
}

// EnsureWorktree 准备 Task 的 worktree：
//   - 已存在：复用；
//   - 分支已存在但 worktree 不存在：挂上已有分支，note 给出提示；
//   - 都不存在：从 base 新建分支。
func (w Workspace) EnsureWorktree(path, branch, base string) (created bool, note string, err error) {
	ok, err := w.isWorktree(path)
	if err != nil {
		return false, "", err
	}
	if ok {
		return false, "", nil
	}
	if _, err := os.Stat(path); err == nil {
		return false, "", fmt.Errorf("%s already exists but is not a worktree of this repository; sort it out by hand", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, "", err
	}
	if w.BranchExists(branch) {
		if _, err := Git(w.Root, "worktree", "add", path, branch); err != nil {
			return false, "", err
		}
		return true, fmt.Sprintf("branch %s already exists and has been attached to the new worktree (--base ignored)", branch), nil
	}
	if base == "" {
		base = "HEAD"
	}
	if _, err := Git(w.Root, "worktree", "add", "-b", branch, path, base); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// RemoveWorktree 删除 worktree（保留分支）。
func (w Workspace) RemoveWorktree(path string, force bool) error {
	ok, err := w.isWorktree(path)
	if err != nil || !ok {
		return err
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, err = Git(w.Root, append(args, path)...)
	return err
}

// Dirty 返回 worktree 中未提交的改动（git status --porcelain）。
func Dirty(dir string) (string, error) {
	return Git(dir, "status", "--porcelain")
}
