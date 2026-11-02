// Package taskmemory owns bounded task handoffs and isolated extraction jobs.
// It has no runtime, tool, transcript, or business-operation execution dependency.
package taskmemory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"sandx/internal/fsutil"
	"sandx/internal/projectmemory"
)

const HandoffSchema = "sbx.task-handoff/v1"
const JobSchema = "sbx.memory-extraction/v1"
const ExtractorVersion = "sbx-extractor/v1"
const MaxMaterial = 64 << 10

type Check struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code,omitempty"`
}
type Operation struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
}
type Stage struct {
	Phase string `json:"phase"`
	State string `json:"state"`
}

// Handoff facts are projected exclusively from the durable execution record.
// Open records are incomplete, not evidence that a running process has died.
type Handoff struct {
	OperatorSummary *Summary    `json:"operator_summary,omitempty"`
	Schema          string      `json:"schema_version"`
	TaskKey         string      `json:"task_key"`
	Attempt         string      `json:"attempt_id"`
	Sequence        uint64      `json:"record_sequence"`
	UpdatedAt       time.Time   `json:"updated_at"`
	Closed          bool        `json:"closed"`
	Incomplete      bool        `json:"incomplete"`
	Recovery        string      `json:"recovery"`
	State           string      `json:"state"`
	ExecutionState  string      `json:"execution_state"`
	AgentExitCode   *int        `json:"agent_exit_code,omitempty"`
	ValidationState string      `json:"validation_state"`
	DeliveryState   string      `json:"delivery_state"`
	ArtifactDigest  string      `json:"artifact_digest,omitempty"`
	ReceiptDigest   string      `json:"receipt_digest,omitempty"`
	InputDigest     string      `json:"input_digest,omitempty"`
	Stages          []Stage     `json:"stages"`
	Validation      []Check     `json:"validation"`
	Operations      []Operation `json:"operations"`
	Digest          string      `json:"digest"`
}

func TaskDir(home, key string) string {
	return filepath.Join(home, "task-memory", strings.TrimPrefix(projectmemory.Digest(key), "sha256:"))
}
func (h Handoff) digest() string { h.Digest = ""; return projectmemory.Digest(h) }
func SaveHandoff(home string, h Handoff) error {
	if h.Schema != HandoffSchema || h.TaskKey == "" || !projectmemory.ValidID(h.Attempt) {
		return errors.New("invalid handoff identity")
	}
	var e error
	h, e = WithSummary(home, h)
	if e != nil {
		return e
	}
	h.Digest = h.digest()
	return save(filepath.Join(TaskDir(home, h.TaskKey), "handoff-"+h.Attempt+".json"), h)
}
func ReadHandoff(home, key, attempt string) (Handoff, error) {
	var h Handoff
	if !projectmemory.ValidID(attempt) {
		return h, errors.New("invalid attempt ID")
	}
	e := read(filepath.Join(TaskDir(home, key), "handoff-"+attempt+".json"), &h)
	if e == nil && (h.Schema != HandoffSchema || h.TaskKey != key || h.Attempt != attempt || h.Digest != h.digest()) {
		e = errors.New("invalid handoff digest or identity")
	}
	return h, e
}
func SealHandoff(h Handoff) Handoff { h.Digest = h.digest(); return h }
func save(name string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if len(b) > 1<<20 {
		return errors.New("memory document too large")
	}
	if e := os.MkdirAll(filepath.Dir(name), 0700); e != nil {
		return e
	}
	return fsutil.AtomicWrite(name, append(b, '\n'), 0600)
}
func decode(b []byte, v any) error {
	if !utf8.Valid(b) || len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
		return errors.New("expected a UTF-8 JSON object")
	}
	probe := json.NewDecoder(bytes.NewReader(b))
	if e := uniqueKeys(probe, 0); e != nil {
		return e
	}

	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON document")
	}
	return nil
}
func read(name string, v any) error {
	b, e := projectmemory.ReadFile(name, 1<<20)
	if e != nil {
		return e
	}
	return decode(b, v)
}

// Summary is an operator-authorized safe summary, not verification evidence.
// Reading this file is explicit; jobs never scan logs, workspaces or sessions.
type Summary struct {
	Goal        string   `json:"goal,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
	Completed   []string `json:"completed,omitempty"`
	Todo        []string `json:"todo,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	Next        []string `json:"next,omitempty"`
}

func DecodeSummary(b []byte) (Summary, error) {
	var s Summary
	if len(b) > 16<<10 {
		return s, errors.New("summary exceeds 16 KiB")
	}
	e := decode(b, &s)
	if e == nil {
		canonical, _ := json.Marshal(s)
		if len(canonical) > 16<<10 {
			return s, errors.New("serialized summary exceeds 16 KiB")
		}
		e = checkSafe(canonical)
	}
	return s, e
}

type Material struct {
	Handoff Handoff `json:"handoff"`
	Summary Summary `json:"operator_summary"`
}

func (m Material) Validate() error {
	if m.Handoff.Schema != HandoffSchema || m.Handoff.Digest != m.Handoff.digest() || !projectmemory.ValidID(m.Handoff.Attempt) || m.Handoff.TaskKey == "" {
		return errors.New("invalid handoff material")
	}
	b, _ := json.Marshal(m)
	if len(b) > MaxMaterial {
		return errors.New("extraction material exceeds 64 KiB")
	}
	return checkSafe(b)
}

// Operator notes live separately so factual checkpoint projection never erases
// a handoff's goal or pending work and never mistakes notes for test evidence.
func SaveSummary(home, key, attempt string, s Summary) error {
	if !projectmemory.ValidID(attempt) || key == "" {
		return errors.New("invalid handoff summary identity")
	}
	b, _ := json.Marshal(s)
	if _, e := DecodeSummary(b); e != nil {
		return e
	}
	name := filepath.Join(TaskDir(home, key), "summary-"+attempt+".json")
	if e := os.MkdirAll(filepath.Dir(name), 0700); e != nil {
		return e
	}
	// Keep canonical bytes within the same bound used by readers; indentation
	// must not make a valid summary unreadable on the next checkpoint.
	return fsutil.AtomicWrite(name, b, 0600)
}
func WithSummary(home string, h Handoff) (Handoff, error) {
	b, e := projectmemory.ReadFile(filepath.Join(TaskDir(home, h.TaskKey), "summary-"+h.Attempt+".json"), 16<<10)
	if os.IsNotExist(e) {
		h.OperatorSummary = nil
		return SealHandoff(h), nil
	}
	if e != nil {
		return h, e
	}
	s, e := DecodeSummary(b)
	if e != nil {
		return h, e
	}
	h.OperatorSummary = &s
	return SealHandoff(h), nil
}

func uniqueKeys(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting exceeds limit")
	}
	token, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		seen := map[string]bool{}
		for d.More() {
			token, e = d.Token()
			if e != nil {
				return e
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("duplicate JSON key")
			}
			seen[key] = true
			if e = uniqueKeys(d, depth+1); e != nil {
				return e
			}
		}
	} else if delim == '[' {
		for d.More() {
			if e = uniqueKeys(d, depth+1); e != nil {
				return e
			}
		}
	} else {
		return errors.New("invalid JSON delimiter")
	}
	_, e = d.Token()
	return e
}
