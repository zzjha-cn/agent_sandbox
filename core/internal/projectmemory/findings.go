package projectmemory

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// FindingReview acknowledges an issue without changing entry approval. Its ID
// binds the exact finding inputs, so a newer revision creates a new review item.
type FindingReview struct {
	FindingID string          `json:"finding_id"`
	Decision  string          `json:"decision"`
	Actor     string          `json:"actor"`
	Reason    string          `json:"reason"`
	Identity  *ReviewIdentity `json:"review_identity,omitempty"`
	At        time.Time       `json:"at"`
	Digest    string          `json:"digest"`
}
type ReviewFinding struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Sources []EntryRef     `json:"sources"`
	Detail  string         `json:"detail,omitempty"`
	Review  *FindingReview `json:"review,omitempty"`
}
type ReviewQueue struct {
	Schema   string          `json:"schema_version"`
	Project  string          `json:"project_id"`
	Sequence uint64          `json:"sequence"`
	Findings []ReviewFinding `json:"findings"`
}

func findingReviewDigest(r FindingReview) string { r.Digest = ""; return Digest(r) }
func (s *ManagementService) findings(ctx context.Context, p Project, facts map[string]string) (ReviewQueue, error) {
	out := ReviewQueue{Schema: "sbx.memory-review-queue/v1", Project: p.ID, Sequence: p.Sequence, Findings: []ReviewFinding{}}
	if err := ValidateFacts(facts); err != nil {
		return out, err
	}
	reviews := map[string]FindingReview{}
	for _, review := range p.FindingReviews {
		reviews[review.FindingID] = review
	}
	add := func(kind string, refs []EntryRef, detail string) {
		f := ReviewFinding{Kind: kind, Sources: refs, Detail: detail}
		f.ID = Digest(f)
		if review, ok := reviews[f.ID]; ok {
			copy := review
			f.Review = &copy
		}

		out.Findings = append(out.Findings, f)
	}
	groups := map[string][]Entry{}
	for _, e := range p.Latest() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if e.Status != "approved" && e.Status != "candidate" {
			continue
		}
		if e.Status == "approved" {
			if expired(e, time.Now()) {
				add("review_due", []EntryRef{reference(e)}, "")
			}
			state, err := s.queries.evidenceStatus(ctx, p.ID, e)
			if err != nil {
				return out, err
			}
			if state == EvidenceUnavailable {
				add("evidence_unavailable", []EntryRef{reference(e)}, "")
			}
			if a := Applicability(e, facts); a != "satisfied" {
				add("conditions_"+a, []EntryRef{reference(e)}, Digest(facts))
			}
		}
		paths := append([]string(nil), e.Paths...)
		tags := append([]string(nil), e.Tags...)
		sort.Strings(paths)
		sort.Strings(tags)
		k := Digest([]any{e.Kind, normalized(e.Title), paths, tags})
		groups[k] = append(groups[k], e)
	}
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		contents := map[string]bool{}
		refs := []EntryRef{}
		for _, e := range group {
			contents[normalized(e.Content)] = true
			refs = append(refs, reference(e))
		}
		if len(contents) > 1 {
			add("possible_conflict", refs, "lexical_hint_only")
		}
	}
	historical := map[string]EntryRef{}
	for _, entry := range p.History {
		historical[entry.ID+":"+entry.Digest] = reference(entry)
	}
	for _, feedback := range p.Feedback {
		if feedback.Kind == "counterexample" {
			if ref, ok := historical[feedback.EntryID+":"+feedback.EntryDigest]; ok {
				add("counterexample", []EntryRef{ref}, feedback.ID)
			}
		}
	}

	sort.Slice(out.Findings, func(i, j int) bool { return out.Findings[i].ID < out.Findings[j].ID })
	return out, nil
}
func (s *ManagementService) ListReviewFindings(ctx context.Context, project string, facts map[string]string) (ReviewQueue, error) {
	p, err := s.repository.ReadProject(ctx, project)
	if err != nil {
		return ReviewQueue{}, err
	}
	return s.findings(ctx, p, facts)
}
func (s *ManagementService) ReviewFinding(ctx context.Context, project string, input FindingReview, sequence uint64, facts map[string]string) (FindingReview, error) {
	if s.actor != nil {
		input.Actor = s.actor.Subject
		if sequence == 0 {
			return input, errors.New("organization finding review requires expected project sequence")
		}
	}
	if input.Decision != "acknowledged" && input.Decision != "needs_update" && input.Decision != "dismissed" {
		return input, errors.New("unsupported finding review decision")
	}
	if strings.TrimSpace(input.Actor) == "" || len(input.Actor) > 256 || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 2048 {
		return input, errors.New("finding review requires bounded actor and reason")
	}
	err := s.repository.UpdateProject(ctx, project, sequence, func(p *Project) error {
		queue, err := s.findings(ctx, *p, facts)
		if err != nil {
			return err
		}
		found := false
		for _, f := range queue.Findings {
			if f.ID == input.FindingID {
				found = true
				break
			}
		}
		if !found {
			return errors.New("finding absent or changed; inspect current findings")
		}
		input.Identity = s.actor
		input.At = time.Now().UTC()
		input.Digest = findingReviewDigest(input)
		p.FindingReviews = append(p.FindingReviews, input)
		return nil
	})
	return input, err
}
func validateFindingReviews(p Project) error {
	for _, r := range p.FindingReviews {
		if r.Digest != findingReviewDigest(r) || r.FindingID == "" || r.Actor == "" || r.Reason == "" || r.At.IsZero() {
			return errors.New("invalid finding review history")
		}
		switch r.Decision {
		case "acknowledged", "needs_update", "dismissed":
		default:
			return errors.New("invalid finding review decision")
		}
	}
	return nil
}
