package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotRestoreDoesNotFollowReplacedDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	gen := filepath.Join(root, "gen")
	hooks := filepath.Join(gen, "hooks")
	outside := filepath.Join(root, "outside")
	for _, p := range []string{hooks, outside} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(hooks, "status.sh"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "status.sh"), []byte("keep outside"), 0600); err != nil {
		t.Fatal(err)
	}
	restore, err := SnapshotPath(gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(hooks); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, hooks); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(outside, "status.sh"))
	if err != nil || string(got) != "keep outside" {
		t.Fatal("restore followed an injected symlink")
	}
	info, err := os.Lstat(hooks)
	if err != nil || !info.IsDir() {
		t.Fatal("original directory not restored")
	}
	got, err = os.ReadFile(filepath.Join(hooks, "status.sh"))
	if err != nil || string(got) != "original" {
		t.Fatal("old state not restored")
	}
}
