package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/docker"
)

func TestDedicatedConnectsEgressBeforeStarting(t *testing.T) {
	for _, failure := range []string{"", "create", "connect", "start"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "commands")
			shim := filepath.Join(dir, "docker")
			body := `#!/bin/sh
printf '%s\n' "$*" >> "$COMMAND_LOG"
case "$1 $2" in
  'container inspect')
    if [ -f "$COMMAND_LOG.started" ]; then echo '{"Running":true}'; else echo 'No such container' >&2; exit 1; fi ;;
  'create '*) [ "$FAIL_AT" != create ] || exit 9 ;;
  'network connect') [ "$FAIL_AT" != connect ] || exit 9 ;;
  'start '*) [ "$FAIL_AT" != start ] || exit 9; touch "$COMMAND_LOG.started" ;;
  'exec '*) exit 0 ;;
  *) echo unexpected >&2; exit 8 ;;
esac
`
			if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("COMMAND_LOG", log)
			t.Setenv("FAIL_AT", failure)
			d := Dedicated{Docker: &docker.Client{Bin: shim}, Dir: filepath.Join(dir, "config"), Name: "test-proxy", Egress: "test-egress"}
			_, err := d.AttachTask(TaskSpec{TaskID: "test.t1", Network: "test-internal", Open: true})
			if (err != nil) != (failure != "") {
				t.Fatalf("failure=%q err=%v", failure, err)
			}
			b, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			commands := string(b)
			create, connect, start := strings.Index(commands, "create --name"), strings.Index(commands, "network connect"), strings.Index(commands, "start test-proxy")
			if create < 0 {
				t.Fatalf("no create: %s", commands)
			}
			if failure == "create" {
				if connect >= 0 || start >= 0 {
					t.Fatal(commands)
				}
				return
			}
			if connect < create {
				t.Fatal(commands)
			}
			if failure == "connect" {
				if start >= 0 {
					t.Fatal(commands)
				}
				return
			}
			if start < connect {
				t.Fatal(commands)
			}
		})
	}
}
