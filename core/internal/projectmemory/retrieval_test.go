package projectmemory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestKnowledgeLegacyDigestAndStructuredCandidate(t *testing.T) {
	// Golden pre-M4 Input field order: absent new fields must not alter digests.
	const legacy = `{"kind":"testing","title":"old","content":"works","source":"operator"}`
	v, err := DecodeInput(strings.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if string(b) != legacy {
		t.Fatalf("old input encoding changed: %s", b)
	}
	h := sha256.Sum256([]byte(legacy))
	if Digest(v) != "sha256:"+hex.EncodeToString(h[:]) {
		t.Fatal("legacy digest changed")
	}
	v.MemoryType = "episodic"
	v.Experience = &Experience{Actions: "changed cache path", Observations: "build passed", Unknowns: "root cause unproven"}
	v.Requires = map[string]string{"go.version": "1.27"}
	s, _ := fixture(t)
	entry, err := s.Add("demo", v)
	if err != nil || entry.Status != "candidate" {
		t.Fatal(entry, err)
	}
	loaded, err := s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, restored := range loaded.Latest() {
		if restored.ID == entry.ID {
			found = true
			if restored.MemoryType != "episodic" || restored.Experience.Unknowns != "root cause unproven" || restored.Requires["go.version"] != "1.27" {
				t.Fatal("structured experience did not round trip", restored)
			}
		}
	}
	if !found {
		t.Fatal("candidate not persisted")
	}
	v.MemoryType = "skill"
	if v.Validate() == nil {
		t.Fatal("skill accepted as memory type")
	}
}

func TestRankedSelectionScopeConditionsBudgetsAndExplanations(t *testing.T) {
	s, noise := fixture(t)
	approve(t, s, noise)
	add := func(v Input) Entry {
		t.Helper()
		e, err := s.Add("demo", v)
		if err != nil {
			t.Fatal(err)
		}
		return approve(t, s, e)
	}
	target := add(Input{Kind: "troubleshooting", Title: "E401 registry", Content: "npm authentication failure", Source: "verified failure", MemoryType: "procedural", Paths: []string{"web/**"}, Requires: map[string]string{"node": "24"}})
	partial := add(Input{Kind: "testing", Title: "E4010", Content: "distinct error", Source: "operator"})
	_ = partial
	q := Selection{Query: "E401", Paths: []string{"web/app.ts"}, Facts: map[string]string{"node": "24"}, Limit: 1}
	snap, err := s.Select("demo", q)
	if err != nil || len(snap.Entries) != 1 || snap.Entries[0].ID != target.ID {
		t.Fatal(snap, err)
	}
	if snap.DecisionCounts["selected"] != 1 || snap.Selector != SelectorVersion {
		t.Fatal(snap.DecisionCounts)
	}
	q.Facts = nil
	unknown, err := s.Select("demo", q)
	if err != nil || len(unknown.Entries) != 0 || unknown.DecisionCounts["conditions_unknown"] != 1 {
		t.Fatal(unknown, err)
	}
	q.Facts = map[string]string{"node": "20"}
	mismatch, err := s.Select("demo", q)
	if err != nil || len(mismatch.Entries) != 0 || mismatch.DecisionCounts["conditions_mismatch"] != 1 {
		t.Fatal(mismatch, err)
	}
	q.Facts = map[string]string{"node": "24"}
	q.Paths = nil
	outside, err := s.Select("demo", q)
	if err != nil || len(outside.Entries) != 0 {
		t.Fatal(outside, err)
	}
	for _, d := range outside.Decisions {
		if d.ID == target.ID {
			t.Fatal("out-of-scope metadata leaked")
		}
	}
	q.Paths = []string{"web/app.ts"}
	q.Bytes = 256
	limited, err := s.Select("demo", q)
	if err != nil || len(limited.Entries) != 0 || limited.DecisionCounts["byte_budget"] != 1 || len(limited.Context) > 256 {
		t.Fatal(limited, err)
	}
	if err = s.CheckFacts(snap, map[string]string{"node": "20"}); err == nil {
		t.Fatal("old snapshot conditions accepted")
	}
	if err = s.CheckFacts(snap, map[string]string{"node": "24"}); err != nil {
		t.Fatal(err)
	}
	grant := s
	grant.AllowedProjects = []string{"other"}
	if _, err = grant.Select("demo", q); err == nil {
		t.Fatal("project grant bypass")
	}
	for _, q := range []Selection{{Query: strings.Repeat("q", 2049)}, {Query: "!!!"}, {Limit: 21}, {Bytes: 255}, {Facts: map[string]string{"file:.env": "x"}}} {
		if _, err = s.Select("demo", q); err == nil {
			t.Fatal("unbounded or secret selector", q)
		}
	}
}

func TestStableNoQueryLegacySnapshotAndFeedbackDoNotReapprove(t *testing.T) {
	s, v := fixture(t)
	v = approve(t, s, v)
	before, err := s.Select("demo", Selection{})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an old snapshot with none of the new optional metadata.
	old := before
	old.Selector = ""
	old.Decisions = nil
	old.DecisionCounts = nil
	old.Digest = snapshotDigest(old)
	if err = s.Check(old); err != nil {
		t.Fatal("old snapshot rejected", err)
	}
	for i := 0; i < 4; i++ {
		again, err := s.Select("demo", Selection{})
		if err != nil || again.Digest != before.Digest {
			t.Fatal("selection not deterministic", err)
		}
	}
	actor := s
	actor.Actor = &ReviewIdentity{Subject: "trusted-operator", Organization: "org", PolicyDigest: "policy"}
	f, err := actor.RecordFeedback("demo", Feedback{EntryID: v.ID, EntryDigest: v.Digest, Kind: "counterexample", Actor: "forged", Note: "not applicable to new version", Task: "task-x"})
	if err != nil || f.Actor != "trusted-operator" || f.Identity == nil {
		t.Fatal(f, err)
	}
	project, err := s.Load("demo")
	if err != nil || len(project.Feedback) != 1 {
		t.Fatal(project, err)
	}
	current := project.Latest()[0]
	if current.Digest != v.Digest || current.ReviewDue != v.ReviewDue || current.Status != "approved" {
		t.Fatal("feedback mutated review")
	}
	if _, err = s.RecordFeedback("demo", Feedback{EntryID: v.ID, EntryDigest: "wrong", Kind: "validated", Actor: "operator", Note: "claim"}); err == nil {
		t.Fatal("unbound feedback")
	}
	if err = s.Check(before); err != nil {
		t.Fatal("feedback invalidated existing snapshot", err)
	}
	project.Feedback[0].Note = "tampered"
	path, _ := s.projectPath("demo")
	if err = write(path, project); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load("demo"); err == nil {
		t.Fatal("feedback corruption hidden")
	}
}

func TestFeedbackConcurrentAndExpiryNotExtended(t *testing.T) {
	s, v := fixture(t)
	due := time.Now().Add(time.Minute)
	v, err := s.ReviewAt("demo", v.ID, v.Digest, "approved", "operator", "verified", 0, &due)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.RecordFeedback("demo", Feedback{EntryID: v.ID, EntryDigest: v.Digest, Kind: "helpful", Actor: "operator", Note: "explicit claim"}); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	p, err := s.Load("demo")
	if err != nil || len(p.Feedback) != 8 || !p.Latest()[0].ReviewDue.Equal(due) {
		t.Fatal(p, err)
	}
}

func TestWatchConfinedMissingChangedAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "go.mod")
	if err := os.WriteFile(file, []byte("module demo"), 0600); err != nil {
		t.Fatal(err)
	}
	facts, err := ObserveFiles(root, []string{"go.mod", "missing"}, map[string]string{"go.version": "1.27"})
	if err != nil || facts["file:go.mod"] == "" || facts["file:missing"] != "" {
		t.Fatal(facts, err)
	}
	v := Entry{Input: Input{Requires: map[string]string{"file:go.mod": facts["file:go.mod"]}}}
	if Applicability(v, facts) != "satisfied" {
		t.Fatal(facts)
	}
	os.WriteFile(file, []byte("module changed"), 0600)
	changed, err := ObserveFiles(root, []string{"go.mod"}, nil)
	if err != nil || Applicability(v, changed) != "mismatch" {
		t.Fatal(changed, err)
	}
	for _, name := range []string{"../go.mod", "/etc/passwd", ".env", ".aws/credentials", ".git/config"} {
		if _, e := ObserveFiles(root, []string{name}, nil); e == nil {
			t.Fatal("reserved watch", name)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Symlink(outside, filepath.Join(root, "link"))
	if _, err = ObserveFiles(root, []string{"link"}, nil); err == nil {
		t.Fatal("symlink read")
	}
	syscall.Mkfifo(filepath.Join(root, "fifo"), 0600)
	if _, err = ObserveFiles(root, []string{"fifo"}, nil); err == nil {
		t.Fatal("fifo accepted")
	}
	if _, err = ObserveFiles(root, nil, map[string]string{"file:go.mod": "asserted"}); err == nil {
		t.Fatal("asserted file fingerprint")
	}
}

func TestRegisteredEvidenceMissingOrChangedExcludesWithoutErasingHistory(t *testing.T) {
	s, _ := fixture(t)
	material := map[string]any{"safe_summary": "synthetic failed task"}
	output := map[string]any{"candidate": "workaround, root cause unknown"}
	refs := []Provenance{{Job: "job1", Task: "task1", Attempt: "attempt1", Extractor: "fixture", MaterialDigest: Digest(material), OutputDigest: Digest(output), Evidence: []string{"handoff:fixture"}}}
	ids, err := s.PublishExtraction("demo", []Input{{Kind: "troubleshooting", Title: "EVIDENCE_CODE", Content: "Verify current project first", Source: "extraction:job1"}}, refs)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Load("demo")
	var candidate Entry
	for _, e := range p.Latest() {
		if e.ID == ids[0] {
			candidate = e
		}
	}
	candidate = approve(t, s, candidate)
	q := Selection{Query: "EVIDENCE_CODE"}
	missing, err := s.Select("demo", q)
	if err != nil || len(missing.Entries) != 0 || missing.DecisionCounts["evidence_unavailable"] != 1 {
		t.Fatal(missing, err)
	}
	job := map[string]any{"id": "job1", "project_id": "demo", "material_digest": Digest(material), "output_digest": Digest(output), "material": material, "output": output}
	file := filepath.Join(s.Home, "memory-extractions", "job1", "job.json")
	os.MkdirAll(filepath.Dir(file), 0700)
	b, _ := json.MarshalIndent(job, "", "  ")
	if err = os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	valid, err := s.Select("demo", q)
	if err != nil || len(valid.Entries) != 1 {
		t.Fatal(valid, err)
	}
	if err = s.Check(valid); err != nil {
		t.Fatal(err)
	}
	job["output"] = map[string]any{"changed": "unbound"}
	b, _ = json.Marshal(job)
	os.WriteFile(file, b, 0600)
	if err = s.Check(valid); err == nil {
		t.Fatal("changed evidence resumed")
	}
	p, err = s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Latest() {
		if e.ID == candidate.ID && (e.Status != "approved" || e.Digest != candidate.Digest) {
			t.Fatal("evidence disappearance rewrote review")
		}
	}
}
