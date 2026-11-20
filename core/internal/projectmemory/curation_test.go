package projectmemory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func draftFixture(t *testing.T) (Store, DraftRequest, FileDraftWriter) {
	s := queryFixture(t)
	p, _ := s.Load("demo")
	var ref EntryRef
	for _, e := range p.Latest() {
		if e.ID == "c" {
			ref = reference(e)
		}
	}
	r := DraftRequest{Kind: "skill", Name: "prep-diagnose", Description: "Diagnose prep dependency errors", Sources: []EntryRef{ref}, Facts: map[string]string{"node": "24"}, Plan: DraftPlan{Conditions: "Existing prep config", Trigger: "Dependency not found", Steps: "Read config and compare tool versions", Validation: "Run isolated fixture", Exit: "Report observed outcome", Limits: "No automatic reseed or publishing", Unknowns: "Private network is unverified"}}
	return s, r, FileDraftWriter{Repo: t.TempDir(), Output: "docs/drafts/prep-diagnose"}
}
func TestCurationFrozenWriteReviewRevocationAndRelations(t *testing.T) {
	s, request, writer := draftFixture(t)
	service := s.Curation()
	ctx := context.Background()
	v, err := service.CreateDraft(ctx, "demo", request)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != "prepared" || len(v.Reviews) != 0 {
		t.Fatal(v)
	}
	if _, err = service.ReviewDraft(ctx, "demo", v.ID, v.Digest, DraftReview{Actor: "operator", Decision: "approved", Reason: "review"}, writer); err == nil {
		t.Fatal("approved unwritten draft")
	}
	v, err = service.WriteDraft(ctx, "demo", v.ID, writer)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.WriteDraft(ctx, "demo", v.ID, writer)
	if err != nil || again.Digest != v.Digest {
		t.Fatal(again, err)
	}
	review := DraftReview{Actor: "fixture-reviewer", Decision: "approved", Reason: "Controlled test assertion, not actual human approval", Validation: []DraftValidation{{Scenario: "bounded prep fixture", Outcome: "passed", Evidence: "controlled test"}}}
	old := v
	v, err = service.ReviewDraft(ctx, "demo", v.ID, v.Digest, review, writer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ReviewDraft(ctx, "demo", old.ID, old.Digest, review, writer); err == nil {
		t.Fatal("stale review accepted")
	}
	if _, err = s.Review("demo", request.Sources[0].ID, request.Sources[0].Digest, "archived", "fixture", "withdraw"); err != nil {
		t.Fatal(err)
	}
	relations, err := service.SourceRelations(ctx, "demo", request.Sources[0].ID)
	if err != nil || len(relations) != 1 || len(relations[0].Findings) == 0 {
		t.Fatal(relations, err)
	}
	b, err := os.ReadFile(filepath.Join(writer.Repo, writer.Output, "SKILL.md"))
	if err != nil || !strings.Contains(string(b), request.Sources[0].Digest) {
		t.Fatal(err)
	}
	if _, err = service.ReviewDraft(ctx, "demo", v.ID, v.Digest, review, writer); err == nil {
		t.Fatal("revoked source approved downstream")
	}
	after, _ := os.ReadFile(filepath.Join(writer.Repo, writer.Output, "SKILL.md"))
	if string(after) != string(b) {
		t.Fatal("revocation silently rewrote artifact")
	}
}
func TestDraftWriterCannotOverwriteOrInstallAndRecoveryKeepsMaterial(t *testing.T) {
	s, r, w := draftFixture(t)
	v, err := s.Curation().CreateDraft(context.Background(), "demo", r)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{".agents/skills/example", "sbx_skills/example", "../outside", "AGENTS.md"} {
		bad := w
		bad.Output = output
		if err = bad.Write(context.Background(), v); err == nil {
			t.Fatal(output)
		}
	}
	if err = os.MkdirAll(filepath.Join(w.Repo, w.Output), 0700); err != nil {
		t.Fatal(err)
	}
	if err = w.Write(context.Background(), v); err == nil {
		t.Fatal("overwrote existing empty directory")
	}
	w.Output = "docs/drafts/owned"
	if err = w.Write(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	// Crash after file creation but before ledger completion: write reuses bytes.
	again, err := s.Curation().WriteDraft(context.Background(), "demo", v.ID, w)
	if err != nil || again.MaterialDigest != v.MaterialDigest {
		t.Fatal(again, err)
	}
	filename := filepath.Join(w.Repo, w.Output, "SKILL.md")
	if err = os.WriteFile(filename, []byte("operator modification"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.Write(context.Background(), v); err == nil {
		t.Fatal("overwrote operator modification")
	}
	inspection, err := s.Curation().Inspect(context.Background(), "demo", v.ID, w)
	if err != nil || len(inspection.Findings) == 0 {
		t.Fatal(inspection, err)
	}
	w.Output = "docs/drafts/link"
	if err = os.Symlink(t.TempDir(), filepath.Join(w.Repo, w.Output)); err != nil {
		t.Fatal(err)
	}
	if err = w.Write(context.Background(), v); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestCurationConcurrentReviewsOnlyOneCASWins(t *testing.T) {
	s, r, w := draftFixture(t)
	service := s.Curation()
	ctx := context.Background()
	v, err := service.CreateDraft(ctx, "demo", r)
	if err != nil {
		t.Fatal(err)
	}
	v, err = service.WriteDraft(ctx, "demo", v.ID, w)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.ReviewDraft(ctx, "demo", v.ID, v.Digest, DraftReview{Decision: "rejected", Actor: "fixture", Reason: "test"}, w)
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatal(wins)
	}
}
func TestCurationRejectsStalePendingAndForeignSources(t *testing.T) {
	s, r, _ := draftFixture(t)
	for _, ref := range []EntryRef{{ID: "b", Revision: 1, Digest: "fake"}, {ID: "c", Revision: 99, Digest: r.Sources[0].Digest}, {ID: "foreign", Revision: 1, Digest: "fake"}} {
		r.Sources = []EntryRef{ref}
		if _, err := s.Curation().CreateDraft(context.Background(), "demo", r); err == nil {
			t.Fatal(ref)
		}
	}
}

func TestDraftExtraArtifactsBlockApproval(t *testing.T) {
	s, r, w := draftFixture(t)
	ctx := context.Background()
	v, err := s.Curation().CreateDraft(ctx, "demo", r)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.Curation().WriteDraft(ctx, "demo", v.ID, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(w.Repo, w.Output, "unreviewed.sh"), []byte("echo unexpected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.Verify(ctx, v); err == nil {
		t.Fatal("extra artifact accepted")
	}
}

type forgedSourceGenerator struct{}

func (forgedSourceGenerator) Version() string { return "test-forgery/v1" }
func (forgedSourceGenerator) Generate(ctx context.Context, m DraftMaterial) ([]DraftFile, error) {
	m.Entries[0].Content = "forged source"
	return (ReferenceDraftGenerator{}).Generate(ctx, m)
}
func TestGeneratorCannotForgeFrozenSourceManifest(t *testing.T) {
	s, r, _ := draftFixture(t)
	service := NewCurationService(s.Queries(), StoreDraftLedger{s}, forgedSourceGenerator{}, nil)
	if _, err := service.CreateDraft(context.Background(), "demo", r); err == nil {
		t.Fatal("generator rewrote frozen source file")
	}
	records, err := (StoreDraftLedger{s}).List(context.Background(), "demo")
	if err != nil || len(records) != 0 {
		t.Fatal(records, err)
	}
}

func TestDraftInputRejectsAmbiguousAndNullFields(t *testing.T) {
	_, r, _ := draftFixture(t)
	raw, _ := json.Marshal(r)
	if _, err := DecodeDraftRequest(raw); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"kind":"rule","kind":"skill"}`, strings.TrimSuffix(string(raw), "}") + `,"facts":null}`, string(raw) + ` {}`} {
		if _, err := DecodeDraftRequest([]byte(input)); err == nil {
			t.Fatal(input)
		}
	}
}
