// Package completion evaluates immutable artifacts independently of agent exit.
package completion

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"sandx/internal/artifact"
)

type Criterion struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Rule comes only from trusted configuration, never from model output.
type Rule struct {
	Kind           string              `toml:"kind" json:"kind"`
	Version        string              `toml:"version" json:"version"`
	AllowedChanges []string            `toml:"allowed_changes" json:"allowed_changes,omitempty"`
	Required       []string            `toml:"required" json:"required,omitempty"`
	AllowUnchanged bool                `toml:"allow_unchanged" json:"allow_unchanged"`
	Format         map[string]string   `toml:"format" json:"format,omitempty"`
	Contains       map[string][]string `toml:"contains" json:"contains,omitempty"`
	Image          string              `toml:"image" json:"image,omitempty"`
	Command        []string            `toml:"command" json:"command,omitempty"`
	TimeoutSeconds int                 `toml:"timeout_seconds" json:"timeout_seconds,omitempty"`
}

type Evidence struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	RuleDigest string `json:"rule_digest"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
}

type Result struct {
	State          string     `json:"state"`
	ArtifactDigest string     `json:"artifact_digest"`
	CriteriaDigest string     `json:"criteria_digest"`
	ObservedAt     time.Time  `json:"observed_at"`
	Evidence       []Evidence `json:"evidence"`
}

// TestRunner must use a disposable, restricted execution environment. A
// nonzero exit is a business failure; a runtime/transport error is a fault.
type TestRunner interface {
	Test(context.Context, string, Rule) (int, error)
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func Binding(criteria []Criterion, rules map[string]Rule) (string, error) {
	bound := []any{}
	seen := map[string]bool{}
	for _, c := range criteria {
		r, ok := rules[c.Ref]
		if !ok || c.ID == "" || seen[c.ID] || c.Kind != r.Kind || r.Version == "" {
			return "", errors.New("criterion is not registered with a matching kind and version")
		}
		seen[c.ID] = true
		switch c.Kind {
		case "files", "tests", "manual":
			if err := r.Validate(); err != nil {
				return "", err
			}
		default:
			return "", errors.New("unsupported criterion kind")
		}
		bound = append(bound, struct {
			Criterion Criterion
			Rule      Rule
		}{c, r})
	}
	return digest(bound), nil
}

var immutableImage = regexp.MustCompile(`^(sha256:[a-f0-9]{64}|[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64})$`)

func (r Rule) Validate() error {
	switch r.Kind {
	case "files":
		if len(r.AllowedChanges) > 0 {
			if err := artifact.ValidateSelection(r.AllowedChanges); err != nil {
				return err
			}
		}
		for _, name := range r.Required {
			if err := exactPath(name); err != nil {
				return err
			}
		}
		for name, format := range r.Format {
			if err := exactPath(name); err != nil {
				return err
			}
			if format != "json" && format != "csv" && format != "text" {
				return errors.New("unsupported file verification format")
			}
		}
		for name := range r.Contains {
			if err := exactPath(name); err != nil {
				return err
			}
		}
	case "tests":
		if !immutableImage.MatchString(r.Image) || len(r.Command) == 0 || r.TimeoutSeconds < 1 || r.TimeoutSeconds > 86400 {
			return errors.New("test verifier requires an immutable image digest, a command, and timeout_seconds between 1 and 86400")
		}
		for _, arg := range r.Command {
			if strings.ContainsRune(arg, 0) {
				return errors.New("invalid verifier argument")
			}
		}
	case "manual":
	default:
		return errors.New("unsupported verifier kind")
	}
	return nil
}

func Evaluate(ctx context.Context, dir string, current, baseline artifact.Manifest, criteria []Criterion, rules map[string]Rule, runner TestRunner) (Result, error) {
	return EvaluateBundle(ctx, dir, dir, current, current, baseline, criteria, rules, runner)
}

// EvaluateBundle checks required/structured files in the selected artifacts,
// change scope against the complete frozen workspace, and tests in a separate
// copy of that frozen workspace. Evidence binds both snapshots at the caller.
func EvaluateBundle(ctx context.Context, artifactDir, workspaceDir string, current, workspace, baseline artifact.Manifest, criteria []Criterion, rules map[string]Rule, runner TestRunner) (Result, error) {
	bound, err := Binding(criteria, rules)
	if err != nil {
		return Result{}, err
	}
	result := Result{State: "passed", ArtifactDigest: current.Digest, CriteriaDigest: bound, ObservedAt: time.Now().UTC(), Evidence: []Evidence{}}
	if criteria == nil {
		result.State = "not_requested"
		return result, nil
	}
	for _, c := range criteria {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		r := rules[c.Ref]
		e := Evidence{ID: c.ID, Kind: c.Kind, RuleDigest: digest(r), State: "passed"}
		switch c.Kind {
		case "manual":
			e.State = "needs_review"
			e.Reason = "trusted_review_required"
		case "files":
			state, reason, err := checkFiles(ctx, artifactDir, current, workspace, baseline, r)
			if err != nil {
				e.State = "error"
				e.Reason = "verifier_io"
			} else {
				e.State = state
				e.Reason = reason
			}
		case "tests":
			if runner == nil || r.Image == "" || len(r.Command) == 0 || r.TimeoutSeconds <= 0 || r.TimeoutSeconds > 86400 {
				e.State = "error"
				e.Reason = "verifier_configuration"
				break
			}
			testCtx, cancel := context.WithTimeout(ctx, time.Duration(r.TimeoutSeconds)*time.Second)
			code, err := runner.Test(testCtx, workspaceDir, r)
			cancel()
			if err != nil {
				e.State = "error"
				e.Reason = "verifier_runtime"
			} else {
				e.ExitCode = &code
				if code != 0 {
					e.State = "failed"
					e.Reason = "test_exit"
				}
			}
		}
		result.Evidence = append(result.Evidence, e)
		if e.State == "error" {
			result.State = "error"
		} else if e.State == "failed" && result.State != "error" {
			result.State = "failed"
		} else if e.State == "needs_review" && result.State == "passed" {
			result.State = "needs_review"
		}
	}
	return result, ctx.Err()
}

func Changes(current, baseline artifact.Manifest) []string {
	old := map[string]artifact.File{}
	for _, f := range baseline.Files {
		old[f.Path] = f
	}
	var changed []string
	for _, f := range current.Files {
		before, ok := old[f.Path]
		if !ok || before.SHA256 != f.SHA256 || before.Executable != f.Executable {
			changed = append(changed, f.Path)
		}
		delete(old, f.Path)
	}
	for name := range old {
		changed = append(changed, name)
	}
	return changed
}

func checkFiles(ctx context.Context, dir string, current, workspace, baseline artifact.Manifest, r Rule) (string, string, error) {
	changes := Changes(workspace, baseline)
	if len(changes) == 0 && !r.AllowUnchanged {
		return "failed", "unchanged", nil
	}
	if len(r.AllowedChanges) > 0 {
		if err := artifact.ValidateSelection(r.AllowedChanges); err != nil {
			return "", "", err
		}
		for _, name := range changes {
			if !artifact.Selected(r.AllowedChanges, name) {
				return "failed", "change_out_of_scope", nil
			}
		}
	}
	files := map[string]bool{}
	for _, f := range current.Files {
		files[f.Path] = true
	}
	for _, name := range r.Required {
		if err := exactPath(name); err != nil {
			return "", "", err
		}
		if !files[name] {
			return "failed", "required_file_missing", nil
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	read := func(name string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := exactPath(name); err != nil {
			return nil, err
		}
		if !files[name] {
			return nil, os.ErrNotExist
		}
		return root.ReadFile(name)
	}
	for name, format := range r.Format {
		b, err := read(name)
		if errors.Is(err, os.ErrNotExist) {
			return "failed", "format_file_missing", nil
		}
		if err != nil {
			return "", "", err
		}
		switch format {
		case "json":
			if !json.Valid(b) {
				return "failed", "invalid_json", nil
			}
		case "csv":
			reader := csv.NewReader(strings.NewReader(string(b)))
			if _, err := reader.ReadAll(); err != nil {
				return "failed", "invalid_csv", nil
			}
		case "text":
			if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
				return "failed", "invalid_text", nil
			}
		default:
			return "", "", fmt.Errorf("unknown file format %s", format)
		}
	}
	for name, sections := range r.Contains {
		b, err := read(name)
		if errors.Is(err, os.ErrNotExist) {
			return "failed", "structure_file_missing", nil
		}
		if err != nil {
			return "", "", err
		}
		for _, section := range sections {
			if !strings.Contains(string(b), section) {
				return "failed", "required_structure_missing", nil
			}
		}
	}
	return "passed", "", nil
}

func exactPath(name string) error {
	if err := artifact.ValidateSelection([]string{name}); err != nil {
		return err
	}
	if name == "." || strings.ContainsAny(name, "*?[") || filepath.ToSlash(name) != name || artifact.Reserved(name) {
		return errors.New("expected a non-sensitive exact file path")
	}
	return nil
}
