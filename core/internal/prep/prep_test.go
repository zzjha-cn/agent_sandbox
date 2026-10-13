package prep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tag 必须对 watch 列出的文件的**内容**敏感：解析 Dockerfile 猜不出它 COPY 了什么，
// 改了 go.sum 而 Dockerfile 没动的话，不敏感就意味着种子悄悄陈旧了。
func TestTagTracksWatchedFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".sbx/prep.Dockerfile", "FROM scratch\n")
	write(t, root, "go.sum", "a\n")
	s := Spec{WSID: "demo-abc123", Root: root, File: ".sbx/prep.Dockerfile", Watch: []string{"go.sum"}}

	base, err := s.Tag()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(base, "sbx/prep-demo-abc123:") {
		t.Fatalf("tag 形状不对：%s", base)
	}
	write(t, root, "go.sum", "b\n")
	changed, _ := s.Tag()
	if changed == base {
		t.Fatal("watch 的文件变了，tag 必须变")
	}
	write(t, root, ".sbx/prep.Dockerfile", "FROM scratch\nRUN true\n")
	if df, _ := s.Tag(); df == changed {
		t.Fatal("Dockerfile 变了，tag 必须变")
	}

	// watch 的书写顺序不该影响 tag
	write(t, root, "go.mod", "m\n")
	s1 := Spec{WSID: "w", Root: root, File: ".sbx/prep.Dockerfile", Watch: []string{"go.sum", "go.mod"}}
	s2 := Spec{WSID: "w", Root: root, File: ".sbx/prep.Dockerfile", Watch: []string{"go.mod", "go.sum"}}
	a, _ := s1.Tag()
	b, _ := s2.Tag()
	if a != b {
		t.Fatal("watch 顺序不该影响 tag")
	}

	// 文件被删掉也是一次漂移
	withFile, _ := s1.Tag()
	os.Remove(filepath.Join(root, "go.sum"))
	gone, err := s1.Tag()
	if err != nil {
		t.Fatalf("watch 的文件不存在不该报错：%v", err)
	}
	if gone == withFile {
		t.Fatal("watch 的文件被删掉，tag 必须变")
	}
}

// Enabled 看的是 Dockerfile 在不在，不是配置里有没有值：
// prep.file 永远有默认值，拿它判断会让所有仓库都"配了 prep"。
func TestEnabled(t *testing.T) {
	root := t.TempDir()
	s := Spec{Root: root, File: ".sbx/prep.Dockerfile"}
	if s.Enabled() {
		t.Fatal("没有文件时不该算配了 prep")
	}
	write(t, root, ".sbx/prep.Dockerfile", "FROM scratch\n")
	if !s.Enabled() {
		t.Fatal("有文件时该算配了 prep")
	}
}

func TestParseProbe(t *testing.T) {
	out := strings.Join([]string{
		preSize + "412000\t/src/node_modules",
		preCache + "go-mod",
		preSize + "1200000\t/sbx/cache/go-mod",
		preMan + `{"allow":["gitlab.corp.test"],`,
		preMan + `"env":{"GOFLAGS":"-mod=mod"}}`,
		"",
	}, "\n")
	sizes, cache, man := parseProbe(out)
	if sizes["/src/node_modules"] != 412000*1024 {
		t.Errorf("size = %d", sizes["/src/node_modules"])
	}
	if len(cache) != 1 || cache[0] != "go-mod" {
		t.Errorf("cache = %v", cache)
	}
	// manifest 是多行的，必须拼回来而不是只留最后一行
	if !strings.Contains(man, "gitlab.corp.test") || !strings.Contains(man, "GOFLAGS") {
		t.Errorf("manifest = %q", man)
	}
}

func TestDescribe(t *testing.T) {
	p := Plan{
		Seeds: []Seedable{
			{Dest: "node_modules", Size: 412_000_000},
			{Dest: "cache/go-mod", Sub: "go-mod", Size: 1_200_000_000, Cache: true},
		},
		Manifest: &Manifest{Allow: []string{"gitlab.corp.test"}, Env: map[string]string{"GOFLAGS": "-mod=mod"}},
		Warnings: []string{".venv is in deps.mask but /src/.venv does not exist in the image"},
	}
	got := p.Describe()
	for _, want := range []string{"node_modules 412MB", "cache/go-mod 1.2GB", "allow +1 (gitlab.corp.test)", "env +1 (GOFLAGS)", "warning:"} {
		if !strings.Contains(got, want) {
			t.Errorf("报告里少了 %q：\n%s", want, got)
		}
	}
}
