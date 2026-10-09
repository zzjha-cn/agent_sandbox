package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sandx/internal/task"
	"sandx/internal/workspace"
)

func writeMeta(t *testing.T, home, ws, name string, m task.Meta) {
	t.Helper()
	tk := task.Task{Name: name, WS: workspace.Workspace{ID: ws}, Home: home}
	if err := os.MkdirAll(tk.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := tk.WriteMeta(m); err != nil {
		t.Fatal(err)
	}
}

// --all 要跨 Workspace 列出，并且从 meta.json 还原出每个 Workspace 的仓库路径。
func TestAllTaskRefs(t *testing.T) {
	home := t.TempDir()
	a := &App{Home: home, Docker: nil}
	writeMeta(t, home, "shop-aaa111", "fix", task.Meta{Task: "fix", WS: "shop-aaa111", Root: "/repos/shop", CreatedAt: time.Now()})
	writeMeta(t, home, "shop-aaa111", "main", task.Meta{Task: "main", WS: "shop-aaa111", Root: "/repos/shop"})
	writeMeta(t, home, "blog-bbb222", "main", task.Meta{Task: "main", WS: "blog-bbb222", Root: "/repos/blog"})
	// 没有 meta 的 state 目录也要列出来（Task 建到一半、或者 meta 被删了）
	if err := os.MkdirAll(filepath.Join(home, "state", "blog-bbb222", "draft"), 0o755); err != nil {
		t.Fatal(err)
	}

	refs, err := a.allTaskRefsFromState()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range refs {
		got = append(got, r.WS.ID+"/"+r.Name+"@"+r.WS.Root)
	}
	want := []string{
		"blog-bbb222/draft@/repos/blog",
		"blog-bbb222/main@/repos/blog",
		"shop-aaa111/fix@/repos/shop",
		"shop-aaa111/main@/repos/shop",
	}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个是 %s，期望 %s", i, got[i], want[i])
		}
	}
}
