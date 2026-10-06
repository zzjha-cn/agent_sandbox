package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/config"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

func TestClaudeVersion(t *testing.T) {
	a := &App{Home: t.TempDir(), Cfg: config.Default()}
	if v := a.claudeVersion(); v != "latest" {
		t.Fatalf("no file: %q", v)
	}
	os.WriteFile(filepath.Join(a.Home, "claude-version"), []byte("2.1.300\n"), 0o644)
	if v := a.claudeVersion(); v != "2.1.300" {
		t.Fatalf("upgrade file: %q", v)
	}
	a.Cfg.Agents["claude"] = config.Agent{Version: "2.0.0"}
	if v := a.claudeVersion(); v != "2.0.0" {
		t.Fatalf("pinned config should win: %q", v)
	}
}

func TestLoginHelpUsesUpstream(t *testing.T) {
	home := t.TempDir()
	tk, err := task.New(home, workspace.Workspace{Root: "/r", ID: "r-abc123"}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Home: home, Cfg: config.Default()}
	if s := a.loginHelp(tk); strings.Contains(s, "HTTPS_PROXY") {
		t.Fatalf("no upstream should not set proxy:\n%s", s)
	}
	a.Cfg.Network.Upstream = "http://host.docker.internal:1087"
	if s := a.loginHelp(tk); !strings.Contains(s, "-e HTTPS_PROXY=http://host.docker.internal:1087 ") || strings.Contains(s, "7890") {
		t.Fatalf("should use configured upstream:\n%s", s)
	}
}
