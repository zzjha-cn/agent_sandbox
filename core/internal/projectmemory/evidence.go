package projectmemory

import (
	"encoding/json"
	"path/filepath"
)

// EvidenceStatus checks only the registered, bounded extraction job. It never
// follows candidate-provided paths or scans provider transcripts. Availability
// proves material binding, not the truth of an extracted claim.
func (s Store) EvidenceStatus(project string, entry Entry) string {
	p := entry.Provenance
	if p == nil {
		if entry.Import != nil {
			return "imported_source"
		}
		return "manual_source"
	}
	if !ValidID(p.Job) {
		return "unavailable"
	}
	b, err := ReadFile(filepath.Join(s.Home, "memory-extractions", p.Job, "job.json"), maxStore)
	if err != nil {
		return "unavailable"
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
		return "unavailable"
	}
	return "available"
}
