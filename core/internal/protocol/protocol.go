// Package protocol exposes the single v2 machine contract.
package protocol

import (
	"io"
	"sandx/internal/execution"
)

const SchemaVersion = execution.TaskInputSchema
const MaxEnvelopeBytes = execution.MaxTaskBytes
const MaxTimeoutSeconds = 86400

type CodedError struct {
	Code string
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

func Decode(r io.Reader) (execution.TaskSpec, error) {
	spec, err := execution.DecodeTaskSpec(r)
	if err != nil {
		return spec, &CodedError{Code: "invalid_task", Err: err}
	}
	return spec, nil
}

type Event struct {
	SchemaVersion string `json:"schema_version"`
	execution.Event
}
type Result = execution.Result

func NewEventSink(w io.Writer) *execution.JSONLSink { return execution.NewJSONLSink(w) }

type VersionInfo struct {
	SchemaVersion      string   `json:"schema_version"`
	ApplicationVersion string   `json:"application_version"`
	Providers          []string `json:"providers"`
	ArtifactDelivery   string   `json:"artifact_delivery"`
	Capabilities       []string `json:"capabilities"`
	MaxTimeoutSeconds  int      `json:"max_timeout_seconds"`
}

func Version(appVersion string) VersionInfo {
	return VersionInfo{SchemaVersion: SchemaVersion, ApplicationVersion: appVersion,
		Providers: []string{"claude", "codex"}, ArtifactDelivery: "local_directory,git_local",
		Capabilities: []string{"task.v2", "admit.v2", "events.jsonl.v2", "timeout", "source.files", "source.git", "artifact.snapshot", "criteria.files", "criteria.tests", "criteria.manual", "criteria.external_state", "tools.records.get", "operations.record_http"}, MaxTimeoutSeconds: MaxTimeoutSeconds}
}
