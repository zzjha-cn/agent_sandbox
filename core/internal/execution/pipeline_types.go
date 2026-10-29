package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"sandx/internal/artifact"
	"sandx/internal/completion"
	"sandx/internal/external"
	"sandx/internal/sink"
	"sandx/internal/source"
	"sandx/internal/toolgate"
	"sandx/internal/workspace"
)

const GeneralRecordSchema = "sbx.execution-record/v2"
const TaskInputSchema = "sbx.execution/v2"

// TaskSpec is the staged general-task entrypoint. The execution/v1 envelope
// remains a separate decoder and protocol projection.
type TaskSpec struct {
	SchemaVersion string            `json:"schema_version"`
	TaskID        string            `json:"task_id"`
	Prompt        string            `json:"prompt"`
	Source        *source.Selection `json:"source,omitempty"`
	Agent         struct {
		Provider string `json:"provider"`
	} `json:"agent"`
	Tools      []toolgate.Selection   `json:"tools,omitempty"`
	Operations []external.Call        `json:"operations,omitempty"`
	Outputs    *OutputSelection       `json:"outputs,omitempty"`
	Criteria   []completion.Criterion `json:"criteria"`
	Sink       *SinkSelection         `json:"sink,omitempty"`
	Limits     struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	} `json:"limits"`
}

type OutputSelection struct {
	Include    []string `json:"include"`
	AllowEmpty bool     `json:"allow_empty"`
	Patch      bool     `json:"patch,omitempty"`
}

type SinkSelection = sink.Selection
type SinkGrant = sink.Grant

// PipelinePolicy is loaded exclusively from the operator-owned registry.
// Absence/revocation of a reference denies both new and resumed operations.
type PipelinePolicy struct {
	ToolGateway     toolgate.Policy            `toml:"tool_gateway"`
	Tools           map[string]external.Grant  `toml:"tools"`
	Sources         map[string]source.Grant    `toml:"sources"`
	Sinks           map[string]SinkGrant       `toml:"sinks"`
	Criteria        map[string]completion.Rule `toml:"criteria"`
	InputLimits     artifact.Limits            `toml:"input_limits"`
	ArtifactLimits  artifact.Limits            `toml:"artifact_limits"`
	Exclude         []string                   `toml:"exclude"`
	AllowUnverified bool                       `toml:"allow_unverified"`
}

func DefaultPipelinePolicy() PipelinePolicy {
	return PipelinePolicy{InputLimits: artifact.DefaultLimits(), ArtifactLimits: artifact.DefaultLimits()}
}

type PipelinePlan struct {
	Spec        TaskSpec
	Policy      PipelinePolicy
	Verifier    completion.TestRunner
	Reauthorize func() (PipelinePolicy, error)
}

func (p *PipelinePlan) refresh() error {
	if p.Reauthorize != nil {
		next, err := p.Reauthorize()
		if err != nil {
			return err
		}
		p.Policy = next
	}
	return p.Validate()
}

type ArtifactRef struct {
	ID                string            `json:"id"`
	Digest            string            `json:"digest"`
	Manifest          artifact.Manifest `json:"manifest"`
	WorkspaceManifest artifact.Manifest `json:"workspace_manifest"`
	Retention         string            `json:"retention"`
}

type ReviewRecord struct {
	ArtifactDigest string `json:"artifact_digest"`
	CriteriaDigest string `json:"criteria_digest"`
	Decision       string `json:"decision"`
	Reviewer       string `json:"reviewer"`
	Time           string `json:"time"`
}

type ProductReceipt = sink.Receipt

type PipelineFacts struct {
	Prepared      *source.Prepared   `json:"prepared,omitempty"`
	InputSnapshot string             `json:"input_snapshot,omitempty"`
	Quarantines   []string           `json:"quarantines,omitempty"`
	Artifact      *ArtifactRef       `json:"artifact,omitempty"`
	Validation    *completion.Result `json:"validation,omitempty"`
	Review        *ReviewRecord      `json:"review,omitempty"`
	Receipt       *ProductReceipt    `json:"receipt,omitempty"`
	// Selection excludes prompt and physical grant paths from status records.
	ToolsDigest      string                 `json:"tools_digest,omitempty"`
	ToolJournal      string                 `json:"tool_journal,omitempty"`
	OperationsDigest string                 `json:"operations_digest,omitempty"`
	Outputs          *OutputSelection       `json:"outputs,omitempty"`
	Criteria         []completion.Criterion `json:"criteria"`
	Sink             *SinkSelection         `json:"sink,omitempty"`
}

