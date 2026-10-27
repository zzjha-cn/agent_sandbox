package execution

import (
	"context"
	"os"
	"path/filepath"
	"sandx/internal/docker"
	"testing"
)

func TestOwnedResourcesResponseLossAndForeignConflict(t *testing.T) {
	for _, kind := range []string{"container", "network", "volume"} {
		for _, mode := range []string{"response-lost", "existing", "foreign-race", "list-failed", "remove-failed"} {
			if kind == "container" && mode == "existing" {
				continue
			} // caller's Inspect handles this case
			t.Run(kind+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("OWNED_TEST_DIR", dir)
				t.Setenv("OWNED_TEST_KIND", kind)
				t.Setenv("OWNED_TEST_MODE", mode)
				shim := filepath.Join(dir, "docker")
				script := `#!/bin/sh
set -eu
case "$1 $2" in
 'network inspect'|'volume inspect')
  if [ "$OWNED_TEST_MODE" = existing ]; then echo '{}'; exit 0; fi
  echo 'No such volume or network' >&2; exit 1 ;;
 'network ls'|'volume ls'|'ps -a')
  if [ "$OWNED_TEST_MODE" = list-failed ]; then exit 72; fi
  if [ -f "$OWNED_TEST_DIR/token" ]; then
   if [ "$OWNED_TEST_KIND" = container ]; then echo '{"Names":"fixture"}'; else echo '{"Name":"fixture"}'; fi
  fi
  exit 0 ;;
 'rm -f'|'network rm'|'volume rm')
  touch "$OWNED_TEST_DIR/removed"
  if [ "$OWNED_TEST_MODE" = remove-failed ]; then exit 74; fi
  exit 0 ;;
esac
case "$1" in
 run|network|volume)
  if [ "$OWNED_TEST_MODE" != foreign-race ]; then
   for arg in "$@"; do case "$arg" in sbx.provision=*) echo "$arg" > "$OWNED_TEST_DIR/token" ;; esac; done
  fi
  exit 73 ;;
esac
exit 75
`
				if err := os.WriteFile(shim, []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
				d := &docker.Client{Bin: shim}
				var undo Undo
				var err error
				switch kind {
				case "container":
					err = CreateOwnedContainer(d, docker.RunSpec{Name: "fixture", Image: "fixture"}, &undo)
				case "network":
					err = CreateOwnedNetwork(d, "fixture", true, nil, &undo)
				case "volume":
					_, err = CreateOwnedVolume(d, "fixture", nil, &undo)
				}
				if mode != "existing" && err == nil {
					t.Fatal("missing primary failure")
				}
				cleanup := undo.Run(context.Background())
				if (len(cleanup) > 0) != (mode == "list-failed" || mode == "remove-failed") {
					t.Fatalf("cleanup: %v", cleanup)
				}
				_, statErr := os.Stat(filepath.Join(dir, "removed"))
				wantRemoved := mode == "response-lost" || mode == "remove-failed"
				if wantRemoved != (statErr == nil) {
					t.Fatalf("removed=%v, want %v", statErr == nil, wantRemoved)
				}
			})
		}
	}
}

func TestSnapshotRestoresBytesModesAndGeneratedTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen")
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "hooks", "original")
	if err := os.WriteFile(file, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var undo Undo
	if err := preservePath(dir, &undo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "new"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new", "file"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if failures := undo.Run(context.Background()); len(failures) > 0 {
		t.Fatal(failures)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "old\n" {
		t.Fatalf("restored %q: %v", data, err)
	}
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("mode %v: %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatalf("new path remains: %v", err)
	}
}

func TestSnapshotRejectsSymlinkWithoutFollowing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var undo Undo
	if err := preservePath(link, &undo); err == nil {
		t.Fatal("followed symlink")
	}
	if len(undo.actions) != 0 {
		t.Fatal("registered destructive rollback")
	}
}

func TestVolumeCreateRaceDoesNotSeedOrDeleteExistingVolume(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OWNED_RACE_DIR", dir)
	shim := filepath.Join(dir, "docker")
	script := `#!/bin/sh
case "$1 $2" in
 'volume inspect')
  if [ ! -f "$OWNED_RACE_DIR/created" ];then echo 'No such volume' >&2;exit 1;fi
  echo '{"sbx.provision":"another-attempt"}' ;;
 'volume create') touch "$OWNED_RACE_DIR/created" ;;
 'volume ls') exit 0 ;;
 'volume rm') touch "$OWNED_RACE_DIR/removed" ;;
 *) exit 2 ;;
esac
`
	if err := os.WriteFile(shim, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	var undo Undo
	fresh, err := CreateOwnedVolume(&docker.Client{Bin: shim}, "fixture", nil, &undo)
	if err != nil || fresh {
		t.Fatalf("raced volume should be retained, never seeded: %v %v", fresh, err)
	}
	if failures := undo.Run(context.Background()); len(failures) > 0 {
		t.Fatal(failures)
	}
	if _, err := os.Stat(filepath.Join(dir, "removed")); !os.IsNotExist(err) {
		t.Fatal("deleted existing volume")
	}
}
