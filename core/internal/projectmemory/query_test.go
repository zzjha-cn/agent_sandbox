package projectmemory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func queryFixture(t *testing.T) Store {
	t.Helper()
	s := Store{Home: t.TempDir()}
	created := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	p := Project{Schema: Schema, ID: "demo", Name: "demo", Sequence: 1, History: []Entry{}}
	for _, row := range []struct{ id, title, status string }{
		{"a", "Unrelated", "approved"}, {"b", "E401 pending", "candidate"},
		{"c", "E401 registry", "approved"}, {"d", "E401 expired", "approved"},
		{"e", "E401 wrong version", "approved"}, {"f", "Other", "approved"},
	} {
		e := Entry{Input: Input{Kind: "troubleshooting", Title: row.title, Content: "Check the registry", Source: "fixture"},
			ID: row.id, Revision: 1, Status: row.status, CreatedAt: created}
		if row.id == "d" {
			e.ReviewDue = &created
		}
		if row.id == "e" {
			e.Requires = map[string]string{"node": "20"}
		}
		e.Digest = entryDigest(e)
		p.History = append(p.History, e)
	}
	name, err := s.projectPath("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err = write(name, p); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestQueryLegacySnapshotDigests(t *testing.T) {
	s := queryFixture(t)
	for query, expected := range map[string]string{
		"":     "sha256:230d5c5aeb3664cc51f9249456b1b189d2379b9fb6afe50462716520d0e9589e",
		"E401": "sha256:10fbf06638124dad683f467eb5b69ee244c7b18d998211dc7f244500a7a80507",
	} {
		v, err := s.Select("demo", Selection{Query: query, Facts: map[string]string{"node": "24"}})
		if err != nil {
			t.Fatal(err)
		}
		if v.Digest != expected {
			t.Fatalf("legacy query %q snapshot changed: %s", query, v.Digest)
		}
	}
}

type readerFunc func(context.Context, string) (Project, error)

func (f readerFunc) ReadProject(ctx context.Context, id string) (Project, error) { return f(ctx, id) }

type evidenceFunc func(context.Context, string, Provenance) (EvidenceState, error)

func (f evidenceFunc) ResolveEvidence(ctx context.Context, id string, p Provenance) (EvidenceState, error) {
	return f(ctx, id, p)
}

type retrieverFunc func(context.Context, string, []RetrievalCandidate) (RetrievalResult, error)

func (f retrieverFunc) Retrieve(ctx context.Context, q string, c []RetrievalCandidate) (RetrievalResult, error) {
	return f(ctx, q, c)
}

func TestQueryReplaceableRetrieverReceivesOnlyEligibleCorpus(t *testing.T) {
	s := queryFixture(t)
	p, err := s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	// Entire query use case can run with an in-memory view, without an sbx home,
	// task, provider, concrete file schema or command adapter.
	reader := readerFunc(func(context.Context, string) (Project, error) { return p, nil })
	evidence := evidenceFunc(func(context.Context, string, Provenance) (EvidenceState, error) {
		t.Fatal("manual source required no evidence I/O")
		return EvidenceUnavailable, nil
	})
	strategy := retrieverFunc(func(_ context.Context, q string, corpus []RetrievalCandidate) (RetrievalResult, error) {
		if len(corpus) != 3 || corpus[0].Ref.ID != "a" || corpus[1].Ref.ID != "c" || corpus[2].Ref.ID != "f" {
			t.Fatalf("ineligible corpus: %+v", corpus)
		}
		// Alternate strategy may select a semantic match with no literal terms.
		return RetrievalResult{Version: "test.semantic/v1", Matches: []RetrievalMatch{{Ref: corpus[2].Ref, Score: 9}}}, nil
	})
	service := NewQueryService(reader, evidence, strategy)
	v, err := service.Search(context.Background(), "demo", Selection{Query: "E401", Facts: map[string]string{"node": "24"}})
	if err != nil || len(v.Entries) != 1 || v.Entries[0].ID != "f" || v.Selector != "test.semantic/v1" {
		t.Fatal(v, err)
	}
	// Empty query remains legacy ID order and does not depend on the strategy.
	v, err = service.Search(context.Background(), "demo", Selection{Facts: map[string]string{"node": "24"}})
	if err != nil || len(v.Entries) != 3 || v.Entries[0].ID != "a" || v.Selector != SelectorVersion {
		t.Fatal(v, err)
	}
}

func TestQueryRejectsStaleForeignOrDuplicateStrategyReferences(t *testing.T) {
	s := queryFixture(t)
	for _, mode := range []string{"pending", "stale", "duplicate", "foreign", "bad_terms"} {
		t.Run(mode, func(t *testing.T) {
			strategy := retrieverFunc(func(_ context.Context, _ string, corpus []RetrievalCandidate) (RetrievalResult, error) {
				ref := corpus[0].Ref
				switch mode {
				case "pending":
					ref.ID = "b"
				case "stale":
					ref.Digest = "sha256:stale"
				case "foreign":
					ref.ID = "another-project-entry"
				}
				matches := []RetrievalMatch{{Ref: ref, Score: 1}}
				if mode == "duplicate" {
					matches = append(matches, matches[0])
				}
				if mode == "bad_terms" {
					matches[0].Matched = []string{"not-in-query"}
				}
				return RetrievalResult{Version: "test.cache/v1", Matches: matches}, nil
			})
			service := NewQueryService(StoreReader{s}, FileEvidenceResolver{Home: s.Home}, strategy)
			v, err := service.Search(context.Background(), "demo", Selection{Query: "E401", Facts: map[string]string{"node": "24"}})
			if !errors.Is(err, ErrInvalidRetrievalResult) || v.Digest != "" {
				t.Fatal("invalid strategy output delivered", v, err)
			}
		})
	}
}

func TestQueryEvidenceAdapterIsIndependentAndRevalidated(t *testing.T) {
	s := queryFixture(t)
	p, err := s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.History {
		if p.History[i].ID == "c" {
			p.History[i].Provenance = &Provenance{Job: "registered", MaterialDigest: "material", OutputDigest: "output"}
			p.History[i].Digest = entryDigest(p.History[i])
		}
	}
	reader := readerFunc(func(context.Context, string) (Project, error) { return p, nil })
	available := true
	resolver := evidenceFunc(func(_ context.Context, id string, source Provenance) (EvidenceState, error) {
		if id != "demo" || source.Job != "registered" {
			t.Fatal(id, source)
		}
		if available {
			return EvidenceAvailable, nil
		}
		return EvidenceUnavailable, nil
	})
	service := NewQueryService(reader, resolver, LexicalRetriever{})
	q := Selection{Query: "E401", Facts: map[string]string{"node": "24"}}
	v, err := service.Search(context.Background(), "demo", q)
	if err != nil || len(v.Entries) != 1 {
		t.Fatal(v, err)
	}
	available = false
	if err = service.ValidateSnapshot(context.Background(), v); err == nil {
		t.Fatal("missing source accepted")
	}
	missing, err := service.Search(context.Background(), "demo", q)
	if err != nil || len(missing.Entries) != 0 || missing.DecisionCounts["evidence_unavailable"] != 1 {
		t.Fatal(missing, err)
	}
	failed := errors.New("evidence unavailable due to adapter failure")
	resolver = evidenceFunc(func(context.Context, string, Provenance) (EvidenceState, error) { return EvidenceAvailable, failed })
	service = NewQueryService(reader, resolver, LexicalRetriever{})
	if v, err = service.Search(context.Background(), "demo", q); !errors.Is(err, failed) || v.Digest != "" {
		t.Fatal("failed resolver delivered body", v, err)
	}
}

func TestQueryOwnsExplanationFromCachedRanking(t *testing.T) {
	s := queryFixture(t)
	matched := []string{"e401"}
	strategy := retrieverFunc(func(_ context.Context, _ string, corpus []RetrievalCandidate) (RetrievalResult, error) {
		return RetrievalResult{Version: "test.cache/v1", Matches: []RetrievalMatch{{Ref: corpus[1].Ref, Score: 1, Matched: matched}}}, nil
	})
	service := NewQueryService(StoreReader{s}, FileEvidenceResolver{Home: s.Home}, strategy)
	v, err := service.Search(context.Background(), "demo", Selection{Query: "E401", Facts: map[string]string{"node": "24"}})
	if err != nil {
		t.Fatal(err)
	}
	matched[0] = "cache-evicted"
	if snapshotDigest(v) != v.Digest {
		t.Fatal("cache mutated frozen query explanation")
	}
}

func TestQueryRechecksAuthorityAfterRetrieverAndHonorsCancellation(t *testing.T) {
	s := queryFixture(t)
	strategy := retrieverFunc(func(ctx context.Context, q string, corpus []RetrievalCandidate) (RetrievalResult, error) {
		v, err := (LexicalRetriever{}).Retrieve(ctx, q, corpus)
		if err != nil {
			return v, err
		}
		if _, err = s.Review("demo", v.Matches[0].Ref.ID, v.Matches[0].Ref.Digest, "archived", "operator", "changed while retrieving"); err != nil {
			t.Fatal(err)
		}
		return v, nil
	})
	service := NewQueryService(StoreReader{s}, FileEvidenceResolver{Home: s.Home}, strategy)
	v, err := service.Search(context.Background(), "demo", Selection{Query: "E401", Facts: map[string]string{"node": "24"}})
	if err == nil || v.Digest != "" {
		t.Fatal("revoked result delivered", v, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Queries().Search(ctx, "demo", Selection{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	denied := Store{Home: s.Home, AllowedProjects: []string{"other"}}
	called := false
	strategy = retrieverFunc(func(context.Context, string, []RetrievalCandidate) (RetrievalResult, error) {
		called = true
		return RetrievalResult{}, nil
	})
	service = NewQueryService(StoreReader{denied}, FileEvidenceResolver{Home: s.Home}, strategy)
	if _, err = service.Search(context.Background(), "demo", Selection{Query: "E401"}); err == nil || called {
		t.Fatal("query bypassed project authorization", err)
	}
}
