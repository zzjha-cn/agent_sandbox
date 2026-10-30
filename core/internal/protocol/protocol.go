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
	SchemaVersion      string             `json:"schema_version"`
	ApplicationVersion string             `json:"application_version"`
	Providers          []string           `json:"providers"`
	ArtifactDelivery   string             `json:"artifact_delivery"`
	Capabilities       []string           `json:"capabilities"`
	MaxTimeoutSeconds  int                `json:"max_timeout_seconds"`
	Schemas            []string           `json:"schemas"`
	CapabilityDetails  []CapabilityDetail `json:"capability_details"`
	RecordSchema       string             `json:"record_schema"`
	Compatibility      Compatibility      `json:"compatibility"`
}

func Version(appVersion string) VersionInfo {
	return VersionInfo{SchemaVersion: SchemaVersion, ApplicationVersion: appVersion,
		Providers: []string{"claude", "codex"}, ArtifactDelivery: "local_directory,git_local",
		Capabilities: []string{"task.v2", "admit.v2", "events.jsonl.v2", "timeout", "source.files", "source.git", "artifact.snapshot", "criteria.files", "criteria.tests", "criteria.manual", "criteria.external_state", "tools.records.get", "operations.record_http", "organization.ceilings", "events.team-outbox"}, MaxTimeoutSeconds: MaxTimeoutSeconds, Schemas: SchemaNames(), CapabilityDetails: capabilityDetails(), RecordSchema: execution.RecordSchema, Compatibility: Compatibility{InputVersions: []string{SchemaVersion}, RecordVersions: []string{execution.RecordSchema}, V1Records: "preserve_and_reconcile_with_original_binary", AdditiveOutputFields: true}}
}

type CapabilityDetail struct {
	Name         string   `json:"name"`
	Requirements []string `json:"requirements"`
}
type Compatibility struct {
	InputVersions        []string `json:"input_versions"`
	RecordVersions       []string `json:"record_versions"`
	V1Records            string   `json:"v1_records"`
	AdditiveOutputFields bool     `json:"additive_output_fields"`
}

// Implementation constraints, not a grant of local authority.
func capabilityDetails() []CapabilityDetail {
	return []CapabilityDetail{
		{Name: "organization.ceilings", Requirements: []string{"root-owned /etc/sbx/organization.toml", "synchronous TaskSpec only when managed", "dedicated Docker daemon/VM for mutually untrusted operators", "policy changes cancel within 250ms plus bounded cleanup; external effects require reconciliation"}},
		{Name: "events.team-outbox", Requirements: []string{"managed policy", "private durable local outbox", "explicit events flush with per-operator collector credential", "collector authenticates subject and deduplicates stable event id"}},
		{Name: "task.v2", Requirements: []string{"explicit provider", "headless synchronous execution", "Docker", "current operator policy"}},
		{Name: "tools.records.get", Requirements: []string{"authorized unexpired read grant", "dedicated proxy", "bounded MCP HTTP", "supported pinned provider version; admission is authoritative"}},
		{Name: "operations.record_http", Requirements: []string{"predeclared operation and write/lookup/read grants", "host-held credentials", "sandbox stopped before submission", "unknown outcomes query-only until reconciled"}},
		{Name: "artifact.delivery", Requirements: []string{"local_directory or git_local grant", "verified artifact or explicit verification opt-out", "no implicit push or merge"}},
	}
}
