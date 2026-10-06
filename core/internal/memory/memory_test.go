package memory

import (
	"path/filepath"
	"reflect"
	"testing"
)

func kinds(acts []Action) map[string]string {
	m := map[string]string{}
	for _, a := range acts {
		m[a.File] = a.Kind
	}
	return m
}

func TestKey(t *testing.T) {
	cases := map[string]string{
		"/Users/apple/workspace/my/m-skill/m-skill":     "-Users-apple-workspace-my-m-skill-m-skill",
		"/Users/apple/.sbx/worktrees/fixture-9359a5/t1": "-Users-apple--sbx-worktrees-fixture-9359a5-t1",
		"/private/var/T/sbx-e2e.3wwXXJ/fixture":         "-private-var-T-sbx-e2e-3wwXXJ-fixture",
	}
	for in, want := range cases {
		if got := Key(in); got != want {
			t.Errorf("Key(%q)=%q want %q", in, got, want)
		}
	}
}

func TestPlanThreeWay(t *testing.T) {
	base := Base{"same.md": hash([]byte("s")), "srcchg.md": hash([]byte("old")), "dstchg.md": hash([]byte("old")),
		"both.md": hash([]byte("old")), "deleted.md": hash([]byte("d")), "MEMORY.md": hash([]byte("- a\n"))}
	src := Files{"same.md": []byte("s"), "new.md": []byte("n"), "srcchg.md": []byte("new"), "dstchg.md": []byte("old"),
		"both.md": []byte("src"), "deleted.md": []byte("d"), "MEMORY.md": []byte("- a\n- b\n")}
	dst := Files{"same.md": []byte("s"), "srcchg.md": []byte("old"), "dstchg.md": []byte("mine"),
		"both.md": []byte("dst"), "MEMORY.md": []byte("- a\n- c\n")}
	acts, next := Plan(src, dst, base)
	want := map[string]string{"same.md": "same", "new.md": "add", "srcchg.md": "update", "dstchg.md": "keep-dst",
		"both.md": "conflict", "deleted.md": "skip-deleted", "MEMORY.md": "merge"}
	if got := kinds(acts); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	ch := Changes(acts)
	if string(ch["MEMORY.md"]) != "- a\n- c\n- b\n" || string(ch["new.md"]) != "n" || string(ch["srcchg.md"]) != "new" || len(ch) != 3 {
		t.Fatalf("changes %q", ch)
	}
	if _, ok := next["deleted.md"]; ok {
		t.Fatal("deleted should leave base")
	}
	if next["both.md"] != base["both.md"] || next["dstchg.md"] != base["dstchg.md"] {
		t.Fatal("conflict/keep-dst must not move base")
	}

	// 应用后反向同步：dst 改过的、合并过的回到 src；冲突的仍是冲突
	for n, b := range ch {
		dst[n] = b
	}
	back, _ := Plan(dst, src, next)
	bk := kinds(back)
	if bk["dstchg.md"] != "update" || bk["MEMORY.md"] != "update" || bk["both.md"] != "conflict" || bk["new.md"] != "same" {
		t.Fatalf("reverse %v", bk)
	}
}

func TestHostDirIO(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if f, err := ReadDir(dir); err != nil || len(f) != 0 {
		t.Fatal(f, err)
	}
	if err := WriteDir(dir, Files{"a.md": []byte("x")}); err != nil {
		t.Fatal(err)
	}
	f, _ := ReadDir(dir)
	if string(f["a.md"]) != "x" || len(f) != 1 {
		t.Fatal(f)
	}
	bp := filepath.Join(t.TempDir(), "k.json")
	SaveBase(bp, Base{"a.md": "h"})
	if b, _ := LoadBase(bp); b["a.md"] != "h" {
		t.Fatal(b)
	}
}
