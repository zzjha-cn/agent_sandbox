package projectmemory

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSharingCandidateOnlyReplayAndPrivateMetadata(t *testing.T) {
	s, v := fixture(t)
	v = approve(t, s, v)
	p, _ := s.Load("demo")
	// Metadata that must never leave the host by default.
	v.Source = "/Users/private/repo task:secret"
	v.Reviewer = "private-reviewer"
	v.ReviewIdentity = &ReviewIdentity{Subject: "private-reviewer", Organization: "secret-team", PolicyDigest: "secret-policy"}
	v.Provenance = &Provenance{Task: "private-task", Job: "private-job"}
	v.Digest = entryDigest(v)
	p.History[len(p.History)-1] = v
	f, _ := s.projectPath("demo")
	if e := write(f, p); e != nil {
		t.Fatal(e)
	}
	b, e := s.Export("demo", []string{v.ID})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(b)
	for _, secret := range []string{"/Users/private", "private-reviewer", "private-task", "private-job", "secret-team", "secret-policy", "supersedes", "approved"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("leaked metadata", secret, string(raw))
		}
	}
	if _, e = s.Export("demo", nil); e == nil {
		t.Fatal("implicit whole project export")
	}
	if _, e = s.Create("local"); e != nil {
		t.Fatal(e)
	}
	decoded, e := DecodeBundle(raw)
	if e != nil {
		t.Fatal(e)
	}
	entries, e := s.ImportBundle("local", decoded)
	if e != nil || len(entries) != 1 {
		t.Fatal(entries, e)
	}
	n := entries[0]
	if n.Status != "candidate" || n.Reviewer != "" || n.ReviewIdentity != nil || n.Import == nil {
		t.Fatal(n)
	}
	snap, e := s.Select("local", Selection{})
	if e != nil || len(snap.Entries) != 0 {
		t.Fatal(snap, e)
	}
	n, e = s.Review("local", n.ID, n.Digest, "approved", "receiver", "verified locally")
	if e != nil {
		t.Fatal(e)
	}
	before, _ := s.Load("local")
	entries, e = s.ImportBundle("local", decoded)
	after, _ := s.Load("local")
	if e != nil || entries[0].Digest != n.Digest || entries[0].Status != "approved" || before.Sequence != after.Sequence {
		t.Fatal("replay changed review", entries, e)
	}
	// Mapping must point at an existing project; foreign project isn't created.
	if _, e = s.ImportBundle("absent", decoded); e == nil {
		t.Fatal("implicit project mapping")
	}
}

func TestSharingRejectsUntrustedAuthorityAndAtomicBatch(t *testing.T) {
	s, v := fixture(t)
	v = approve(t, s, v)
	b, _ := s.Export("demo", []string{v.ID})
	raw, _ := json.Marshal(b)
	bads := [][]byte{
		[]byte(strings.Replace(string(raw), `"input":{`, `"input":{"status":"approved",`, 1)),
		[]byte(strings.Replace(string(raw), `"input":{`, `"input":{"review_identity":{"subject":"admin"},`, 1)),
		[]byte(strings.Replace(string(raw), `"input":{`, `"input":{"title":"duplicate",`, 1)),
		append(append([]byte{}, raw...), []byte(` {}`)...),
		[]byte(`null`),
		[]byte(strings.Replace(string(raw), `"input":{`, `"input":{"paths":null,`, 1)),
		[]byte(strings.Replace(string(raw), "Check Docker", "tampered", 1)),
		[]byte(strings.Repeat(" ", MaxBundle+1)),
	}
	for i, raw := range bads {
		if _, e := DecodeBundle(raw); e == nil {
			t.Fatal("accepted untrusted bundle", i)
		}
	}
	before, _ := s.Load("demo")
	b.Entries = append(b.Entries, SharedEntry{Input: Input{Kind: "testing", Title: "bad", Content: "bad", Source: "foreign", Supersedes: v.ID}})
	b.Digest = bundleDigest(b)
	if _, e := s.ImportBundle("demo", b); e == nil {
		t.Fatal("invalid batch accepted")
	}
	after, _ := s.Load("demo")
	if before.Sequence != after.Sequence || len(before.History) != len(after.History) {
		t.Fatal("partial import")
	}
}

