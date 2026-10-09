package cli

import (
	"bytes"
	"strings"
	"testing"
)

// warn 的语义是"能用，但该改"。doctor 会被写进脚本，warn 让它挂掉会逼人去 || true，
// 所以只有 fail 才非 0 退出；--strict 才把 warn 也算上。
func TestDoctorExitCode(t *testing.T) {
	cases := []struct {
		name    string
		results []Check
		strict  bool
		wantErr bool
	}{
		{"全绿", []Check{ok("a", "fine")}, false, false},
		{"只有警告", []Check{warn("a", "d", "f")}, false, false},
		{"只有警告 + strict", []Check{warn("a", "d", "f")}, true, true},
		{"有失败", []Check{ok("a", "x"), fail("b", "d", "f")}, false, true},
		{"跳过不算问题", []Check{skip("a", "没 docker")}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &App{Out: &bytes.Buffer{}}
			err := a.printChecks(c.results, c.strict)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v，期望出错 = %v", err, c.wantErr)
			}
		})
	}
}

func TestDoctorOutput(t *testing.T) {
	var out bytes.Buffer
	a := &App{Out: &out}
	a.printChecks([]Check{
		ok("docker", "Docker 27.4.0"),
		warn("trust", ".sbx/ 变过", "sbx trust"),
		fail("sbx-proxy", "停了", "docker rm -f sbx-proxy"),
		skip("smoke", "跳过（--quick）"),
	}, false)
	s := out.String()
	for _, want := range []string{"✓ docker", "! trust", "✗ sbx-proxy", "- smoke", "→ sbx trust", "1 项警告，1 项失败"} {
		if !strings.Contains(s, want) {
			t.Errorf("输出里缺 %q：\n%s", want, s)
		}
	}
	// ok 的项不该有修复建议
	if strings.Contains(s, "→ Docker") {
		t.Error("ok 的项不该给修复建议")
	}
}

// 环境不具备时要跳过，而不是刷一屏红叉。
func TestDoctorSkipsWhatItCannotCheck(t *testing.T) {
	a := &App{Out: &bytes.Buffer{}}
	got := map[string]level{}
	for _, c := range a.runChecks(docEnv{Docker: false, Repo: false}, true) {
		got[c.Name] = c.Level
	}
	for _, name := range []string{"vm-memory", "upstream", "sbx-proxy", "login", "smoke"} {
		if got[name] != lvSkip {
			t.Errorf("没有 docker 时 %s 应该跳过，得到 %v", name, got[name])
		}
	}
	if got["trust"] != lvSkip {
		t.Error("不在仓库里时 trust 应该跳过")
	}
	// docker 这一项本身不需要 docker 可用才能跑（它就是来报告这件事的）
	if got["docker"] == lvSkip {
		t.Error("docker 这项不该被跳过")
	}
}

func TestSingleQuotedVar(t *testing.T) {
	// design §9.2 的示例就踩了这个坑：单引号里的 $SBX_TASK 不会展开
	bad := `curl -X POST https://a.io/x -d '{"text":"$SBX_TASK idle"}'`
	if got := singleQuotedVar(bad); got != "$SBX_TASK" {
		t.Errorf("没认出来：%q", got)
	}
	good := `curl -X POST https://a.io/x --data-raw "{\"text\":\"$SBX_TASK idle\"}"`
	if got := singleQuotedVar(good); got != "" {
		t.Errorf("双引号里的不该报：%q", got)
	}
	if got := singleQuotedVar("echo hi", ""); got != "" {
		t.Errorf("%q", got)
	}
}
