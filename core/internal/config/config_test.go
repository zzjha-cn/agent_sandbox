package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultWhenMissing(t *testing.T) {
	cfg, w, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(w) != 0 {
		t.Fatal(err, w)
	}
	if cfg.Resources.Memory != "3g" || cfg.Resources.CPUs != 2 || cfg.Resources.Pids != 1024 || cfg.MaxRunning != 3 {
		t.Fatalf("%+v", cfg)
	}
	if cfg.Network.Mode != "open" || cfg.Network.Proxy != "shared" || cfg.Network.CloudMCP {
		t.Fatalf("%+v", cfg.Network)
	}
	if len(cfg.Deps.Mask) != 1 || cfg.Deps.Mask[0] != "node_modules" {
		t.Fatal(cfg.Deps)
	}
}

func TestOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte(`
max_running = 2
[network]
upstream = "http://host.docker.internal:7890"
[resources]
memory = "4g"
[agents.claude]
version = "2.1.0"
`), 0o644)
	cfg, w, err := Load(p)
	if err != nil || len(w) != 0 {
		t.Fatal(err, w)
	}
	if cfg.MaxRunning != 2 || cfg.Resources.Memory != "4g" || cfg.Resources.CPUs != 2 || cfg.ClaudeVersion() != "2.1.0" {
		t.Fatalf("%+v", cfg)
	}
	h, port, err := cfg.Upstream()
	if err != nil || h != "host.docker.internal" || port != "7890" {
		t.Fatal(h, port, err)
	}
}

func TestUnknownFieldWarns(t *testing.T) {
	cfg, w, err := parse("bogus = 1\n[network]\nfoo = 2\n", "x.toml", Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 2 || !strings.Contains(w[0], "bogus") || !strings.Contains(w[1], "network.foo") {
		t.Fatal(w)
	}
	if cfg.MaxRunning != 3 {
		t.Fatal(cfg)
	}
}

func TestInvalid(t *testing.T) {
	for _, in := range []string{
		"[resources]\nmemory = \"lots\"",
		"[network]\nmode = \"wide\"",
		"[network]\nupstream = \"socks5://h:1080\"",
		"[network]\nupstream = \"http://nohost\"",
	} {
		if _, _, err := parse(in, "x", Default()); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}
