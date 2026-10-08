package agent

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sandx/internal/docker"
)

var update = flag.Bool("update", false, "update golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		os.WriteFile(p, got, 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("mismatch %s\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func fmtMounts(ms []docker.Mount) []byte {
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "%s -> %s volume=%v ro=%v\n", m.Source, m.Target, m.Volume, m.ReadOnly)
	}
	return []byte(b.String())
}

func TestMounts(t *testing.T) {
	host := HostClaude{Dir: "/nonexistent/.claude", HasMD: true, Dirs: []string{"skills", "commands"}}
	dep := func(i int) string { return fmt.Sprintf("sbx-demo-abc123-t1-dep-%d", i) }
	wt := Mounts(MountInput{
		Worktree: "/h/.sbx/worktrees/demo-abc123/t1", GitDir: "/r/demo/.git",
		DepMasks: []string{"node_modules", "web/node_modules"}, DepVolume: dep,
		StateDir: "/h/.sbx/state/demo-abc123/t1", GenDir: "/h/.sbx/state/demo-abc123/t1/gen", Host: host,
	})
	golden(t, "mounts-worktree.golden", fmtMounts(wt))
	main := Mounts(MountInput{
		Worktree: "/r/demo", DepMasks: []string{"node_modules"}, DepVolume: dep,
		StateDir: "/h/.sbx/state/demo-abc123/main", GenDir: "/h/.sbx/state/demo-abc123/main/gen",
	})
	golden(t, "mounts-main.golden", fmtMounts(main))
}

func TestSettingsJSON(t *testing.T) {
	var v struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct{ Type, Command string }
		}
		DisableConnectors bool `json:"disableClaudeAiConnectors"`
	}
	if err := json.Unmarshal(SettingsJSON(true), &v); err != nil {
		t.Fatal(err)
	}
	if !v.DisableConnectors {
		t.Fatal("blockCloudMCP 时必须写 disableClaudeAiConnectors")
	}
	var open struct {
		DisableConnectors bool `json:"disableClaudeAiConnectors"`
	}
	json.Unmarshal(SettingsJSON(false), &open)
	if open.DisableConnectors {
		t.Fatal("cloud_mcp=true 时不应该写 disableClaudeAiConnectors")
	}
	if len(v.Hooks) != 6 || v.Hooks["PreToolUse"][0].Matcher != "*" ||
		v.Hooks["Stop"][0].Hooks[0].Command != "/sbx/gen/hooks/status.sh idle" ||
		v.Hooks["UserPromptSubmit"][0].Hooks[0].Command != "/sbx/gen/hooks/status.sh running" ||
		v.Hooks["SessionEnd"][0].Hooks[0].Command != "/sbx/gen/hooks/status.sh exited" {
		t.Fatalf("%+v", v)
	}
}

// claude 退出后窗口必须留下一个 shell，否则 tmux 会话会跟着结束，Task 再也 attach 不回去。
func TestClaudeCmdKeepsSessionAlive(t *testing.T) {
	fresh := ClaudeCmd(false)
	if !strings.HasSuffix(fresh, "exec bash -l") || !strings.Contains(fresh, ExitNotice) {
		t.Fatalf("fresh: %s", fresh)
	}
	if strings.Contains(fresh, "--continue") {
		t.Fatalf("fresh 不应该带 --continue: %s", fresh)
	}
	if !strings.Contains(ClaudeCmd(true), "--continue") {
		t.Fatalf("continue: %s", ClaudeCmd(true))
	}
}

func TestPreseedIdempotent(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not found")
	}
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"userID":"u","projects":{"/other":{"x":1}}}`), 0o600)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"theme":"light"}`), 0o600)
	run := func() (string, string) {
		cmd := exec.Command("bash", "-c", Preseed())
		cmd.Env = append(os.Environ(), "HOME="+home, "SBX_WORKTREE=/w/t1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		a, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
		b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		return string(a), string(b)
	}
	a1, b1 := run()
	a2, b2 := run()
	if a1 != a2 || b1 != b2 {
		t.Fatal("not idempotent")
	}
	var cj map[string]any
	json.Unmarshal([]byte(a1), &cj)
	projects := cj["projects"].(map[string]any)
	if cj["hasCompletedOnboarding"] != true || cj["userID"] != "u" || projects["/other"] == nil ||
		projects["/w/t1"].(map[string]any)["hasTrustDialogAccepted"] != true {
		t.Fatal(a1)
	}
	var st map[string]any
	json.Unmarshal([]byte(b1), &st)
	if st["theme"] != "light" || st["skipDangerousModePermissionPrompt"] != true {
		t.Fatal(b1)
	}
	// 用户自建的普通文件不被覆盖
	os.Remove(filepath.Join(home, ".claude", "skills"))
	os.WriteFile(filepath.Join(home, ".claude", "skills"), []byte("mine"), 0o644)
	run()
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", "skills")); string(b) != "mine" {
		t.Fatal("user file overwritten")
	}
	// 新 home（文件都不存在）也能跑
	home = t.TempDir()
	run()
}

