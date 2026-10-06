package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(dir, "My Repo")
	os.MkdirAll(root, 0o755)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	return root
}

func TestResolveAndID(t *testing.T) {
	root := initRepo(t)
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	ws, err := Resolve(filepath.Join(root, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if ws.Root != root || ws.GitDir != filepath.Join(root, ".git") {
		t.Fatalf("%+v", ws)
	}
	if !strings.HasPrefix(ws.ID, "my-repo-") || len(ws.ID) != len("my-repo-")+6 {
		t.Fatal(ws.ID)
	}
	if ID(root) != ws.ID {
		t.Fatal("ID not stable")
	}
}

func TestResolveNotRepo(t *testing.T) {
	if _, err := Resolve(t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	root := initRepo(t)
	ws, _ := Resolve(root)
	wt := filepath.Join(filepath.Dir(root), "wt", "t1")

	created, note, err := ws.EnsureWorktree(wt, "sbx/t1", "")
	if err != nil || !created || note != "" {
		t.Fatal(created, note, err)
	}
	// 复用
	created, _, err = ws.EnsureWorktree(wt, "sbx/t1", "")
	if err != nil || created {
		t.Fatal(created, err)
	}
	// 在 worktree 里解析回到主仓库
	ws2, err := Resolve(wt)
	if err != nil || ws2.ID != ws.ID || ws2.Root != root {
		t.Fatalf("%+v %v", ws2, err)
	}
	// 脏检查
	os.WriteFile(filepath.Join(wt, "x"), []byte("x"), 0o644)
	if d, _ := Dirty(wt); d == "" {
		t.Fatal("expected dirty")
	}
	if err := ws.RemoveWorktree(wt, false); err == nil {
		t.Fatal("remove dirty worktree without force should fail")
	}
	if err := ws.RemoveWorktree(wt, true); err != nil {
		t.Fatal(err)
	}
	if !ws.BranchExists("sbx/t1") {
		t.Fatal("branch should be kept")
	}
	// 分支已存在、worktree 不存在 → 挂上已有分支
	created, note, err = ws.EnsureWorktree(wt, "sbx/t1", "")
	if err != nil || !created || note == "" {
		t.Fatal(created, note, err)
	}
}

func TestValidateTaskName(t *testing.T) {
	for _, ok := range []string{"main", "t1", "fix-login", "a"} {
		if ValidateTaskName(ok) != nil {
			t.Error(ok)
		}
	}
	for _, bad := range []string{"", "-a", "A", "a_b", "a.b", strings.Repeat("a", 41)} {
		if ValidateTaskName(bad) == nil {
			t.Error(bad)
		}
	}
}
