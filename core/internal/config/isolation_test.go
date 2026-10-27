package config

import (
	"strings"
	"testing"
)

func TestIsolationIsGlobalAdministratorPolicy(t *testing.T) {
	for _, section := range []string{"isolation", "Isolation", "ISOLATION"} {
		for _, layer := range []string{LayerProject, LayerWorkspace} {
			t.Run(section+"/"+layer, func(t *testing.T) {
				l := write(t, t.TempDir(), "layer.toml", "["+section+"]\nowner=\"attacker\"\n", layer == LayerProject)
				l.Name = layer
				if _, err := LoadLayers([]Layer{l}); err == nil || !strings.Contains(err.Error(), "global layer") {
					t.Fatal(err)
				}
			})
		}
	}
	l := write(t, t.TempDir(), "global.toml", "[isolation]\nboundary=\"dedicated-daemon\"\nowner=\"operator-a\"\n", false)
	l.Name = LayerGlobal
	ld, err := LoadLayers([]Layer{l})
	if err != nil || ld.Config.Isolation.Owner != "operator-a" || ld.Sources["isolation.owner"].Last() != LayerGlobal {
		t.Fatalf("%+v %v", ld, err)
	}
}

func TestIsolationRejectsTyposInsteadOfSilentlyUsingDefault(t *testing.T) {
	for _, body := range []string{"[Isolation]\nboundary=\"shared-daemon\"", "[isolation]\nBoundary=\"shared-daemon\"", "[isolation]\nmode=\"shared-daemon\"", "[isolation]\nowner=\"../bad\""} {
		l := write(t, t.TempDir(), "global.toml", body, false)
		l.Name = LayerGlobal
		if _, err := LoadLayers([]Layer{l}); err == nil {
			t.Fatal("unsafe/unknown isolation policy accepted", body)
		}
	}
}
