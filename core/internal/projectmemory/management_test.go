package projectmemory

import (
	"context"
	"testing"
)

func TestReviewFindingsFeedbackResolutionNeverApproves(t *testing.T) {
	s := queryFixture(t)
	ctx := context.Background()
	p, _ := s.Load("demo")
	var target Entry
	for _, e := range p.Latest() {
		if e.ID == "c" {
			target = e
		}
	}
	feedback, err := s.Management().RecordFeedback(ctx, "demo", Feedback{EntryID: target.ID, EntryDigest: target.Digest, Kind: "counterexample", Actor: "fixture", Note: "Observed contrary result"}, p.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Management().RecordFeedback(ctx, "demo", feedback, p.Sequence); err == nil {
		t.Fatal("stale feedback sequence accepted")
	}
	q, err := s.Management().ListReviewFindings(ctx, "demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, f := range q.Findings {
		if f.Kind == "counterexample" {
			id = f.ID
		}
	}
	if id == "" {
		t.Fatal(q)
	}
	_, err = s.Management().ReviewFinding(ctx, "demo", FindingReview{FindingID: id, Actor: "fixture", Reason: "Need follow-up", Decision: "needs_update"}, q.Sequence, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.Load("demo")
	for _, e := range after.Latest() {
		if e.ID == target.ID && e.Digest != target.Digest {
			t.Fatal("finding review changed approval")
		}
	}
	q, err = s.Management().ListReviewFindings(ctx, "demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range q.Findings {
		if f.ID == id {
			found = f.Review != nil && f.Review.Decision == "needs_update"
		}
	}
	if !found {
		t.Fatal(q)
	}
}
func TestManagementAtomicReplacementAndStaleSequence(t *testing.T) {
	s := queryFixture(t)
	ctx := context.Background()
	p, _ := s.Load("demo")
	replacement, err := s.Management().SubmitCandidate(ctx, "demo", Input{Kind: "testing", Title: "Replacement", Content: "new", Source: "fixture", Supersedes: "c"}, p.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Management().Review(ctx, "demo", replacement.ID, replacement.Digest, "approved", "fixture", "review", p.Sequence, nil); err == nil {
		t.Fatal("stale sequence approved")
	}
	p, _ = s.Load("demo")
	_, err = s.Management().Review(ctx, "demo", replacement.ID, replacement.Digest, "approved", "fixture", "review", p.Sequence, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.Load("demo")
	if after.Sequence != p.Sequence+1 {
		t.Fatal(after.Sequence)
	}
	statuses := map[string]string{}
	for _, e := range after.Latest() {
		statuses[e.ID] = e.Status
	}
	if statuses["c"] != "superseded" || statuses[replacement.ID] != "approved" {
		t.Fatal(statuses)
	}
}
func TestQueryHostScopeAndObservationBinding(t *testing.T) {
	s := queryFixture(t)
	scope := QueryContext{Project: "demo", Scope: Selection{Paths: []string{"core/a.go"}, Tags: []string{"build"}, Limit: 2, Bytes: 1024}}
	for _, q := range []Query{{Text: "E401", Paths: []string{"other"}}, {Text: "E401", Tags: []string{"foreign"}}, {Text: "E401", Budget: QueryBudget{Entries: 3}}} {
		if _, err := s.Queries().SearchWithin(context.Background(), scope, q); err == nil {
			t.Fatal(q)
		}
	}
	scope.Scope.Tags = nil
	scope.Scope.Bytes = 0
	v, err := s.Queries().SearchWithin(context.Background(), scope, Query{Text: "E401"})
	if err != nil || len(v.Entries) != 1 {
		t.Fatal(v, err)
	}
}

type observationFunc func(context.Context) (ObservedFacts, error)

func (f observationFunc) Observe(ctx context.Context) (ObservedFacts, error) { return f(ctx) }
func TestObservationIsAuthorizedBeforeIOAndBindingChecked(t *testing.T) {
	s := queryFixture(t)
	denied := s
	denied.AllowedProjects = []string{}
	observed := false
	observer := observationFunc(func(context.Context) (ObservedFacts, error) {
		observed = true
		return ObservedFacts{Values: map[string]string{"node": "24"}, Binding: "wrong"}, nil
	})
	if _, err := denied.Queries().SearchWithin(context.Background(), QueryContext{Project: "demo", Observations: observer}, Query{Text: "E401"}); err == nil || observed {
		t.Fatal("observation before authorization", err)
	}
	if _, err := s.Queries().SearchWithin(context.Background(), QueryContext{Project: "demo", Observations: observer}, Query{Text: "E401"}); err == nil {
		t.Fatal("invalid observation binding accepted")
	}
}
