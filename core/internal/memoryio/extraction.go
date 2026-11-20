// Package memoryio contains host storage adapters, without knowledge policy.
package memoryio

import (
	"context"
	"path/filepath"
)

// ExtractionFiles alone knows the task-extraction job directory layout. The
// caller supplies its bounded, no-follow reader; IDs were validated upstream.
type ExtractionFiles struct {
	Home     string
	ReadFile func(string, int64) ([]byte, error)
}

func (s ExtractionFiles) ReadEvidence(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := s.ReadFile(filepath.Join(s.Home, "memory-extractions", id, "job.json"), 8<<20)
	if err != nil {
		return nil, err
	}
	return b, ctx.Err()
}
