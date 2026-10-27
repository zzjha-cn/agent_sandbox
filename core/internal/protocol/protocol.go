// Package protocol contains the versioned machine-facing execution contract.
package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	if !utf8.Valid(data) {
		return Envelope{}, invalid("envelope must be UTF-8")
	}
	fields, err := envelopeFields(data)
	if err != nil {
		return Envelope{}, invalid("%v", err)
	}
	if raw, ok := fields["timeout_seconds"]; ok {
		value, err := timeoutInteger(raw)
		if err != nil {
			return Envelope{}, invalid("timeout_seconds: %v", err)
		}
		fields["timeout_seconds"] = json.RawMessage(strconv.Itoa(value))
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return Envelope{}, invalid("decode envelope: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(normalized))
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

func envelopeFields(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("envelope must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	allowed := map[string]bool{"schema_version": true, "task_id": true, "prompt": true, "base": true, "fresh": true, "timeout_seconds": true}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("object key is not a string")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		if !allowed[key] {
			return nil, fmt.Errorf("unknown field %q", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("field %q must not be null", key)
		}
		fields[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return fields, nil
}

// JSON Schema integers include 1.0 and 1e2. Normalize exactly, with bounded
// string operations rather than floating-point rounding or huge big.Ints.
func timeoutInteger(raw json.RawMessage) (int, error) {
	number := strings.TrimSpace(string(raw))
	if len(number) == 0 || (number[0] != '-' && (number[0] < '0' || number[0] > '9')) {
		return 0, fmt.Errorf("must be an integer")
	}
	negative := strings.HasPrefix(number, "-")
	number = strings.TrimPrefix(number, "-")
	coefficient, exponent := number, "0"
	if i := strings.IndexAny(number, "eE"); i >= 0 {
		coefficient, exponent = number[:i], number[i+1:]
	}
	fraction := 0
	if i := strings.IndexByte(coefficient, '.'); i >= 0 {
		fraction = len(coefficient) - i - 1
		coefficient = coefficient[:i] + coefficient[i+1:]
	}
	digits := strings.TrimLeft(coefficient, "0")
	if digits == "" {
		return 0, nil
	}
	exp, err := strconv.ParseInt(exponent, 10, 64)
	if err != nil || exp > int64(len(raw))+6 || exp < -int64(len(raw))-6 {
		return 0, fmt.Errorf("must be an integer between 0 and %d", MaxTimeoutSeconds)
	}
	scale := exp - int64(fraction)
	if scale < 0 {
		cut := -scale
		if cut > int64(len(digits)) || strings.Trim(digits[int64(len(digits))-cut:], "0") != "" {
			return 0, fmt.Errorf("must be an integer")
		}
		digits = digits[:int64(len(digits))-cut]
	} else {
		if int64(len(digits))+scale > int64(len(strconv.Itoa(MaxTimeoutSeconds))) {
			return 0, fmt.Errorf("exceeds maximum timeout")
		}
		digits += strings.Repeat("0", int(scale))
	}
	if negative || len(digits) > len(strconv.Itoa(MaxTimeoutSeconds)) {
		return 0, fmt.Errorf("must be between 0 and %d", MaxTimeoutSeconds)
	}
	value, err := strconv.Atoi(digits)
	if err != nil || value > MaxTimeoutSeconds {
		return 0, fmt.Errorf("must be between 0 and %d", MaxTimeoutSeconds)
	}
	return value, nil
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
