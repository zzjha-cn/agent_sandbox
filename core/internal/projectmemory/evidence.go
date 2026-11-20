package projectmemory

import (
	"context"
	"encoding/json"
	"sandx/internal/memoryio"
)

// FileEvidenceResolver is the adapter for registered extraction-job files.
// QueryService depends only on EvidenceResolver, not this layout or job wire type.
type EvidenceDocumentReader interface {
	ReadEvidence(context.Context, string) ([]byte, error)
}

// EvidenceDocumentResolver verifies source identity without knowing storage paths.
type EvidenceDocumentResolver struct{ Reader EvidenceDocumentReader }

// FileEvidenceResolver is a compatibility composition adapter, not query policy.
type FileEvidenceResolver struct{ Home string }

func (s FileEvidenceResolver) ResolveEvidence(ctx context.Context, project string, p Provenance) (EvidenceState, error) {
	return (EvidenceDocumentResolver{Reader: memoryio.ExtractionFiles{Home: s.Home, ReadFile: ReadFile}}).ResolveEvidence(ctx, project, p)
}

func (s EvidenceDocumentResolver) ResolveEvidence(ctx context.Context, project string, p Provenance) (EvidenceState, error) {
	if err := ctx.Err(); err != nil {
		return EvidenceUnavailable, err
	}
	if s.Reader == nil {
		return EvidenceUnavailable, nil
	}
	if !ValidID(p.Job) {
		return EvidenceUnavailable, nil
	}
	b, err := s.Reader.ReadEvidence(ctx, p.Job)
	if err != nil {
		return EvidenceUnavailable, nil
	}
	var job struct {
		ID             string          `json:"id"`
		Project        string          `json:"project_id"`
		MaterialDigest string          `json:"material_digest"`
		OutputDigest   string          `json:"output_digest"`
		Material       json.RawMessage `json:"material"`
		Output         json.RawMessage `json:"output"`
	}
	if json.Unmarshal(b, &job) != nil || job.ID != p.Job || job.Project != project || job.MaterialDigest != p.MaterialDigest || job.OutputDigest != p.OutputDigest || Digest(job.Material) != p.MaterialDigest || Digest(job.Output) != p.OutputDigest {
		return EvidenceUnavailable, nil
	}
	return EvidenceAvailable, ctx.Err()
}

// EvidenceStatus preserves the legacy convenience API conservatively on errors.
func (s Store) EvidenceStatus(project string, entry Entry) string {
	state, err := s.Queries().evidenceStatus(context.Background(), project, entry)
	if err != nil {
		return string(EvidenceUnavailable)
	}
	return string(state)
}
