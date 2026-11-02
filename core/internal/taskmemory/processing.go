package taskmemory

import (
	"errors"
	"path/filepath"
	"sandx/internal/projectmemory"
	"time"
)

const ProcessingSchema = "sbx.memory-processing/v1"

// Processing is an independently queryable automatic post-processing outcome.
// It never changes the execution protocol's terminal result or recovery gate.
type Processing struct {
	Schema    string    `json:"schema_version"`
	TaskKey   string    `json:"task_key"`
	Attempt   string    `json:"attempt_id"`
	State     string    `json:"state"`
	Job       string    `json:"job_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func SaveProcessing(home string, p Processing) error {
	if p.TaskKey == "" || !projectmemory.ValidID(p.Attempt) {
		return errors.New("invalid processing identity")
	}
	p.Schema = ProcessingSchema
	p.UpdatedAt = time.Now().UTC()
	return save(filepath.Join(TaskDir(home, p.TaskKey), "processing-"+p.Attempt+".json"), p)
}
func ReadProcessing(home, key, attempt string) (Processing, error) {
	var p Processing
	if !projectmemory.ValidID(attempt) {
		return p, errors.New("invalid attempt ID")
	}
	e := read(filepath.Join(TaskDir(home, key), "processing-"+attempt+".json"), &p)
	if e == nil && (p.Schema != ProcessingSchema || p.TaskKey != key || p.Attempt != attempt) {
		e = errors.New("invalid processing identity")
	}
	if e == nil && p.Job != "" {
		j, err := (Jobs{Home: home}).Load(p.Job)
		if err != nil {
			return p, err
		}
		p.State = j.State
		p.Error = j.Error
		p.UpdatedAt = j.UpdatedAt
	}
	return p, e
}
