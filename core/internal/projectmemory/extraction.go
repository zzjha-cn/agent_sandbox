package projectmemory

import (
	"errors"
	"fmt"
	"time"
)

// Provenance is host-generated and cannot be supplied by manual input or model output.
type Provenance struct {
	Job            string   `json:"job_id"`
	Task           string   `json:"task_key"`
	Attempt        string   `json:"attempt_id"`
	Extractor      string   `json:"extractor_version"`
	MaterialDigest string   `json:"material_digest"`
	OutputDigest   string   `json:"output_digest"`
	Evidence       []string `json:"evidence"`
}

// PublishExtraction commits a complete validated batch atomically. IDs depend on
// the frozen job, so crash recovery cannot publish duplicate candidates, even
// if an earlier batch was already reviewed before recovery.
func (s Store) PublishExtraction(project string, inputs []Input, refs []Provenance) ([]string, error) {
	if len(inputs) > 5 || len(inputs) != len(refs) {
		return nil, errors.New("invalid extraction batch")
	}
	for i, v := range inputs {
		if e := v.Validate(); e != nil {
			return nil, e
		}
		p := refs[i]
		if !ValidID(p.Job) || !ValidID(p.Attempt) || p.Task == "" || p.Extractor == "" || p.MaterialDigest == "" || p.OutputDigest == "" || len(p.Evidence) == 0 {
			return nil, errors.New("missing extraction provenance")
		}
		if v.Supersedes != "" {
			return nil, errors.New("extraction cannot supersede knowledge")
		}
	}
	u, e := s.lock()
	if e != nil {
		return nil, e
	}
	defer u()
	p, e := s.Load(project)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for i, v := range inputs {
		id := fmt.Sprintf("%s_%d", refs[i].Job, i)
		ids = append(ids, id)
		found := false
		for _, old := range p.History {
			if old.ID == id {
				if Digest(old.Input) != Digest(v) || Digest(old.Provenance) != Digest(&refs[i]) {
					return nil, errors.New("extraction publication mismatch")
				}
				found = true
				break
			}
		}
		if found {
			continue
		}
		n := Entry{Input: v, ID: id, Revision: 1, Status: "candidate", CreatedAt: time.Now().UTC(), Provenance: &refs[i]}
		n.Digest = entryDigest(n)
		p.History = append(p.History, n)
	}
	if len(inputs) == 0 {
		return ids, nil
	}
	p.Sequence++
	f, _ := s.projectPath(project)
	return ids, write(f, p)
}
