package cli

import (
	"strings"
	"testing"

	"sandx/internal/config"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

func TestResolveAgent(t *testing.T) {
	if name, cli, err := resolveAgent(""); err != nil || name != "claude" || cli.bin != "claude" {
		t.Fatalf("空参数应当默认 claude：%q %+v %v", name, cli, err)
	}
	if _, _, err := resolveAgent("claude"); err != nil {
		t.Fatal(err)
	}
	// 设计里有、还没实现的 Agent：报错要说明原因，而不是"不认识"。
	err := mustErr(t, "codex")
	if !strings.Contains(err.Error(), "M3-6") {
		t.Fatalf("codex 的报错应当指向 M3-6：%v", err)
	}
	if err := mustErr(t, "cluade"); !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("打错名字应当是「不认识」：%v", err)
	}
}

func mustErr(t *testing.T, name string) error {
	t.Helper()
	_, _, err := resolveAgent(name)
	if err == nil {
		t.Fatalf("resolveAgent(%q) 应当报错", name)
	}
	return err
}

func TestAuthArgs(t *testing.T) {
	a := &App{Cfg: config.Default()}
	_, cli, err := resolveAgent("claude")
	if err != nil {
		t.Fatal(err)
	}

	got := strings.Join(a.authArgs("sbx/web-go:abc", cli, false, cli.status...), " ")
	want := "run --rm --mount type=volume,source=sbx-home,target=/home/agent sbx/web-go:abc claude auth status"
	if got != want {
		t.Fatalf("no upstream:\n got %s\nwant %s", got, want)
	}

	a.Cfg.Network.Upstream = "http://host.docker.internal:1087"
	got = strings.Join(a.authArgs("sbx/web-go:abc", cli, true, cli.login(true, "a@b.c")...), " ")
	for _, sub := range []string{"run --rm -it ", "-e HTTPS_PROXY=http://host.docker.internal:1087 ", "-e HTTP_PROXY=http://host.docker.internal:1087 ", "claude auth login --console --email a@b.c"} {
		if !strings.Contains(got, sub) {
			t.Fatalf("missing %q in:\n%s", sub, got)
		}
	}
}

func TestLoginHelpPointsAtSbxLogin(t *testing.T) {
	home := t.TempDir()
	tk, err := task.New(home, workspace.Workspace{Root: "/r", ID: "r-abc123"}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Home: home, Cfg: config.Default()}
	if s := a.loginHelp(tk); !strings.Contains(s, "sbx login") || !strings.Contains(s, "sbx run t1") {
		t.Fatalf("should point at sbx login:\n%s", s)
	}
}
