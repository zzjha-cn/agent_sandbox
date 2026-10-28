package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"sandx/internal/execution"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

func TestExecutionStatusRealCLIIsReadOnlyAndIndependentOfDockerConfig(t *testing.T) {
	root := t.TempDir()
	repo, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	// Both configs are broken and project trust has not been granted.
	if err := os.MkdirAll(filepath.Join(repo, ".sbx"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sbx", "sandbox.toml"), []byte("[broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[also broken"), 0600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "docker-called")
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\nprintf called > \"$SBX_G1_DOCKER_MARKER\"\nexit 73\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	module := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	binary := filepath.Join(root, "sbx")
	build := exec.Command("go", "build", "-o", binary, "./cmd/sbx")
	build.Dir = module
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "SBX_HOME="+home, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "SBX_G1_DOCKER_MARKER="+marker)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if bytes.Contains(stdout.Bytes(), []byte("secret")) {
			t.Fatal("leaked secret")
		}
		return stdout.Bytes(), err
	}
	out, err := run("execution", "status", "job", "--json")
	if err != nil {
		t.Fatalf("%s %v", out, err)
	}
	var status executionStatus
	if err := json.Unmarshal(out, &status); err != nil || status.Recorded || status.Recovery != "not_recorded" || status.SchemaVersion != executionStatusSchema {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(home, "executions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("query mutated home", err)
	}
	ws, err := workspace.Resolve(repo)
	if err != nil {
		t.Fatal(err)
	}
	taskValue, err := task.New(home, ws, "job")
	if err != nil {
		t.Fatal(err)
	}
	store, err := execution.NewFileStore(taskValue)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stages := []execution.StageProgress{
		{Phase: execution.AdmissionPhase, State: execution.ProgressSucceeded},
		{Phase: execution.PreparePhase, State: execution.ProgressRunning},
		{Phase: execution.ExecutePhase, State: execution.ProgressPending},
		{Phase: execution.CollectPhase, State: execution.ProgressSkipped},
		{Phase: execution.EvaluatePhase, State: execution.ProgressSkipped},
		{Phase: execution.DeliverPhase, State: execution.ProgressSkipped},
		{Phase: execution.FinalizePhase, State: execution.ProgressPending},
	}
	record := &execution.TaskRecord{SchemaVersion: execution.RecordSchema, TaskKey: taskValue.ID(), Attempts: []execution.AttemptRecord{{Result: execution.Result{TaskID: "job", AttemptID: "attempt-1", Stages: stages}, StartedAt: now, UpdatedAt: now}}}
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	err = store.Save(context.Background(), record)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "executions", ws.ID, "job", "record.json")
	before, _ := os.ReadFile(path)
	out, err = run("execution", "status", "job", "--json")
	if err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if err := json.Unmarshal(out, &status); err != nil || !status.Recorded || status.Recovery != "reconcile_prepare" || status.Record.Attempts[0].Result.AttemptID != "attempt-1" {
		t.Fatalf("%s %v", out, err)
	}
	text, err := run("execution", "status", "job")
	if err != nil || !bytes.Contains(text, []byte("prepare: running")) {
		t.Fatalf("%s %v", text, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("query changed record")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status contacted docker")
	}
	if err := os.WriteFile(path, []byte("secret-broken-record"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = run("execution", "status", "job", "--json")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("%s %v", out, err)
	}
	if err := json.Unmarshal(out, &status); err != nil || status.Recovery != "record_invalid" {
		t.Fatalf("%s %v", out, err)
	}
}
