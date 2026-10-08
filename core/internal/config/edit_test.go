package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTmp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestAddAllowNewFile(t *testing.T) {
	p := writeTmp(t, "")
	added, err := AddAllow(p, []string{"repo.mongodb.org"})
	if err != nil || len(added) != 1 {
		t.Fatalf("added=%v err=%v", added, err)
	}
	cfg, _, err := Load(p)
	if err != nil || len(cfg.Network.Allow) != 1 || cfg.Network.Allow[0] != "repo.mongodb.org" {
		t.Fatalf("%+v err=%v", cfg.Network, err)
	}
}

// 用户会手改这个文件，所以注释和其他字段必须原样保留。
func TestAddAllowKeepsComments(t *testing.T) {
	p := writeTmp(t, `# 我的配置
max_running = 3

[network]
# 宿主机代理
upstream = "http://host.docker.internal:7890"
allow = ["a.com"]

[resources]
memory = "4g"
`)
	added, err := AddAllow(p, []string{"b.com", "a.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "b.com" {
		t.Fatalf("已有的不该重复加：%v", added)
	}
	out, _ := os.ReadFile(p)
	s := string(out)
	for _, want := range []string{"# 我的配置", "# 宿主机代理", `upstream = "http://host.docker.internal:7890"`, `memory = "4g"`, `allow = ["a.com", "b.com"]`} {
		if !strings.Contains(s, want) {
			t.Fatalf("丢了 %q：\n%s", want, s)
		}
	}
}

func TestAddAllowSectionWithoutAllow(t *testing.T) {
	p := writeTmp(t, "[network]\nupstream = \"http://x:1\"\n")
	if _, err := AddAllow(p, []string{"c.com"}); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := Load(p)
	if len(cfg.Network.Allow) != 1 || cfg.Network.Upstream != "http://x:1" {
		t.Fatalf("%+v", cfg.Network)
	}
}

func TestAddAllowMultiline(t *testing.T) {
	p := writeTmp(t, `[network]
allow = [
  "a.com",
  "b.com",
]
[deps]
mask = ["node_modules"]
`)
	if _, err := AddAllow(p, []string{"c.com"}); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Network.Allow) != 3 || len(cfg.Deps.Mask) != 1 {
		t.Fatalf("%+v %+v", cfg.Network.Allow, cfg.Deps.Mask)
	}
}

func TestAddAllowNoop(t *testing.T) {
	p := writeTmp(t, "[network]\nallow = [\"a.com\"]\n")
	before, _ := os.ReadFile(p)
	added, err := AddAllow(p, []string{"a.com"})
	if err != nil || added != nil {
		t.Fatalf("added=%v err=%v", added, err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("没有新增时不该改文件")
	}
}

func TestDefaultModeIsOpen(t *testing.T) {
	if Default().Network.Mode != "open" {
		t.Fatal("默认应该不拦截出网（ADR 0005 修订）")
	}
}
