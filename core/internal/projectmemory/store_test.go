package projectmemory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fixture(t *testing.T) (Store, Entry) {
	t.Helper()
	s := Store{Home: t.TempDir()}
	if _, e := s.Create("demo"); e != nil {
		t.Fatal(e)
	}
	v, e := s.Add("demo", Input{Kind: "testing", Title: "Docker tests", Content: "Check Docker first. Literal $(touch /tmp/memory-should-not-execute).", Source: "operator verified local test"})
	if e != nil {
		t.Fatal(e)
	}
	return s, v
}
func approve(t *testing.T, s Store, v Entry) Entry {
	t.Helper()
	v, e := s.Review("demo", v.ID, v.Digest, "approved", "operator", "checked evidence")
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestReviewSelectionRevocationAndHistory(t *testing.T) {
	s, v := fixture(t)
	empty, e := s.Select("demo", Selection{})
	if e != nil || len(empty.Entries) != 0 {
		t.Fatal(empty, e)
	}
	if _, e = s.Review("demo", v.ID, "stale", "approved", "operator", "checked"); e == nil {
		t.Fatal("stale approval accepted")
	}
	v = approve(t, s, v)
	snap, e := s.Select("demo", Selection{})
	if e != nil || len(snap.Entries) != 1 {
		t.Fatal(snap, e)
	}
	if e = s.Check(snap); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Review("demo", v.ID, v.Digest, "archived", "operator", "outdated"); e != nil {
		t.Fatal(e)
	}
	if e = s.Check(snap); e == nil {
		t.Fatal("revoked snapshot remained usable")
	}
	p, e := s.Load("demo")
	if e != nil || len(p.History) != 3 || p.History[0].Status != "candidate" || p.History[1].Status != "approved" {
		t.Fatal(p, e)
	}
	if _, e = s.Select("../other", Selection{}); e == nil {
		t.Fatal("traversal")
	}
}
func TestProjectIsolationSelectorsAndLimits(t *testing.T) {
	s, v := fixture(t)
	approve(t, s, v)
	if _, e := s.Create("other"); e != nil {
		t.Fatal(e)
	}
	empty, e := s.Select("other", Selection{})
	if e != nil || len(empty.Entries) != 0 {
		t.Fatal(empty, e)
	}
	v, e = s.Add("demo", Input{Kind: "environment", Title: "core", Content: "Core only", Source: "operator", Paths: []string{"core/**"}, Tags: []string{"integration"}})
	if e != nil {
		t.Fatal(e)
	}
	approve(t, s, v)
	snap, e := s.Select("demo", Selection{})
	if e != nil || len(snap.Entries) != 1 {
		t.Fatal(snap, e)
	}
	snap, e = s.Select("demo", Selection{Paths: []string{"core/internal/a.go"}, Tags: []string{"integration"}})
	if e != nil || len(snap.Entries) != 1 || snap.Entries[0].ID != v.ID {
		t.Fatal(snap, e)
	}
	for i := 0; i < 25; i++ {
		n, e := s.Add("demo", Input{Kind: "testing", Title: fmt.Sprint(i), Content: strings.Repeat("x", 1500), Source: "operator"})
		if e != nil {
			t.Fatal(e)
		}
		approve(t, s, n)
	}
	a, _ := s.Select("demo", Selection{})
	b, _ := s.Select("demo", Selection{})
	if a.Digest != b.Digest || len(a.Entries) > MaxEntries || len(a.Context) > MaxBytes {
		t.Fatal("nondeterministic or unbounded snapshot")
	}
}
func TestReplacementAndConcurrentReview(t *testing.T) {
	s, old := fixture(t)
	old = approve(t, s, old)
	candidate, e := s.Add("demo", Input{Kind: "testing", Title: "replacement", Content: "New advice", Source: "operator", Supersedes: old.ID})
	if e != nil {
		t.Fatal(e)
	}
	snap, _ := s.Select("demo", Selection{})
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Review("demo", candidate.ID, candidate.Digest, "approved", "operator", "checked")
			out <- e
		}()
	}
	wg.Wait()
	close(out)
	success := 0
	for e := range out {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("approvals=%d", success)
	}
	if e = s.Check(snap); e == nil {
		t.Fatal("superseded entry still injected")
	}
	newSnap, _ := s.Select("demo", Selection{})
	if len(newSnap.Entries) != 1 || newSnap.Entries[0].ID != candidate.ID {
		t.Fatal(newSnap)
	}
}
func TestCorruptionBindingAndUntrustedImport(t *testing.T) {
	s, _ := fixture(t)
	repo := t.TempDir()
	if e := s.Bind("demo", repo); e != nil {
		t.Fatal(e)
	}
	if id, e := s.Resolve("", repo); e != nil || id != "demo" {
		t.Fatal(id, e)
	}
	s.Create("other")
	if e := s.Bind("other", repo); e == nil {
		t.Fatal("silent rebind")
	}
	if _, e := DecodeInput(strings.NewReader(`{"kind":"testing","title":"x","content":"y","source":"z","status":"approved"}`)); e == nil {
		t.Fatal("untrusted approved imported")
	}
	for _, p := range []string{"/secret", "../secret", "a/../../b", "[bad"} {
		v := Input{Kind: "testing", Title: "x", Content: "y", Source: "z", Paths: []string{p}}
		if e := v.Validate(); e == nil {
			t.Fatal(p)
		}
	}
	file, _ := s.projectPath("demo")
	b, _ := os.ReadFile(file)
	b = []byte(strings.Replace(string(b), "Docker tests", "tampered", 1))
	os.WriteFile(file, b, 0600)
	if _, e := s.Load("demo"); e == nil {
		t.Fatal("corruption silently ignored")
	}
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("{}"), 0600)
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(target, link)
	if _, e := ReadFile(link, 1024); e == nil {
		t.Fatal("followed symlink")
	}
}

func TestPinnedSnapshotsAreProjectBoundAndRevocable(t *testing.T) {
	s, v := fixture(t)
	v = approve(t, s, v)
	snapshot, e := s.Select("demo", Selection{})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Pin(snapshot); e != nil {
		t.Fatal(e)
	}
	if e = s.Pin(snapshot); e != nil {
		t.Fatal("idempotent pin", e)
	}
	if _, e = s.Pinned("demo", snapshot.Digest); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pinned("another", snapshot.Digest); e == nil {
		t.Fatal("cross project pin")
	}
	if _, e = s.Pinned("demo", "../../escape"); e == nil {
		t.Fatal("traversal")
	}
	if _, e = s.Review("demo", v.ID, v.Digest, "archived", "operator", "revoked"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pinned("demo", snapshot.Digest); e == nil {
		t.Fatal("revoked pinned snapshot used")
	}
}