func TestExpiryRenewalPreservesHistoryAndStopsPinnedReuse(t *testing.T) {
	s, v := fixture(t)
	due := time.Now().Add(time.Hour)
	v, e := s.ReviewAt("demo", v.ID, v.Digest, "approved", "operator", "tested", 0, &due)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := s.Select("demo", Selection{})
	if e != nil || len(snap.Entries) != 1 {
		t.Fatal(snap, e)
	}
	if e = s.Pin(snap); e != nil {
		t.Fatal(e)
	}
	// Set a past time on disk to simulate passage without sleeps.
	p, _ := s.Load("demo")
	past := time.Now().Add(-time.Hour)
	v.ReviewDue = &past
	v.Digest = entryDigest(v)
	p.History[len(p.History)-1] = v
	f, _ := s.projectPath("demo")
	if e = write(f, p); e != nil {
		t.Fatal(e)
	}
	expiredSnapshot, _ := s.Select("demo", Selection{})
	if len(expiredSnapshot.Entries) != 0 {
		t.Fatal("expired memory injected")
	}
	old := snap
	old.Entries = []Entry{v}
	old.Digest = snapshotDigest(old)
	if e = s.Check(old); e == nil {
		t.Fatal("expired pinned entry reused")
	}
	if _, e = s.Export("demo", []string{v.ID}); e == nil {
		t.Fatal("expired export")
	}
	report, e := s.Inspect("demo")
	if e != nil || len(report.Findings) != 1 || report.Findings[0].Kind != "review_due" {
		t.Fatal(report, e)
	}
	renewed, e := s.ReviewAt("demo", v.ID, v.Digest, "renewed", "operator", "retested", p.Sequence, &due)
	if e != nil {
		t.Fatal(e)
	}
	current, _ := s.Select("demo", Selection{})
	history, _ := s.Load("demo")
	if len(current.Entries) != 1 || renewed.Revision != 3 || len(history.History) != 3 {
		t.Fatal(current, history)
	}
	if _, e = LoadSnapshot(mustSnapshotPath(t, s, snap.Digest)); e != nil {
		t.Fatal("historical snapshot lost", e)
	}
	if e = s.Check(snap); e == nil {
		t.Fatal("old digest silently replaced by renewed memory")
	}
}
func mustSnapshotPath(t *testing.T, s Store, digest string) string {
	t.Helper()
	p, e := s.snapshotPath(digest)
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestMaintenanceHintsHistoryIntegrityAndConcurrentSequence(t *testing.T) {
	s, v := fixture(t)
	s.Add("demo", v.Input)
	conflict := v.Input
	conflict.Content = "Different advice"
	s.Add("demo", conflict)
	report, e := s.Inspect("demo")
	if e != nil || report.StoreBytes == 0 || report.HistoryRevisions != 3 || len(report.Findings) != 2 || report.Findings[0].Kind != "duplicate" || report.Findings[1].Kind != "possible_conflict" {
		t.Fatal(report, e)
	}
	p, _ := s.Load("demo")
	// Different entries with one project sequence cannot both be reviewed.
	entries := p.Latest()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, entry := range entries[:2] {
		wg.Add(1)
		go func(v Entry) {
			defer wg.Done()
			_, e := s.ReviewAt("demo", v.ID, v.Digest, "approved", "operator", "checked", p.Sequence, nil)
			results <- e
		}(entry)
	}
	wg.Wait()
	close(results)
	ok := 0
	for e := range results {
		if e == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatal("sequence conflict lost", ok)
	}
	f, _ := s.projectPath("demo")
	raw, _ := os.ReadFile(f)
	raw = []byte(strings.Replace(string(raw), "Different advice", "corrupted", 1))
	os.WriteFile(f, raw, 0600)
	if _, e = s.Inspect("demo"); e == nil {
		t.Fatal("corrupt history silently rebuilt")
	}
}

func TestOrganizationStoreProjectScopeAndAuthenticatedReviewer(t *testing.T) {
	s, v := fixture(t)
	s.Create("other")
	scoped := s
	scoped.AllowedProjects = []string{"demo"}
	scoped.Actor = &ReviewIdentity{Subject: "uid:501 alice", Organization: "team", PolicyDigest: "admin-policy"}
	for _, id := range []string{"other", "new"} {
		if _, e := scoped.Load(id); e == nil {
			t.Fatal("read scope bypass")
		}
		if _, e := scoped.Create(id); e == nil {
			t.Fatal("create scope bypass")
		}
	}
	if _, e := scoped.Review("demo", v.ID, v.Digest, "approved", "forged-admin", "checked"); e == nil {
		t.Fatal("missing project sequence")
	}
	p, _ := scoped.Load("demo")
	n, e := scoped.ReviewAt("demo", v.ID, v.Digest, "approved", "forged-admin", "checked", p.Sequence, nil)
	if e != nil || n.Reviewer != scoped.Actor.Subject || n.ReviewIdentity == nil || n.ReviewIdentity.PolicyDigest != "admin-policy" {
		t.Fatal(n, e)
	}
}

func TestConcurrentImportAndRenewSupersedingEntry(t *testing.T) {
	s, old := fixture(t)
	old = approve(t, s, old)
	in := old.Input
	in.Content = "new advice"
	in.Supersedes = old.ID
	replacement, e := s.Add("demo", in)
	if e != nil {
		t.Fatal(e)
	}
	replacement = approve(t, s, replacement)
	due := time.Now().Add(time.Hour)
	replacement, e = s.ReviewAt("demo", replacement.ID, replacement.Digest, "renewed", "operator", "rechecked", 0, &due)
	if e != nil {
		t.Fatal("renew attempted to supersede the old entry again", e)
	}
	b, e := s.Export("demo", []string{replacement.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Create("receiver"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.ImportBundle("receiver", b); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	p, e := s.Load("receiver")
	if e != nil || len(p.History) != 1 || len(p.Latest()) != 1 || p.Sequence != 2 {
		t.Fatal("concurrent duplicate import", p, e)
	}
}

func TestSharingRevalidatesAfterSourceReplacement(t *testing.T) {
	s, v := fixture(t)
	// A valid source input can become oversized when its source label is replaced.
	in := Input{Kind: "testing", Title: "boundary", Content: "x", Source: "x"}
	for {
		next := in
		next.Content += "x"
		if next.Validate() != nil {
			break
		}
		in = next
	}
	v, e := s.Add("demo", in)
	if e != nil {
		t.Fatal(e)
	}
	v = approve(t, s, v)
	if _, e = s.Export("demo", []string{v.ID}); e == nil {
		t.Fatal("oversized portable input exported")
	}
	b := Bundle{Schema: BundleSchema, Project: "foreign", Entries: []SharedEntry{{Input: in, Digest: Digest(in)}}}
	b.Digest = bundleDigest(b)
	if e = b.Validate(); e != nil {
		t.Fatal(e)
	}
	p, _ := s.Load("demo")
	if _, e = s.ImportBundle("demo", b); e == nil {
		t.Fatal("oversized local input persisted")
	}
	after, e := s.Load("demo")
	if e != nil || after.Sequence != p.Sequence {
		t.Fatal("partial or unreadable store", e)
	}
}

func TestMaintenanceNormalizedDuplicateHints(t *testing.T) {
	s, _ := fixture(t)
	a := Input{Kind: "workflow", Title: "First title", Content: "Run  OFFLINE tests", Source: "operator", Paths: []string{"a/**", "b/**"}, Tags: []string{"go", "test"}}
	b := a
	b.Title = "Other title"
	b.Content = "run offline\n tests"
	b.Paths = []string{"b/**", "a/**"}
	b.Tags = []string{"test", "go"}
	if _, e := s.Add("demo", a); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Add("demo", b); e != nil {
		t.Fatal(e)
	}
	report, e := s.Inspect("demo")
	if e != nil || len(report.Findings) != 1 || report.Findings[0].Kind != "duplicate" || len(report.Findings[0].Entries) != 2 {
		t.Fatal(report, e)
	}
}
