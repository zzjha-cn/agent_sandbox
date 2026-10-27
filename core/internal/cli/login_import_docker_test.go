package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"sandx/internal/docker"
)

func TestRealDockerCodexCredentialImport(t *testing.T) {
	if os.Getenv("SBX_REAL_DOCKER_E2E") != "1" {
		t.Skip("set SBX_REAL_DOCKER_E2E=1")
	}
	image := os.Getenv("SBX_CODEX_TEST_IMAGE")
	if image == "" {
		image = "sbx/p0-provider-check:20261026"
	}
	client := docker.New(false)
	volume := fmt.Sprintf("sbx-test-codex-import-%d", time.Now().UnixNano())
	created, err := client.VolumeCreate(volume, map[string]string{"sbx.kind": "test-auth-import"})
	if err != nil || !created {
		t.Fatalf("create isolated volume: %v", err)
	}
	t.Cleanup(func() {
		if err := client.VolumeRm(volume); err != nil {
			t.Error(err)
		}
	})
	mount := "type=volume,source=" + volume + ",target=/home/agent"
	_, err = client.Run("run", "--rm", "--network", "none", "-u", "0", "--mount", mount, image, "sh", "-c", `chown "$(id -u agent):$(id -g agent)" /home/agent`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run("run", "--rm", "--network", "none", "--mount", mount, image, "sh", "-c", `mkdir -p /home/agent/.claude; printf keep > /home/agent/.claude/sentinel`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		force bool
		want  string
		fail  bool
	}{{false, "imported", false}, {false, "exists", true}, {true, "imported", false}} {
		out, err := client.RunInput(strings.NewReader(fixtureCodexAuth), codexImportArgs(image, volume, tc.force)...)
		if out != tc.want || (err != nil) != tc.fail {
			t.Fatalf("force=%t: result=%q err=%v", tc.force, out, err)
		}
	}
	// The real Codex CLI is used by the install script, with networking disabled.
	// No real account data is present, so this proves recognition, not model access.
	out, err := client.Run("run", "--rm", "--network", "none", "--mount", mount, image, "sh", "-c", `test "$(stat -c %a /home/agent/.codex/auth.json)" = 600 && test "$(cat /home/agent/.claude/sentinel)" = keep && test ! -e /home/agent/.sbx-codex-import.lock && printf verified`)
	if err != nil || out != "verified" {
		t.Fatalf("credential permissions/provider isolation: %q %v", out, err)
	}
}
