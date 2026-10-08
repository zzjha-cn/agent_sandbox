package cli

import (
	"os"
	"path/filepath"
	"testing"

	"sandx/internal/config"
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
