package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// 绝大多数仓库没有版本文件，这时整条 mise 的路都不该走。
func TestVersionFiles(t *testing.T) {
	dir := t.TempDir()
	if got := VersionFiles(dir); got != nil {
		t.Fatalf("空仓库不该有版本文件：%v", got)
	}
	for _, f := range []string{".nvmrc", "rust-toolchain.toml"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 同名目录不算
	if err := os.Mkdir(filepath.Join(dir, "mise.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := VersionFiles(dir)
	if len(got) != 2 || got[0] != ".nvmrc" || got[1] != "rust-toolchain.toml" {
		t.Fatalf("%v", got)
	}
}
