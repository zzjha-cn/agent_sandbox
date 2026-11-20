package projectmemory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RecallLedger serializes a complete transaction across processes. A reserved
// record is committed before retrieval; failed/crashed reservations stay charged.
// Adapters must never expose uncommitted material to callers.
type RecallLedger interface {
	Update(context.Context, func(*RecallHistory) error) error
}
type RecallHistory struct {
	Schema  string         `json:"schema_version"`
	Binding string         `json:"binding,omitempty"`
	Records []RecallRecord `json:"records"`
}
type RecallRequest struct {
	Key   string `json:"key"`
	Query string `json:"query"`
}
type RecallResponse struct {
	Status  string `json:"status"`
	Context string `json:"context,omitempty"`
	Digest  string `json:"digest,omitempty"`
}
type RecallRecord struct {
	Key      string         `json:"key"`
	Attempt  string         `json:"attempt"`
	Query    string         `json:"query"`
	State    string         `json:"state"`
	Bytes    int            `json:"charged_bytes"`
	Millis   int64          `json:"charged_millis"`
	Snapshot *Snapshot      `json:"snapshot,omitempty"`
	Response RecallResponse `json:"response"`
	Digest   string         `json:"digest"`
}

// Limits bound local retrieval cost rather than pretending to measure model
// token charges. Failed calls and interrupted reservations consume task budget.
const RecallMaxCalls = 16
const RecallMaxBytes = 128 << 10
const RecallCallBytes = 16 << 10
const RecallTotalMillis int64 = 60000
const RecallTimeout = 5 * time.Second

type RecallService struct {
	Queries   *QueryService
	Ledger    RecallLedger
	Project   string
	Attempt   string
	Selection Selection
	Facts     func(context.Context) (map[string]string, error)
	// Authorize revalidates host policy at query, delivery and recovery boundaries.
	Authorize func(context.Context) error
}

func recallBytes(out RecallResponse) int {
	b, _ := json.Marshal(out)
	quoted, _ := json.Marshal(string(b))
	return len(b) + len(quoted) + 512
}

func recallDigest(r RecallRecord) string { r.Digest = ""; return Digest(r) }
func (s RecallService) validate(ctx context.Context, snap *Snapshot) error {
	if s.Authorize != nil {
		if err := s.Authorize(ctx); err != nil {
			return err
		}
	}
	facts := s.Selection.Facts
	if s.Facts != nil {
		var err error
		facts, err = s.Facts(ctx)
		if err != nil {
			return err
		}
	}
	if snap != nil {
		return s.Queries.ValidateSnapshotFacts(ctx, *snap, facts)
	}
	_, err := s.Queries.read(ctx, s.Project)
	return err
}
func (s RecallService) Recall(ctx context.Context, req RecallRequest) (RecallResponse, error) {
	if !ValidID(req.Key) || strings.TrimSpace(req.Query) == "" || s.Queries == nil || s.Ledger == nil || !ValidID(s.Attempt) {
		return RecallResponse{}, errors.New("invalid recall request")
	}
	q := s.Selection
	q.Query = req.Query
	q.Bytes = (RecallCallBytes - 1024) / 4
	if err := q.Validate(); err != nil {
		return RecallResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RecallTimeout)
	defer cancel()
	if err := s.validate(ctx, nil); err != nil {
		return RecallResponse{Status: "denied"}, nil
	}
	var out RecallResponse
	var reserved bool
	err := s.Ledger.Update(ctx, func(h *RecallHistory) error {
		bytes := 0
		var millis int64
		for _, r := range h.Records {
			if r.Digest != recallDigest(r) {
				return errors.New("invalid recall record digest")
			}
			bytes += r.Bytes
			millis += r.Millis
			if r.Key == req.Key {
				if r.Query != req.Query {
					return errors.New("recall key reused with different query")
				}
				if err := s.validate(ctx, r.Snapshot); err != nil {
					out = RecallResponse{Status: "revoked"}
					return nil
				}
				out = r.Response
				if r.State == "reserved" {
					out = RecallResponse{Status: "interrupted"}
				}
				return nil
			}
		}
		if len(h.Records) >= RecallMaxCalls || bytes+RecallCallBytes > RecallMaxBytes || millis+RecallTimeout.Milliseconds() > RecallTotalMillis {
			out = RecallResponse{Status: "budget_exhausted"}
			return nil
		}
		r := RecallRecord{Key: req.Key, Attempt: s.Attempt, Query: req.Query, State: "reserved", Bytes: RecallCallBytes, Millis: RecallTimeout.Milliseconds(), Response: RecallResponse{Status: "interrupted"}}
		r.Digest = recallDigest(r)
		h.Records = append(h.Records, r)
		reserved = true
		return nil
	})
	if err != nil || !reserved {
		return out, err
	}
	start := time.Now()
	if s.Facts != nil {
		q.Facts, err = s.Facts(ctx)
	}
	var snap Snapshot
	if err == nil {
		snap, err = s.Queries.SearchWithin(ctx, QueryContext{Project: s.Project, Scope: q}, Query{Text: q.Query})
	}
	out = RecallResponse{Status: "retrieval_failed"}
	if ctx.Err() != nil {
		out.Status = "timeout"
	} else if err == nil {
		if err = s.validate(ctx, &snap); err == nil {
			out = RecallResponse{Status: "ok", Context: snap.Context, Digest: snap.Digest}
			if len(snap.Entries) == 0 {
				out = RecallResponse{Status: "empty"}
			}
		} else {
			out = RecallResponse{Status: "revoked"}
		}
	}
	// Bound both the structured payload and its text fallback, including JSON
	// escaping and a conservative transport envelope. Oversized material is never
	// delivered or recorded as consumed.
	if recallBytes(out) > RecallCallBytes {
		out = RecallResponse{Status: "retrieval_failed"}
	}
	// Persist even when request was cancelled; never deliver material if this fails.
	commitCtx, done := context.WithTimeout(context.WithoutCancel(ctx), RecallTimeout)
	defer done()
	err = s.Ledger.Update(commitCtx, func(h *RecallHistory) error {
		for i := range h.Records {
			r := &h.Records[i]
			if r.Key != req.Key {
				continue
			}
			if r.State != "reserved" || r.Digest != recallDigest(*r) {
				return errors.New("invalid recall reservation")
			}
			r.State = "prepared_delivery_unknown"
			r.Response = out
			r.Millis = time.Since(start).Milliseconds()
			// Charge content plus a fixed bounded status envelope, including empty calls.
			r.Bytes = recallBytes(out)
			if out.Status == "ok" || out.Status == "empty" {
				r.Snapshot = &snap
			}
			r.Digest = recallDigest(*r)
			return nil
		}
		return errors.New("recall reservation missing")
	})
	if err != nil {
		return RecallResponse{}, err
	}
	if out.Status == "ok" || out.Status == "empty" {
		if err = s.validate(ctx, &snap); err != nil {
			return RecallResponse{Status: "revoked"}, nil
		}
	}

	return out, nil
}

// ValidateHistory rejects resuming a conversation containing revoked references.
// It never re-runs retrieval or mutates already frozen material.
func (s RecallService) ValidateHistory(ctx context.Context) error {
	return s.Ledger.Update(ctx, func(h *RecallHistory) error {
		for _, r := range h.Records {
			if r.Digest != recallDigest(r) {
				return errors.New("invalid recall record digest")
			}
			if err := s.validate(ctx, r.Snapshot); err != nil {
				return err
			}
		}
		return nil
	})
}
