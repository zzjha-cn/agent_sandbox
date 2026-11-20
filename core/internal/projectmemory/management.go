package projectmemory

import (
	"context"
	"errors"
	"strings"
	"time"
)

// KnowledgeRepository keeps one authoritative mutation and its audit append in
// one atomic commit. Implementations bind project grants before calling mutate.
type KnowledgeRepository interface {
	ProjectReader
	UpdateProject(context.Context, string, uint64, func(*Project) error) error
}
type ManagementService struct {
	repository KnowledgeRepository
	queries    *QueryService
	actor      *ReviewIdentity
}

func NewManagementService(repository KnowledgeRepository, queries *QueryService, actor *ReviewIdentity) *ManagementService {
	return &ManagementService{repository, queries, actor}
}
func (s *ManagementService) SubmitCandidate(ctx context.Context, id string, v Input, sequence uint64) (Entry, error) {
	if e := v.Validate(); e != nil {
		return Entry{}, e
	}
	var n Entry
	err := s.repository.UpdateProject(ctx, id, sequence, func(p *Project) error {
		if v.Supersedes != "" {
			found := false
			for _, old := range p.Latest() {
				if old.ID == v.Supersedes && old.Status == "approved" {
					found = true
				}
			}
			if !found {
				return errors.New("supersedes must identify an approved entry in this project")
			}
		}
		n = Entry{Input: v, ID: ID(), Revision: 1, Status: "candidate", CreatedAt: time.Now().UTC()}
		n.Digest = entryDigest(n)
		p.History = append(p.History, n)
		return nil
	})
	return n, err
}
func (s *ManagementService) Review(ctx context.Context, project, id, digest, status, reviewer, reason string, sequence uint64, due *time.Time) (Entry, error) {
	if s.actor != nil {
		reviewer = s.actor.Subject
		if sequence == 0 {
			return Entry{}, errors.New("organization review requires expected project sequence")
		}
	}
	if due != nil && status != "approved" && status != "renewed" {
		return Entry{}, errors.New("review due only applies to approval or renewal")
	}
	if due != nil && !due.After(time.Now()) {
		return Entry{}, errors.New("review due must be in the future")
	}
	if digest == "" || strings.TrimSpace(reviewer) == "" || strings.TrimSpace(reason) == "" {
		return Entry{}, errors.New("digest, reviewer and reason are required")
	}
	if len(reviewer) > 256 || len(reason) > 2048 {
		return Entry{}, errors.New("review metadata too large")
	}
	var current Entry
	err := s.repository.UpdateProject(ctx, project, sequence, func(p *Project) error {
		for _, v := range p.Latest() {
			if v.ID == id {
				current = v
			}
		}
		if current.ID == "" || current.Digest != digest {
			return errors.New("entry absent or changed; review its current digest")
		}
		switch status {
		case "approved", "rejected":
			if current.Status != "candidate" {
				return errors.New("only candidates may be approved or rejected")
			}
		case "renewed":
			if current.Status != "approved" || due == nil {
				return errors.New("renew requires an approved entry and future review due")
			}
			status = "approved"
		case "archived":
			if current.Status != "approved" {
				return errors.New("only approved entries may be archived")
			}
		default:
			return errors.New("invalid review transition")
		}
		if status == "approved" && current.Status == "candidate" && current.Supersedes != "" {
			var old Entry
			for _, v := range p.Latest() {
				if v.ID == current.Supersedes {
					old = v
				}
			}
			if old.Status != "approved" {
				return errors.New("superseded entry changed; submit a new candidate")
			}
			now := time.Now().UTC()
			old.ReviewedAt = &now
			old.Status = "superseded"
			old.Revision++
			old.Reviewer = reviewer
			old.ReviewIdentity = s.actor
			old.Reason = reason
			old.Digest = entryDigest(old)
			p.History = append(p.History, old)
		}
		now := time.Now().UTC()
		current.ReviewedAt = &now
		current.Status = status
		current.Revision++
		current.Reviewer = reviewer
		current.ReviewIdentity = s.actor
		if status == "approved" {
			current.ReviewDue = due
		}
		current.Reason = reason
		current.Digest = entryDigest(current)
		p.History = append(p.History, current)
		return nil
	})
	return current, err
}
