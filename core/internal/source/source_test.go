package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"sandx/internal/artifact"
	"sandx/internal/workspace"
)

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	s, err := workspace.Git(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFilesAuthorityResumeAndEmpty(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	put(t, root, "allowed.txt", "input")
	put(t, root, "private.txt", "private")
	g := Grant{Kind: "files", Path: root, Include: []string{"allowed.txt"}}
	sel := Selection{Kind: "files", Ref: "registered", Include: []string{"."}}
	dest := filepath.Join(t.TempDir(), "work")
	p, err := Prepare(ctx, sel, g, dest, artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Baseline.Files) != 1 {
		t.Fatalf("expanded authorization: %+v", p)
	}
	put(t, dest, "allowed.txt", "agent change")
	if err := CheckResume(ctx, p, g, filepath.Join(t.TempDir(), "check"), artifact.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	put(t, root, "allowed.txt", "new input")
	if err := CheckResume(ctx, p, g, filepath.Join(t.TempDir(), "check"), artifact.DefaultLimits()); err == nil {
		t.Fatal("silently replaced source")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	e, err := Prepare(ctx, Selection{}, Grant{}, empty, artifact.DefaultLimits())
	if err != nil || len(e.Baseline.Files) != 0 {
		t.Fatalf("empty: %+v %v", e, err)
	}
	if _, err := os.Stat(filepath.Join(empty, ".git")); !os.IsNotExist(err) {
		t.Fatal("initialized Git")
	}
	if _, err := Prepare(ctx, Selection{Kind: "files", Ref: "x"}, Grant{Kind: "git", Path: root, Include: []string{"."}}, filepath.Join(t.TempDir(), "bad"), artifact.DefaultLimits()); err == nil {
		t.Fatal("accepted wrong grant kind")
	}
}

func TestGitRevisionAndExplicitWorkingInput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "config", "user.name", "test")
	git(t, root, "config", "user.email", "test@example.invalid")
	put(t, root, "tracked.txt", "committed")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "initial")
	sha := git(t, root, "rev-parse", "HEAD")
	put(t, root, "tracked.txt", "dirty")
	put(t, root, "untracked.txt", "new")
	g := Grant{Kind: "git", Path: root, Include: []string{"."}, AllowWorkingTree: true, AllowUntracked: true}
	for _, tc := range []struct {
		name               string
		working, untracked bool
		body               string
		count              int
	}{{"revision", false, false, "committed", 1}, {"tracked", true, false, "dirty", 1}, {"all", true, true, "dirty", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "work")
			p, err := Prepare(ctx, Selection{Kind: "git", Ref: "repo", Revision: sha, WorkingTree: tc.working, Untracked: tc.untracked}, g, dest, artifact.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(dest, "tracked.txt"))
			if err != nil || string(body) != tc.body || len(p.Baseline.Files) != tc.count || p.Revision != sha {
				t.Fatalf("unexpected source %+v %q %v", p, body, err)
			}
		})
	}
	denied := g
	denied.AllowWorkingTree = false
	if _, err := Prepare(ctx, Selection{Kind: "git", Ref: "repo", WorkingTree: true}, denied, filepath.Join(t.TempDir(), "no"), artifact.DefaultLimits()); err == nil {
		t.Fatal("working tree self-authorized")
	}
	if _, err := os.Stat(filepath.Join(root, "tracked.txt")); err != nil {
		t.Fatal("damaged source")
	}
}

func TestSingleFileAndAuthorityRebinding(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	put(t, root, "data.csv", "one")
	put(t, root, "other.txt", "not selected")
	g := Grant{Kind: "files", Path: filepath.Join(root, "data.csv"), Include: []string{"data.csv"}}
	sel := Selection{Kind: "files", Ref: "one"}
	dest := filepath.Join(t.TempDir(), "work")
	p, err := Prepare(ctx, sel, g, dest, artifact.DefaultLimits())
	if err != nil || len(p.Baseline.Files) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	other := t.TempDir()
	put(t, other, "data.csv", "one")
	g.Path = filepath.Join(other, "data.csv")
	if err := CheckResume(ctx, p, g, filepath.Join(t.TempDir(), "check"), artifact.DefaultLimits()); err == nil {
		t.Fatal("silently rebound the source to another path")
	}
}

func TestGitRawRevisionIgnoresArchiveTransformsAndWorkingCopyIgnoresHooks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "config", "user.name", "test")
	git(t, root, "config", "user.email", "test@example.invalid")
	put(t, root, "data.txt", "$Format:%H$")
	put(t, root, "included.txt", "export-ignore is not source authorization")
	put(t, root, ".gitattributes", "data.txt export-subst\nincluded.txt export-ignore\n")
	put(t, root, ".gitignore", "ignored.txt\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "input")
	put(t, root, "ignored.txt", "not selected")
	put(t, root, "visible.txt", "explicit untracked")
	marker := filepath.Join(t.TempDir(), "host-hook-ran")
	hook := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	git(t, root, "config", "core.fsmonitor", hook)
	g := Grant{Kind: "git", Path: root, Include: []string{"."}, AllowWorkingTree: true, AllowUntracked: true}
	for _, working := range []bool{false, true} {
		dest := filepath.Join(t.TempDir(), "work")
		_, err := Prepare(ctx, Selection{Kind: "git", Ref: "repo", WorkingTree: working, Untracked: working}, g, dest, artifact.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dest, "data.txt"))
		if err != nil || string(b) != "$Format:%H$" {
			t.Fatalf("Git archive altered source: %q %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(dest, "included.txt")); err != nil {
			t.Fatal("export-ignore silently removed authorized input")
		}
		if _, err := os.Stat(filepath.Join(dest, "ignored.txt")); !os.IsNotExist(err) {
			t.Fatal("included a Git-ignored untracked file")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("read-only source preparation executed a host hook")
	}
}
