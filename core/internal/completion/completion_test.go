package completion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sandx/internal/artifact"
)

type testRunner struct {
	code  int
	err   error
	calls int
}

func (r *testRunner) Test(context.Context, string, Rule) (int, error) {
	r.calls++
	return r.code, r.err
}

func TestFileJudgmentsAndStableEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		rule          Rule
		state, reason string
	}{
		{"valid", "{\"ok\":true}", Rule{Required: []string{"result.json"}, Format: map[string]string{"result.json": "json"}}, "passed", ""},
		{"missing", "{}", Rule{Required: []string{"missing"}}, "failed", "required_file_missing"},
		{"format", "broken", Rule{Format: map[string]string{"result.json": "json"}}, "failed", "invalid_json"},
		{"structure", "{}", Rule{Contains: map[string][]string{"result.json": {"required"}}}, "failed", "required_structure_missing"},
		{"scope", "{}", Rule{AllowedChanges: []string{"other/**"}}, "failed", "change_out_of_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			m, err := artifact.Scan(context.Background(), dir, []string{"."}, nil, artifact.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			rule := tc.rule
			rule.Kind = "files"
			rule.Version = "1"
			criteria := []Criterion{{ID: "check", Kind: "files", Ref: "registered"}}
			rules := map[string]Rule{"registered": rule}
			r, err := Evaluate(context.Background(), dir, m, artifact.Manifest{}, criteria, rules, nil)
			if err != nil || r.State != tc.state || r.Evidence[0].Reason != tc.reason || r.ArtifactDigest != m.Digest {
				t.Fatalf("%+v %v", r, err)
			}
			bound := r.CriteriaDigest
			rule.Version = "2"
			rules["registered"] = rule
			newBinding, err := Binding(criteria, rules)
			if err != nil || newBinding == bound {
				t.Fatal("rule change did not invalidate evidence")
			}
		})
	}
}

func TestTestsFaultsAndManualAreDistinct(t *testing.T) {
	dir := t.TempDir()
	m, err := artifact.Scan(context.Background(), dir, []string{"."}, nil, artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	criteria := []Criterion{{"test", "tests", "unit"}, {"review", "manual", "human"}}
	rules := map[string]Rule{"unit": {Kind: "tests", Version: "1", Image: "trusted@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Command: []string{"test"}, TimeoutSeconds: 5}, "human": {Kind: "manual", Version: "1"}}
	for _, tc := range []struct {
		code int
		err  error
		want string
	}{{0, nil, "needs_review"}, {1, nil, "failed"}, {0, errors.New("runtime unavailable"), "error"}} {
		runner := &testRunner{code: tc.code, err: tc.err}
		r, err := Evaluate(context.Background(), dir, m, m, criteria, rules, runner)
		if err != nil || r.State != tc.want || runner.calls != 1 {
			t.Fatalf("%+v %v", r, err)
		}
	}
	r, err := Evaluate(context.Background(), dir, m, m, nil, nil, nil)
	if err != nil || r.State != "not_requested" {
		t.Fatalf("omitted: %+v %v", r, err)
	}
	r, err = Evaluate(context.Background(), dir, m, m, []Criterion{}, nil, nil)
	if err != nil || r.State != "passed" {
		t.Fatalf("explicit opt-out: %+v %v", r, err)
	}
	if _, err := Binding([]Criterion{{"x", "external_state", "x"}}, map[string]Rule{"x": {Kind: "external_state", Version: "1"}}); err == nil {
		t.Fatal("advertised future external verification")
	}
}

func TestUnchangedAndDeletion(t *testing.T) {
	m := artifact.Manifest{Files: []artifact.File{{Path: "old", SHA256: "one"}}}
	if changed := Changes(artifact.Manifest{}, m); len(changed) != 1 || changed[0] != "old" {
		t.Fatalf("deletion missing: %+v", changed)
	}
	state, reason, err := checkFiles(context.Background(), t.TempDir(), m, m, m, Rule{})
	if err != nil || state != "failed" || reason != "unchanged" {
		t.Fatalf("%s %s %v", state, reason, err)
	}
}

func TestRequiredFileMustBeInSelectedOutputs(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "report.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	limits := artifact.DefaultLimits()
	full, err := artifact.Scan(context.Background(), workspace, []string{"."}, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	outputs := t.TempDir()
	selected, err := artifact.Scan(context.Background(), outputs, []string{"."}, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	criteria := []Criterion{{ID: "required", Kind: "files", Ref: "report"}}
	rules := map[string]Rule{"report": {Kind: "files", Version: "1", Required: []string{"report.json"}}}
	r, err := EvaluateBundle(context.Background(), outputs, workspace, selected, full, artifact.Manifest{}, criteria, rules, nil)
	if err != nil || r.State != "failed" || r.Evidence[0].Reason != "required_file_missing" {
		t.Fatalf("workspace file was accepted as delivered output: %+v %v", r, err)
	}
}
