// Package protocol contains the versioned machine-facing execution contract.
package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"sandx/internal/execution"
	"sandx/internal/workspace"
)

const (
	SchemaVersion     = "sbx.execution/v1"
	MaxEnvelopeBytes  = 1 << 20
	MaxTimeoutSeconds = 24 * 60 * 60
)

// Envelope v1 describes one headless attempt in the repository selected by
// the runner's working directory. task_id is a stable logical Task name;
// each accepted invocation receives a fresh attempt_id from the runner.
type Envelope struct {
	SchemaVersion  string `json:"schema_version"`
	TaskID         string `json:"task_id"`
	Prompt         string `json:"prompt"`
	Base           string `json:"base,omitempty"`
	Fresh          bool   `json:"fresh,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type CodedError struct {
	Code string
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

func Decode(r io.Reader) (Envelope, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxEnvelopeBytes+1))
	if err != nil {
		return Envelope{}, &CodedError{Code: "invalid_envelope", Err: err}
	}
	if len(data) > MaxEnvelopeBytes {
		return Envelope{}, invalid("envelope exceeds 1 MiB")
	}
	if err := rejectDuplicateTopLevelKeys(data); err != nil {
		return Envelope{}, invalid("%v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var env Envelope
	if err := dec.Decode(&env); err != nil {
		return Envelope{}, invalid("decode envelope: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Envelope{}, invalid("envelope must contain one JSON object")
		}
		return Envelope{}, invalid("trailing envelope data: %w", err)
	}
	if err := env.Validate(); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

func rejectDuplicateTopLevelKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("envelope must be a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("object key is not a string")
		}
		if seen[key] {
			return fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func invalid(format string, args ...any) error {
	return &CodedError{Code: "invalid_envelope", Err: fmt.Errorf(format, args...)}
}

func (e Envelope) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return invalid("schema_version must be %q", SchemaVersion)
	}
	if err := workspace.ValidateTaskName(e.TaskID); err != nil {
		return invalid("task_id must be a valid task name: %w", err)
	}
	if e.Prompt == "" {
		return invalid("prompt is required")
	}
	if e.TimeoutSeconds < 0 || e.TimeoutSeconds > MaxTimeoutSeconds {
		return invalid("timeout_seconds must be between 0 and %d", MaxTimeoutSeconds)
	}
	return nil
}

// Event is the stable JSONL representation; it deliberately omits internal Go
// fields and carries the schema version on every record.
type Event struct {
	SchemaVersion string              `json:"schema_version"`
	Type          execution.EventType `json:"type"`
	TaskID        string              `json:"task_id"`
	AttemptID     string              `json:"attempt_id"`
	Time          time.Time           `json:"time"`
	Phase         string              `json:"phase,omitempty"`
	Summary       string              `json:"summary,omitempty"`
	ErrorKind     execution.ErrorKind `json:"error_kind,omitempty"`
	Stage         string              `json:"stage,omitempty"`
	Result        *Result             `json:"result,omitempty"`
}

type Result struct {
	TaskID         string                  `json:"task_id"`
	AttemptID      string                  `json:"attempt_id"`
	State          execution.State         `json:"state"`
	AgentExitCode  *int                    `json:"agent_exit_code,omitempty"`
	ContainerState string                  `json:"container_state,omitempty"`
	ArtifactState  execution.ArtifactState `json:"artifact_state"`
	CleanupState   execution.CleanupState  `json:"cleanup_state"`
}

type EventSink struct{ encoder *json.Encoder }

func NewEventSink(w io.Writer) *EventSink { return &EventSink{encoder: json.NewEncoder(w)} }
func (s *EventSink) Emit(_ context.Context, event execution.Event) error {
	record := Event{SchemaVersion: SchemaVersion, Type: event.Type, TaskID: event.TaskID, AttemptID: event.AttemptID,
		Time: event.Time, Phase: protocolStage(event.Phase), Summary: event.Summary, ErrorKind: event.ErrorKind, Stage: protocolStage(event.Stage)}
	if event.Result != nil {
		r := event.Result
		record.Result = &Result{TaskID: r.TaskID, AttemptID: r.AttemptID, State: r.State,
			AgentExitCode: r.AgentExitCode, ContainerState: r.ContainerState, ArtifactState: r.ArtifactState, CleanupState: r.CleanupState}
	}
	return s.encoder.Encode(record)
}

func protocolStage(value string) string {
	switch value {
	case "admission":
		return "admission"
	case "inspect", "inspecting_task":
		return "inspect"
	case "provision", "creating task", "resuming task":
		return "provision"
	case "agent", "waiting_for_agent", "waiting for agent":
		return "agent"
	case "result":
		return "result"
	case "event_delivery", "writing execution events":
		return "delivery"
	case "":
		return ""
	default:
		return "internal"
	}
}

// VersionInfo is consumed by schedulers before submitting tasks.
type VersionInfo struct {
	SchemaVersion      string         `json:"schema_version"`
	ApplicationVersion string         `json:"application_version"`
	Providers          []string       `json:"providers"`
	ArtifactDelivery   string         `json:"artifact_delivery"`
	Capabilities       []string       `json:"capabilities"`
	MaxTimeoutSeconds  int            `json:"max_timeout_seconds"`
	EnvelopeSchema     map[string]any `json:"envelope_schema"`
}

func Version(appVersion string) VersionInfo {
	return VersionInfo{SchemaVersion: SchemaVersion, ApplicationVersion: appVersion, Providers: []string{"claude", "codex"}, ArtifactDelivery: "none",
		Capabilities: []string{"envelope.v1", "admit.v1", "events.jsonl.v1", "timeout"}, MaxTimeoutSeconds: MaxTimeoutSeconds,
		EnvelopeSchema: map[string]any{
			"$schema":              "https://json-schema.org/draft/2020-12/schema",
			"$id":                  SchemaVersion,
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"schema_version", "task_id", "prompt"},
			"properties": map[string]any{
				"schema_version":  map[string]any{"type": "string", "const": SchemaVersion},
				"task_id":         map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,39}$", "description": "Stable Task name within the runner workspace."},
				"prompt":          map[string]any{"type": "string", "minLength": 1},
				"base":            map[string]any{"type": "string"},
				"fresh":           map[string]any{"type": "boolean", "default": false},
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": MaxTimeoutSeconds, "default": 0},
			},
		},
	}
}
