package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scan(t *testing.T, root string) Snapshot {
	t.Helper()
	s, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNoSbxDirIsEmpty(t *testing.T) {
	s := scan(t, t.TempDir())
	if !s.Empty() {
		t.Fatalf("没有 .sbx/ 的仓库应该是空快照，得到 %v", s.Files)
	}
}

func TestHashFollowsContent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sandbox.toml", "[network]\nallow = [\"a.test\"]\n")
	first := scan(t, root).Hash

	// 重新扫描内容没变，哈希必须稳定
	if again := scan(t, root).Hash; again != first {
		t.Fatalf("内容没变哈希却变了：%s → %s", first, again)
	}
	write(t, root, "sandbox.toml", "[network]\nallow = [\"b.test\"]\n")
	if changed := scan(t, root).Hash; changed == first {
		t.Fatal("改了内容哈希却没变")
	}
}

// 路径也要进哈希：同样的内容换个文件名是另一回事。
func TestHashFollowsPath(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write(t, a, "sandbox.toml", "x = 1\n")
	write(t, b, "Dockerfile", "x = 1\n")
	if scan(t, a).Hash == scan(t, b).Hash {
		t.Fatal("路径不同的文件算出了同一个哈希")
	}
}

func TestNestedFileCounts(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sandbox.toml", "x = 1\n")
	before := scan(t, root).Hash
	write(t, root, "hooks/post.sh", "echo hi\n")
	s := scan(t, root)
	if s.Hash == before {
		t.Fatal("子目录里新增的文件没有进哈希")
	}
	if _, ok := s.Files["hooks/post.sh"]; !ok {
		t.Fatalf("子目录文件没被记录：%v", s.Paths())
	}
}

// 符号链接按链接本身记录，不跟随：否则换掉链接目标就能绕过信任。
func TestSymlinkRecordedAsLink(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sandbox.toml", "x = 1\n")
	outside := filepath.Join(root, "real.txt")
	os.WriteFile(outside, []byte("hello\n"), 0o644)
	link := filepath.Join(root, Dir, "Dockerfile")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("这个文件系统不支持符号链接")
	}
	s := scan(t, root)
	f := s.Files["Dockerfile"]
	if !f.Link || f.Text != outside {
		t.Fatalf("符号链接没有按链接记录：%+v", f)
	}
	// 改链接目标的内容，哈希不该变（我们记的是链接）；换链接目标才算变
	os.WriteFile(outside, []byte("different\n"), 0o644)
	if scan(t, root).Hash != s.Hash {
		t.Fatal("跟随了符号链接的内容")
	}
	os.Remove(link)
	os.Symlink(filepath.Join(root, "other.txt"), link)
	if scan(t, root).Hash == s.Hash {
		t.Fatal("换了链接目标哈希却没变")
	}
}

func TestDiffKinds(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sandbox.toml", "a = 1\n")
	write(t, root, "gone.txt", "bye\n")
	old := scan(t, root)

	write(t, root, "sandbox.toml", "a = 2\n")
	os.Remove(filepath.Join(root, Dir, "gone.txt"))
	write(t, root, "new.txt", "hi\n")
	cur := scan(t, root)

	got := map[string]Kind{}
	for _, c := range Diff(old, cur) {
		got[c.Path] = c.Kind
	}
	want := map[string]Kind{"sandbox.toml": Modified, "gone.txt": Removed, "new.txt": Added}
	for p, k := range want {
		if got[p] != k {
			t.Errorf("%s：想要 %s，得到 %q", p, k, got[p])
		}
	}
	if len(got) != len(want) {
		t.Errorf("多出了变更：%v", got)
	}
}

func TestDiffCarriesContent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sandbox.toml", "a = 1\n")
	old := scan(t, root)
	write(t, root, "sandbox.toml", "a = 2\n")
	cs := Diff(old, scan(t, root))
	if len(cs) != 1 || string(cs[0].Old) != "a = 1\n" || string(cs[0].New) != "a = 2\n" {
		t.Fatalf("快照里应该带着新旧内容，得到 %+v", cs)
	}
}

// 二进制文件只记哈希：变更能被发现，但给不出 diff。
func TestBinaryIsTrackedWithoutContent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "blob.bin", "\x00\x01\x02")
	old := scan(t, root)
	if old.Files["blob.bin"].Omitted != "binary" {
		t.Fatalf("没识别出二进制：%+v", old.Files["blob.bin"])
	}
	write(t, root, "blob.bin", "\x00\x01\x03")
	cs := Diff(old, scan(t, root))
	if len(cs) != 1 || cs[0].Kind != Modified || cs[0].New != nil || !strings.Contains(cs[0].Note, "二进制") {
		t.Fatalf("二进制变更应该被报出来但不带内容：%+v", cs)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sandbox.toml", "a = 1\n")
	s := scan(t, root)
	p := Path(t.TempDir(), "demo-abc123")

	if _, had, err := Load(p); err != nil || had {
		t.Fatalf("还没保存就读到了：had=%v err=%v", had, err)
	}
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	got, had, err := Load(p)
	if err != nil || !had {
		t.Fatalf("读不回来：had=%v err=%v", had, err)
	}
	if got.Hash != s.Hash || len(Diff(got, s)) != 0 {
		t.Fatalf("存回来的快照和原来不一样：%+v", got)
	}
	if got.TrustedAt.IsZero() {
		t.Error("Save 应该记下确认时间")
	}
}
