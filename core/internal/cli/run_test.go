package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"sandx/internal/config"
	"sandx/internal/task"
)

func TestRunEnvelopeRejectsConflictingFlagsAsProtocolError(t *testing.T) {
	var out bytes.Buffer
	a := &App{Out: &out, Err: &bytes.Buffer{}}
	cmd := a.runCmd()
	cmd.SetIn(strings.NewReader(`{"schema_version":"sbx.execution/v2","task_id":"ci-task","prompt":"go","agent":{"provider":"codex"}}`))
	cmd.SetArgs([]string{"--envelope", "-", "--detach"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected conflicting flags to fail")
	}
	var failure protocolFailure
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &failure); err != nil {
		t.Fatalf("expected protocol JSON failure: %v; output=%s", err, out.String())
	}
	if failure.ErrorCode != "invalid_task" {
		t.Fatalf("unexpected failure code: %+v", failure)
	}
}

func TestRunEnvelopeRejectsPositionalTaskAsProtocolError(t *testing.T) {
	var out bytes.Buffer
	a := &App{Out: &out, Err: &bytes.Buffer{}}
	cmd := a.runCmd()
	cmd.SetIn(strings.NewReader(`{"schema_version":"sbx.execution/v2","task_id":"ci-task","prompt":"go","agent":{"provider":"codex"}}`))
	cmd.SetArgs([]string{"--envelope", "-", "unexpected"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected positional task to fail")
	}
	var failure protocolFailure
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &failure); err != nil || failure.ErrorCode != "invalid_task" {
		t.Fatalf("expected machine-readable invalid envelope: %v: %s", err, out.String())
	}
}

func TestRunJSONLRequiresHeadlessPrompt(t *testing.T) {
	a := &App{}
	err := a.run(context.Background(), task.Task{}, runOpts{JSONL: true})
	if err == nil || !strings.Contains(err.Error(), "--jsonl requires --prompt") {
		t.Fatalf("expected JSONL mode validation before infrastructure setup, got %v", err)
	}
}

func TestRunCommandAdvertisesJSONLFlag(t *testing.T) {
	cmd := (&App{}).runCmd()
	flag := cmd.Flags().Lookup("jsonl")
	if flag == nil || !strings.Contains(flag.Usage, "machine-readable") {
		t.Fatalf("run command should expose the machine output option: %+v", flag)
	}
}

func TestRunAgentOverrideSurvivesPrepMerge(t *testing.T) {
	cfg := config.Default()
	a := &App{Cfg: cfg, Loaded: config.Loaded{Config: cfg, Sources: map[string]config.Source{}}}
	a.setRunAgent("codex")
	if err := a.Loaded.MergePrep([]string{"example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	a.Cfg = a.Loaded.Config
	if a.Cfg.DefaultAgent != "codex" {
		t.Fatal("prep merge discarded --agent")
	}
	if cfg.DefaultAgent != "claude" {
		t.Fatal("override mutated the original config")
	}
}

func TestRunRejectsUnknownAgentBeforeLoadingWorkspace(t *testing.T) {
	for _, name := range []string{"unknown", ""} {
		cmd := (&App{}).runCmd()
		cmd.SetArgs([]string{"--agent", name})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--agent must be") {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

func TestRunEnvelopeRejectsAgentOverride(t *testing.T) {
	var out bytes.Buffer
	cmd := (&App{Out: &out}).runCmd()
	cmd.SetIn(strings.NewReader(`{"schema_version":"sbx.execution/v2","task_id":"test","prompt":"go","agent":{"provider":"codex"}}`))
	cmd.SetArgs([]string{"--envelope", "-", "--agent", "codex"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("accepted envelope provider override")
	}
	var failure protocolFailure
	if err := json.Unmarshal(out.Bytes(), &failure); err != nil || failure.ErrorCode != "invalid_task" {
		t.Fatalf("%s %v", out.String(), err)
	}
}

func TestEveryTaskEntrypointRejectsV1BeforeLoading(t *testing.T) {
	for _, args := range [][]string{{"run", "--spec", "-"}, {"run", "--envelope", "-"}, {"task", "run", "--spec", "-"}, {"admit", "--spec", "-"}} {
		var out bytes.Buffer
		a := &App{Out: &out, Err: &bytes.Buffer{}}
		var cmd *cobra.Command
		switch args[0] {
		case "run":
			cmd = a.runCmd()
		case "task":
			cmd = a.taskCmd()
		case "admit":
			cmd = a.admitCmd()
		}
		cmd.SetIn(strings.NewReader(`{"schema_version":"sbx.execution/v1","task_id":"old","prompt":"x"}`))
		if args[0] == "task" {
			a.In_ = cmd.InOrStdin()
		}
		cmd.SetArgs(args[1:])
		err := cmd.Execute()
		if err == nil || exitCode(err) != 2 {
			t.Fatalf("%v: %v", args, err)
		}
		var record protocolFailure
		if json.Unmarshal(out.Bytes(), &record) != nil || record.SchemaVersion != "sbx.execution/v2" || record.ErrorCode != "invalid_task" {
			t.Fatalf("%v: %s", args, out.String())
		}
	}
}