func contentBinding(value any) string {
	b, _ := json.Marshal(value)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (spec TaskSpec) ValidateInput() error {
	if spec.SchemaVersion != TaskInputSchema {
		return errors.New("unsupported general task schema")
	}
	if err := workspace.ValidateTaskName(spec.TaskID); err != nil {
		return err
	}
	if strings.TrimSpace(spec.Prompt) == "" || !utf8.ValidString(spec.Prompt) {
		return errors.New("a nonempty UTF-8 task prompt is required")
	}
	if spec.Agent.Provider != "claude" && spec.Agent.Provider != "codex" {
		return errors.New("unsupported agent provider")
	}
	if spec.Limits.TimeoutSeconds < 0 || spec.Limits.TimeoutSeconds > 86400 {
		return errors.New("timeout_seconds must be between 0 and 86400")
	}
	if spec.Source != nil {
		sel := spec.Source
		if sel.Ref == "" || sel.Kind != "files" && sel.Kind != "git" {
			return errors.New("source requires a git/files kind and logical ref")
		}
		if sel.Kind == "files" && (sel.Revision != "" || sel.WorkingTree || sel.Untracked) {
			return errors.New("files source does not accept Git options")
		}
		if sel.Untracked && !sel.WorkingTree {
			return errors.New("untracked requires working_tree")
		}
		if len(sel.Include) > 0 {
			if err := artifact.ValidateSelection(sel.Include); err != nil {
				return err
			}
		}
	}
	if spec.Outputs != nil {
		if err := artifact.ValidateSelection(spec.Outputs.Include); err != nil && !(spec.Outputs.Patch && len(spec.Outputs.Include) == 0) {
			return err
		}
	}
	if len(spec.Tools) > 16 {
		return errors.New("at most 16 tools are allowed")
	}
	toolIDs := map[string]bool{}
	for _, tool := range spec.Tools {
		if err := tool.Validate(); err != nil {
			return err
		}
		if toolIDs[tool.ID] {
			return errors.New("duplicate tool id")
		}
		toolIDs[tool.ID] = true
	}
	if len(spec.Operations) > 32 {
		return errors.New("at most 32 external operations are allowed")
	}
	seenOps := map[string]bool{}
	for _, call := range spec.Operations {
		if err := call.Validate(); err != nil {
			return err
		}
		if seenOps[call.ID] {
			return errors.New("duplicate operation id")
		}
		seenOps[call.ID] = true
	}
	seen := map[string]bool{}
	for _, criterion := range spec.Criteria {
		if criterion.ID == "" || criterion.Ref == "" || seen[criterion.ID] {
			return errors.New("criteria require distinct ids and logical refs")
		}
		seen[criterion.ID] = true
		if criterion.Kind != "files" && criterion.Kind != "tests" && criterion.Kind != "manual" && criterion.Kind != "external_state" {
			return errors.New("unsupported criterion kind")
		}
		if spec.Outputs == nil && criterion.Kind != "external_state" {
			return errors.New("file/test/manual criteria require outputs")
		}
	}
	if spec.Sink != nil {
		s := spec.Sink
		if s.Kind != "local_directory" && s.Kind != "git_local" || s.TargetRef == "" || spec.Outputs == nil {
			return errors.New("sink requires a supported kind, logical target_ref and outputs")
		}
		if err := workspace.ValidateTaskName(s.DeliveryKey); err != nil {
			return err
		}
		if s.Conflict != "" && s.Conflict != "reject" && s.Conflict != "replace" {
			return errors.New("unsupported conflict policy")
		}
	}
	return nil
}

func (p PipelinePlan) Validate() error {
	spec := p.Spec
	if err := spec.ValidateInput(); err != nil {
		return err
	}
	if spec.SchemaVersion != TaskInputSchema {
		return errors.New("unsupported general task schema")
	}
	if err := workspace.ValidateTaskName(spec.TaskID); err != nil {
		return err
	}
	if strings.TrimSpace(spec.Prompt) == "" || !utf8.ValidString(spec.Prompt) {
		return errors.New("a nonempty UTF-8 task prompt is required")
	}
	if spec.Agent.Provider != "claude" && spec.Agent.Provider != "codex" {
		return errors.New("unsupported agent provider")
	}
	if spec.Limits.TimeoutSeconds < 0 || spec.Limits.TimeoutSeconds > 86400 {
		return errors.New("timeout_seconds must be between 0 and 86400")
	}
	if err := p.Policy.InputLimits.Validate(); err != nil {
		return err
	}
	if err := p.Policy.ArtifactLimits.Validate(); err != nil {
		return err
	}
	if len(p.Policy.Exclude) > 0 {
		if err := artifact.ValidateSelection(p.Policy.Exclude); err != nil {
			return err
		}
	}
	if spec.Source != nil {
		g, ok := p.Policy.Sources[spec.Source.Ref]
		if !ok || g.Kind != spec.Source.Kind || !filepath.IsAbs(g.Path) {
			return errors.New("source reference is not authorized")
		}
		if err := artifact.ValidateSelection(g.Include); err != nil {
			return err
		}
	}
	if spec.Outputs != nil {
		if err := artifact.ValidateSelection(spec.Outputs.Include); err != nil && !(spec.Outputs.Patch && len(spec.Outputs.Include) == 0) {
			return err
		}
	}
	if _, err := completion.Binding(spec.Criteria, p.Policy.Criteria); err != nil {
		return err
	}
	for _, tool := range spec.Tools {
		g, ok := p.Policy.Tools[tool.GrantRef]
		if !ok {
			return external.ErrDenied
		}
		if err := tool.Authorize(g, tool.Targets[0]); err != nil {
			return err
		}
	}
	for _, c := range spec.Criteria {
		for _, id := range p.Policy.Criteria[c.Ref].RequiredTools {
			found := false
			for _, tool := range spec.Tools {
				if tool.ID == id {
					found = true
				}
			}
			if !found {
				return errors.New("required tool evidence must select a declared tool")
			}
		}
	}
	for _, call := range spec.Operations {
		g, ok := p.Policy.Tools[call.GrantRef]
		if !ok {
			return external.ErrDenied
		}
		// Admission validates connector configuration and scope; permission is checked
		// per call, allowing lookup-only recovery after writes are revoked.
		if err := g.Authorize(call, "lookup"); err != nil {
			return err
		}
	}
	for _, c := range spec.Criteria {
		if c.Kind == "external_state" {
			found := false
			for _, call := range spec.Operations {
				if call.ID == p.Policy.Criteria[c.Ref].Operation {
					found = true
				}
			}
			if !found {
				return errors.New("external criterion must select a declared operation")
			}
		}
	}
	if spec.Sink != nil {
		s := spec.Sink
		g, ok := p.Policy.Sinks[s.TargetRef]
		if !ok || g.Kind != s.Kind || !filepath.IsAbs(g.Path) {
			return errors.New("sink reference is not authorized")
		}
		if s.Kind != "local_directory" && s.Kind != "git_local" {
			return errors.New("unsupported sink kind")
		}
		if spec.Outputs == nil {
			return errors.New("file delivery requires outputs")
		}
		if err := workspace.ValidateTaskName(s.DeliveryKey); err != nil {
			return err
		}
		if s.Conflict != "" && s.Conflict != "reject" && s.Conflict != "replace" {
			return errors.New("unsupported conflict policy")
		}
		if s.Conflict == "replace" && !g.AllowReplace {
			return errors.New("replacement is not authorized")
		}
		if spec.Criteria == nil || len(spec.Criteria) == 0 && !p.Policy.AllowUnverified {
			return errors.New("delivery requires criteria or an explicitly authorized verification opt-out")
		}
	}
	return nil
}
