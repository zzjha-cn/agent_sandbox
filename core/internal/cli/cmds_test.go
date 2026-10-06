package cli

import (
	"testing"
	"time"
)

func TestCompactStat(t *testing.T) {
	cases := map[string]string{
		"": "0",
		" 3 files changed, 10 insertions(+), 2 deletions(-)": "3f +10 -2",
		" 1 file changed, 1 insertion(+)":                    "1f +1 -0",
		" 1 file changed, 4 deletions(-)":                    "1f +0 -4",
	}
	for in, want := range cases {
		if got := compactStat(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestHumanAgo(t *testing.T) {
	if humanAgo(5*time.Second) != "5s ago" || humanAgo(3*time.Minute) != "3m ago" || humanAgo(5*time.Hour) != "5h ago" || humanAgo(72*time.Hour) != "3d ago" {
		t.Fatal()
	}
}

func TestTildePath(t *testing.T) {
	cases := [][3]string{
		{"/Users/a/.sbx/worktrees/x/t1", "/Users/a", "~/.sbx/worktrees/x/t1"},
		{"/Users/a", "/Users/a", "~"},
		{"/Users/ab/x", "/Users/a", "/Users/ab/x"},
		{"/tmp/x", "", "/tmp/x"},
	}
	for _, c := range cases {
		if got := tildePath(c[0], c[1]); got != c[2] {
			t.Errorf("tildePath(%q,%q)=%q want %q", c[0], c[1], got, c[2])
		}
	}
}