func TestInspectHostClaude(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	outside := filepath.Join(dir, "outside")
	os.MkdirAll(outside, 0o755)
	c := filepath.Join(dir, ".claude")
	os.MkdirAll(filepath.Join(c, "skills", "a"), 0o755)
	os.WriteFile(filepath.Join(c, "CLAUDE.md"), []byte("hi"), 0o644)
	os.Symlink(outside, filepath.Join(c, "skills", "ext"))
	os.Symlink(filepath.Join(c, "skills", "a"), filepath.Join(c, "skills", "inner"))
	h := InspectHostClaude(c)
	if !h.HasMD || len(h.Dirs) != 1 || h.Dirs[0] != "skills" {
		t.Fatalf("%+v", h)
	}
	if len(h.Warnings) != 1 || !strings.Contains(h.Warnings[0], "skills/ext") {
		t.Fatal(h.Warnings)
	}
	gen := filepath.Join(dir, "gen")
	if err := RenderGen(gen, h, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(gen, "host-claude", "CLAUDE.md")); string(b) != "hi" {
		t.Fatal("CLAUDE.md snapshot")
	}
	if st, _ := os.Stat(filepath.Join(gen, "hooks", "status.sh")); st.Mode().Perm()&0o111 == 0 {
		t.Fatal("status.sh not executable")
	}
	// 状态栏脚本必须可执行，否则 claude 每次刷新状态栏都会静默失败
	st, err := os.Stat(filepath.Join(gen, "statusline.sh"))
	if err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("statusline.sh 没渲染或不可执行：%v", err)
	}
}

func TestSettingsStatusLine(t *testing.T) {
	var got struct {
		StatusLine struct{ Type, Command string } `json:"statusLine"`
	}
	if err := json.Unmarshal(SettingsJSON(false), &got); err != nil {
		t.Fatal(err)
	}
	if got.StatusLine.Type != "command" || got.StatusLine.Command != "/sbx/gen/statusline.sh" {
		t.Fatalf("statusLine 没注入：%+v", got.StatusLine)
	}
}

func TestEnvTZ(t *testing.T) {
	env := Env(EnvInput{ProxyURL: "http://p", WS: "ws", Task: "t", TZ: "Asia/Shanghai"})
	if !slices.Contains(env, "TZ=Asia/Shanghai") {
		t.Fatalf("TZ 没注入：%v", env)
	}
	// 取不到宿主机时区时不要写一个空的 TZ，否则容器里反而变成 UTC 以外的未定义行为
	for _, e := range Env(EnvInput{ProxyURL: "http://p"}) {
		if strings.HasPrefix(e, "TZ=") {
			t.Fatalf("TZ 为空时不该出现：%v", e)
		}
	}
}

func TestEnvInjectsAPIKey(t *testing.T) {
	env := Env(EnvInput{ProxyURL: "http://p", APIKeyEnv: "ANTHROPIC_API_KEY", APIKey: "sk-x"})
	var found bool
	for _, e := range env {
		if e == "ANTHROPIC_API_KEY=sk-x" {
			found = true
		}
	}
	if !found {
		t.Fatalf("API key 没有注入：%v", env)
	}
	// 没配的时候不该凭空出现一个空变量
	for _, e := range Env(EnvInput{ProxyURL: "http://p"}) {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY") {
			t.Fatalf("没配 API key 却注入了 %q", e)
		}
	}
}

func TestAPIKeyApproval(t *testing.T) {
	// claude 记的是 key 的后 20 个字符，弹窗里显示的也是这一段（M2-13 实测）
	if got := APIKeyApproval("sk-ant-probe-not-a-real-key-0000000000"); got != "-real-key-0000000000" {
		t.Errorf("想要 -real-key-0000000000，得到 %q", got)
	}
	if got := APIKeyApproval("short"); got != "short" {
		t.Errorf("太短的 key 应该原样返回，得到 %q", got)
	}
	if got := APIKeyApproval(""); got != "" {
		t.Errorf("空 key 应该返回空，得到 %q", got)
	}
}

// 注入 API key 时要把确认框先按掉，否则无人值守会卡在 "Do you want to use this API key?"。
func TestPreseedApprovesAPIKey(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not found")
	}
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"customApiKeyResponses":{"approved":[],"rejected":["-real-key-0000000000"]}}`), 0o600)
	run := func(approve string) map[string]any {
		cmd := exec.Command("bash", "-c", Preseed())
		cmd.Env = append(os.Environ(), "HOME="+home, "SBX_WORKTREE=/w/t1", "SBX_API_KEY_APPROVE="+approve)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
		var cj map[string]any
		json.Unmarshal(b, &cj)
		return cj["customApiKeyResponses"].(map[string]any)
	}
	got := run("-real-key-0000000000")
	approved := got["approved"].([]any)
	if len(approved) != 1 || approved[0] != "-real-key-0000000000" {
		t.Fatalf("没有记成已批准：%v", got)
	}
	// 之前被拒过的同一个 key 要从 rejected 里拿掉，否则 claude 还是不用它
	if len(got["rejected"].([]any)) != 0 {
		t.Errorf("rejected 没清干净：%v", got)
	}
	// 跑第二遍不该重复追加
	if again := run("-real-key-0000000000"); len(again["approved"].([]any)) != 1 {
		t.Errorf("不幂等：%v", again)
	}
	// 没配 key 时不动这一段
	before := run("")
	if len(before["approved"].([]any)) != 1 {
		t.Errorf("没配 key 时不该改动已有记录：%v", before)
	}
}
