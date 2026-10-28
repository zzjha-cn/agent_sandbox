package artifact

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func write(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCopySelectionIsolationAndDigest(t *testing.T) {
	root := t.TempDir()
	write(t, root, "reports/a.txt", "one")
	write(t, root, "reports/sub/b.txt", "two")
	write(t, root, "reports/sub/c.csv", "skip")
	write(t, root, ".env", "secret")
	write(t, root, ".sbx/config", "internal")
	dest := filepath.Join(t.TempDir(), "copy")
	m, err := Copy(context.Background(), root, dest, []string{"reports/**/*.txt"}, nil, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 2 || m.Bytes != 6 || m.Digest == "" {
		t.Fatalf("bad manifest: %+v", m)
	}
	write(t, root, "reports/a.txt", "changed")
	check, err := Scan(context.Background(), dest, []string{"."}, nil, DefaultLimits())
	if err != nil || check.Digest != m.Digest {
		t.Fatalf("snapshot changed: %+v %v", check, err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".env")); !os.IsNotExist(err) {
		t.Fatalf("reserved input copied: %v", err)
	}
	if _, err := Copy(context.Background(), root, dest, []string{"."}, nil, DefaultLimits()); err == nil {
		t.Fatal("overwrote existing destination")
	}
}

func TestRejectUnsafeTreesAndLimits(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "file-limit", "total-limit", "count-limit", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "a", "1234")
			limits := DefaultLimits()
			ctx := context.Background()
			switch kind {
			case "symlink":
				if err := os.Symlink("/etc/passwd", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
					t.Fatal(err)
				}
			case "file-limit":
				limits.FileBytes = 3
			case "total-limit":
				limits.Bytes = 5
				limits.FileBytes = 5
				write(t, root, "b", "1234")
			case "count-limit":
				limits.Files = 1
				write(t, root, "b", "1")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			dest := filepath.Join(t.TempDir(), "snapshot")
			if _, err := Copy(ctx, root, dest, []string{"."}, nil, limits); err == nil {
				t.Fatal("unsafe copy accepted")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("partial snapshot published: %v", err)
			}
		})
	}
}

func TestSelections(t *testing.T) {
	for _, bad := range []string{"", "../x", "/tmp/x", "a/../b", "a//b", "a\\b", "["} {
		if ValidateSelection([]string{bad}) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{{"**/*.md", "a.md", true}, {"**/*.md", "x/y/a.md", true}, {"out", "outside/a", false}, {"out", "out/a", true}, {"a/*", "a/b/c", false}} {
		if got := Selected([]string{tc.pattern}, tc.name); got != tc.want {
			t.Errorf("%s %s = %t", tc.pattern, tc.name, got)
		}
	}
}

type changingContext struct {
	context.Context
	calls  int
	change func()
}

func (c *changingContext) Err() error {
	c.calls++
	if c.calls == 3 {
		c.change()
	}
	return c.Context.Err()
}

func TestInputMutationDuringReadRejectsUncommittedCopy(t *testing.T) {
	root := t.TempDir()
	write(t, root, "data", "initial")
	dest := filepath.Join(t.TempDir(), "snapshot")
	ctx := &changingContext{Context: context.Background(), change: func() { write(t, root, "data", "changed-and-larger") }}
	if _, err := Copy(ctx, root, dest, []string{"."}, nil, DefaultLimits()); err == nil {
		t.Fatal("input mutation was accepted")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("published an inconsistent copy")
	}
}
