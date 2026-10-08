package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/config"
)

func appWith(ag config.Agent) *App {
	c := config.Default()
	c.Agents = map[string]config.Agent{"claude": ag}
	return &App{Cfg: c}
}

func TestNoAPIKeyConfigured(t *testing.T) {
	k, err := appWith(config.Agent{Version: "latest"}).apiKey("")
	if err != nil || k.Env != "" {
		t.Fatalf("没配 API key 时应该返回零值：%+v err=%v", k, err)
	}
}

func TestAPIKeyFromEnv(t *testing.T) {
	t.Setenv("E2E_FAKE_KEY", "  sk-test-123\n")
	k, err := appWith(config.Agent{APIKeyEnv: "E2E_FAKE_KEY"}).apiKey("")
	if err != nil {
		t.Fatal(err)
	}
	if k.Env != "ANTHROPIC_API_KEY" || k.Value != "sk-test-123" {
		t.Fatalf("想要 ANTHROPIC_API_KEY=sk-test-123（去掉首尾空白），得到 %+v", k)
	}
	// Source 只说值从哪来，不能带上 key 本身
	if !strings.Contains(k.Source, "E2E_FAKE_KEY") || strings.Contains(k.Source, "sk-test") {
		t.Errorf("Source 不该泄露 key：%q", k.Source)
	}
}

// 配了但取不到值要报错，不能静默退回订阅登录——否则请求会记到订阅账号上。
func TestAPIKeyEnvUnsetIsError(t *testing.T) {
	os.Unsetenv("E2E_MISSING_KEY")
	if _, err := appWith(config.Agent{APIKeyEnv: "E2E_MISSING_KEY"}).apiKey(""); err == nil {
		t.Fatal("环境变量没设时应该报错")
	}
}

func TestAPIKeyFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key")
	os.WriteFile(p, []byte("sk-from-file\n"), 0o600)
	k, err := appWith(config.Agent{APIKeyFile: p}).apiKey("")
	if err != nil || k.Value != "sk-from-file" {
		t.Fatalf("想要 sk-from-file，得到 %+v err=%v", k, err)
	}
}

func TestAPIKeyFileProblems(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\n  \n"), 0o600)
	for name, path := range map[string]string{
		"文件不存在": filepath.Join(dir, "nope"),
		"空文件":   empty,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := appWith(config.Agent{APIKeyFile: path}).apiKey(""); err == nil {
				t.Fatal("应该报错")
			}
		})
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~/keys/anthropic"); got != filepath.Join(home, "keys/anthropic") {
		t.Errorf("~ 没展开：%q", got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("绝对路径不该动：%q", got)
	}
	// 中间的 ~ 不是 home，不要误伤
	if got := expandHome("/a/~b"); got != "/a/~b" {
		t.Errorf("误伤了非 home 的 ~：%q", got)
	}
}

func TestBothKeySourcesRejected(t *testing.T) {
	c := config.Default()
	c.Agents = map[string]config.Agent{"claude": {APIKeyEnv: "A", APIKeyFile: "/b"}}
	if err := c.Validate(); err == nil {
		t.Fatal("api_key_env 和 api_key_file 同时配应该报错")
	}
}
