package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sandx/internal/artifact"
	"sandx/internal/completion"
	"sandx/internal/docker"
	"sandx/internal/isolation"
)

func TestRealDockerVerifierIsolationAndOutcomes(t *testing.T) {
	if os.Getenv("SBX_REAL_DOCKER_E2E") != "1" {
		t.Skip("set SBX_REAL_DOCKER_E2E=1")
	}
	client := docker.New(false)
	cached, err := client.Run("image", "ls", "--filter", "reference=busybox:latest", "--format", "{{.ID}}")
	if err != nil || cached == "" {
		t.Fatalf("preload busybox: %v", err)
	}
	image, err := client.Run("image", "inspect", strings.Split(cached, "\n")[0], "--format", "{{.Id}}")
	if err != nil {
		t.Fatal(err)
	}
	runner := DockerTestRunner{Docker: client, Isolation: isolation.New(client, isolation.Policy{}), TempRoot: filepath.Join(t.TempDir(), "verification"), Limits: artifact.DefaultLimits()}
	snapshot := t.TempDir()
	if err := os.WriteFile(filepath.Join(snapshot, "report.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := artifact.Scan(context.Background(), snapshot, []string{"."}, nil, runner.Limits)
	if err != nil {
		t.Fatal(err)
	}
	checks := `test -f report.json && test ! -e /sbx/state && test ! -e /var/run/docker.sock && test ! -d /home/agent/.codex && test "$(wc -l </proc/net/route)" -eq 1 && grep -Eq 'CapEff:[[:space:]]+0000000000000000' /proc/self/status && ! touch /etc/sbx-verifier-test && touch test-write`
	for _, tc := range []struct {
		name, command string
		code          int
	}{{"restricted", checks, 0}, {"business-failure", "exit 7", 7}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			code, err := runner.Test(ctx, snapshot, completion.Rule{Kind: "tests", Version: "1", Image: image, Command: []string{"sh", "-c", tc.command}})
			if err != nil || code != tc.code {
				t.Fatalf("code=%d error=%v", code, err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := runner.Test(ctx, snapshot, completion.Rule{Kind: "tests", Version: "1", Image: image, Command: []string{"sh", "-c", "sleep 60"}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout did not remain a verifier fault: %v", err)
	}
	after, err := artifact.Scan(context.Background(), snapshot, []string{"."}, nil, runner.Limits)
	if err != nil || after.Digest != before.Digest {
		t.Fatalf("verifier modified immutable snapshot: %+v %v", after, err)
	}
	entries, err := os.ReadDir(runner.TempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("verifier copies leaked: %v %v", entries, err)
	}
}
