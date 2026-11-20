package projectmemory

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Feedback is an operator assertion, not verification or a review transition.
type Feedback struct {
	ID          string          `json:"id"`
	EntryID     string          `json:"entry_id"`
	EntryDigest string          `json:"entry_digest"`
	Kind        string          `json:"kind"`
	Task        string          `json:"task,omitempty"`
	Note        string          `json:"note"`
	Actor       string          `json:"actor"`
	Identity    *ReviewIdentity `json:"review_identity,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	Digest      string          `json:"digest"`
}

func feedbackDigest(v Feedback) string { v.Digest = ""; return Digest(v) }
func (v Feedback) validate() error {
	switch v.Kind {
	case "retrieved", "injected", "helpful", "validated", "counterexample":
	default:
		return errors.New("unsupported feedback kind")
	}
	if !ValidID(v.EntryID) || v.EntryDigest == "" || strings.TrimSpace(v.Note) == "" || len(v.Note) > 1024 || len(v.Task) > 256 || v.Actor == "" || len(v.Actor) > 256 {
		return errors.New("feedback requires an entry digest, actor and bounded explanation")
	}
	return nil
}

func (s Store) RecordFeedback(project string, input Feedback) (Feedback, error) {
	return s.Management().RecordFeedback(context.Background(), project, input, 0)
}

func (s *ManagementService) RecordFeedback(ctx context.Context, project string, input Feedback, sequence uint64) (Feedback, error) {
	if s.actor != nil {
		input.Actor = s.actor.Subject
	}
	if err := input.validate(); err != nil {
		return Feedback{}, err
	}
	err := s.repository.UpdateProject(ctx, project, sequence, func(p *Project) error {
		found := false
		for _, entry := range p.History {
			if entry.ID == input.EntryID && entry.Digest == input.EntryDigest {
				found = true
				break
			}
		}
		if !found {
			return errors.New("feedback entry revision not found in this project")
		}
		input.ID = ID()
		input.Identity = s.actor
		input.CreatedAt = time.Now().UTC()
		input.Digest = feedbackDigest(input)
		p.Feedback = append(p.Feedback, input)
		return nil
	})
	return input, err
}

func validateFeedbackHistory(p Project) error {
	entries := map[string]bool{}
	for _, e := range p.History {
		entries[e.ID+":"+e.Digest] = true
	}
	seen := map[string]bool{}
	for _, v := range p.Feedback {
		if err := v.validate(); err != nil {
			return err
		}
		if !ValidID(v.ID) || seen[v.ID] || v.Digest != feedbackDigest(v) || !entries[v.EntryID+":"+v.EntryDigest] {
			return errors.New("invalid feedback history/digest")
		}
		seen[v.ID] = true
	}
	return nil
}
