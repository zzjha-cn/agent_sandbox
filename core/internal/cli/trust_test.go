package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/trust"
	"sandx/internal/workspace"
)

// trustApp 搭一个只需要 Home + WS 的 App：信任检查不碰 docker，也不读配置。
func trustApp(t *testing.T) (*App, *bytes.Buffer) {
	t.Helper()
	repo := t.TempDir()
	var errb bytes.Buffer
	a := &App{
		Home: t.TempDir(),
		Out:  &errb,
		Err:  &errb,
		WS:   workspace.Workspace{Root: repo, ID: workspace.ID(repo)},
	}
	return a, &errb
}

func writeSbx(t *testing.T, a *App, rel, body string) {
	t.Helper()
	p := filepath.Join(a.WS.Root, trust.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 没有 .sbx/ 的仓库不该被信任检查打扰：绝大多数仓库是这样的。
func TestRunIsNotBlockedWithoutSbxDir(t *testing.T) {
	a, _ := trustApp(t)
	if err := a.requireTrust(); err != nil {
		t.Fatalf("没有 .sbx/ 却被拦住了：%v", err)
	}
}

func TestFirstTimeSbxDirBlocksRun(t *testing.T) {
	a, out := trustApp(t)
	writeSbx(t, a, "sandbox.toml", "[network]\nallow = [\"evil.test\"]\n")
	err := a.requireTrust()
	if err == nil {
		t.Fatal("第一次见到 .sbx/ 应该拦住 run")
	}
	if !strings.Contains(err.Error(), "sbx trust") {
		t.Errorf("错误信息要告诉用户怎么办，得到：%v", err)
	}
	// 拦住的同时要把内容打出来，否则用户不知道自己在信任什么
	if !strings.Contains(out.String(), "evil.test") {
		t.Errorf("没有展示 .sbx/ 的内容：\n%s", out)
	}
}

func TestTrustThenRunThenTamper(t *testing.T) {
	a, out := trustApp(t)
	writeSbx(t, a, "sandbox.toml", "[network]\nallow = [\"a.test\"]\n")

	cur, _, _, err := a.trustState()
	if err != nil {
		t.Fatal(err)
	}
	if err := trust.Save(trust.Path(a.Home, a.WS.ID), cur); err != nil {
		t.Fatal(err)
	}
	if err := a.requireTrust(); err != nil {
		t.Fatalf("信任之后 run 应该放行：%v", err)
	}

	// 队友 pull 下来一个改过的 sandbox.toml
	writeSbx(t, a, "sandbox.toml", "[network]\nallow = [\"a.test\", \"exfil.test\"]\n")
	out.Reset()
	if err := a.requireTrust(); err == nil {
		t.Fatal("改过 .sbx/ 之后应该再次拦住 run")
	}
	s := out.String()
	if !strings.Contains(s, "exfil.test") || !strings.Contains(s, string(trust.Modified)) {
		t.Errorf("应该打印出改动的内容：\n%s", s)
	}
}

// 多出来一个文件（比如将来的自定义 Dockerfile）同样要重新确认。
func TestNewFileInSbxDirBlocksRun(t *testing.T) {
	a, out := trustApp(t)
	writeSbx(t, a, "sandbox.toml", "a = 1\n")
	cur, _, _, _ := a.trustState()
	if err := trust.Save(trust.Path(a.Home, a.WS.ID), cur); err != nil {
		t.Fatal(err)
	}
	writeSbx(t, a, "Dockerfile", "FROM scratch\n")
	out.Reset()
	if err := a.requireTrust(); err == nil {
		t.Fatal("新增文件之后应该拦住 run")
	}
	if !strings.Contains(out.String(), "Dockerfile") {
		t.Errorf("没有指出新增的文件：\n%s", out)
	}
}

// sbx 自己写 .sbx/sandbox.toml（net allow --project）之后不该反过来拦住用户，
// 但前提是改之前本来就是已信任状态。
func TestRetrustOnlyWhenAlreadyTrusted(t *testing.T) {
	a, _ := trustApp(t)
	writeSbx(t, a, "sandbox.toml", "a = 1\n")
	cur, _, _, _ := a.trustState()
	if err := trust.Save(trust.Path(a.Home, a.WS.ID), cur); err != nil {
		t.Fatal(err)
	}

	before := a.trusted()
	writeSbx(t, a, "sandbox.toml", "a = 1\nb = 2\n") // 相当于 net allow --project 的写入
	a.retrust(before)
	if err := a.requireTrust(); err != nil {
		t.Fatalf("sbx 自己写完之后应该仍是已信任：%v", err)
	}

	// 别人留下的、用户还没看过的改动不能被顺带放行
	writeSbx(t, a, "Dockerfile", "FROM scratch\n") // 用户没看过
	before = a.trusted()
	if before {
		t.Fatal("有未确认的改动时 trusted() 应该是 false")
	}
	writeSbx(t, a, "sandbox.toml", "a = 1\nb = 2\nc = 3\n")
	a.retrust(before)
	if err := a.requireTrust(); err == nil {
		t.Fatal("未确认的 Dockerfile 被顺带放行了")
	}
}
