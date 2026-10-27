package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sandx/internal/protocol"
)

// This black-box caller test builds and invokes the real CLI binary. A tiny
// Docker shim makes the read-only admission path deterministic; it does not
// pretend to exercise container creation or an agent runtime.
func TestProtocolClientCLIEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Docker shim uses a POSIX shell")
	}
	root, err := os.MkdirTemp("", "sbx-protocol-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	repo, home, binDir := filepath.Join(root, "repo"), filepath.Join(root, "home"), filepath.Join(root, "bin")
	for _, dir := range []string{repo, home, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "init", repo)
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	dockerShim := "#!/bin/sh\n" + "if [ \"$1\" = container ] && [ \"$2\" = inspect ]; then echo 'No such object' >&2; exit 1; fi\n" + "if [ \"$1\" = ps ]; then exit 0; fi\n" + "if [ \"$1\" = info ] && [ \"$3\" = '{{.ID}}' ]; then echo test-daemon; exit 0; fi\n" + "if [ \"$1\" = info ]; then echo '{\"MemTotal\":17179869184}'; exit 0; fi\n" + "if [ \"$1\" = volume ] && [ \"$2\" = inspect ]; then echo 'No such volume' >&2; exit 1; fi\n" + "echo unsupported docker shim invocation >&2; exit 2\n"
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(dockerShim), 0o755); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	cliBin := filepath.Join(root, "sbx")
	build := exec.Command("go", "build", "-o", cliBin, "./cmd/sbx")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, out)
	}
	pathEnv := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	run := func(args []string, stdin string) ([]byte, []byte, error) {
		cmd := exec.Command(cliBin, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "PATH="+pathEnv, "SBX_HOME="+home)
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), err
	}
	// The scheduler discovers the schema before submitting a task.
	versionOut, _, err := run([]string{"version", "--json"}, "")
	if err != nil {
		t.Fatalf("version command: %v", err)
	}
	var caps protocol.VersionInfo
	if err := json.Unmarshal(bytes.TrimSpace(versionOut), &caps); err != nil {
		t.Fatalf("invalid version JSON: %v: %s", err, versionOut)
	}
	if caps.SchemaVersion != protocol.SchemaVersion {
		t.Fatalf("unexpected protocol version: %+v", caps)
	}
	// Then it submits a v1 envelope to the real read-only admission command.
	envelope, _ := json.Marshal(protocol.Envelope{SchemaVersion: protocol.SchemaVersion, TaskID: "client-job", Prompt: "run checks"})
	out, stderr, err := run([]string{"admit", "--envelope", "-"}, string(envelope))
	if err != nil {
		t.Fatalf("admission failed: %v; stderr=%s; stdout=%s", err, stderr, out)
	}
	var admission admissionRecord
	if err := json.Unmarshal(bytes.TrimSpace(out), &admission); err != nil {
		t.Fatalf("invalid admission JSON: %v: %s", err, out)
	}
	if !admission.Admitted || admission.TaskID != "client-job" || admission.SchemaVersion != protocol.SchemaVersion {
		t.Fatalf("unexpected admission response: %+v", admission)
	}
	// Inputs accepted by the advertised schema must match the real decoder;
	// unknown spelling/null and filesystem failures stay machine-classified.
	for _, suffix := range []string{`,"Fresh":true}`, `,"fresh":null}`, `,"base":null}`, `,"timeout_seconds":null}`} {
		input := strings.TrimSuffix(string(envelope), "}") + suffix
		out, _, err := run([]string{"run", "--envelope", "-"}, input)
		var rejected *exec.ExitError
		if !errors.As(err, &rejected) || rejected.ExitCode() != 2 {
			t.Fatalf("schema-invalid input accepted: %s %v %s", input, err, out)
		}
	}
	for _, literal := range []string{"1.0", "1e2", "0.01e2"} {
		input := strings.TrimSuffix(string(envelope), "}") + `,"timeout_seconds":` + literal + "}"
		if out, stderr, err := run([]string{"admit", "--envelope", "-"}, input); err != nil {
			t.Fatalf("schema-valid integer rejected: %s %v %s %s", literal, err, stderr, out)
		}
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "locks"), []byte("not a lock directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockOutput, _, lockErr := run([]string{"run", "--envelope", "-"}, string(envelope))
	var lockExit *exec.ExitError
	if !errors.As(lockErr, &lockExit) || lockExit.ExitCode() != 4 {
		t.Fatalf("lock storage error must exit 4: %v %s", lockErr, lockOutput)
	}
	var lockRecord struct {
		Result struct {
			State string `json:"state"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(lockOutput), &lockRecord); err != nil || lockRecord.Result.State != "environment_failed" {
		t.Fatalf("wrong lock terminal record: %s %v", lockOutput, err)
	}
	if err := os.Remove(filepath.Join(home, "locks")); err != nil {
		t.Fatal(err)
	}
	// Unknown schema is rejected in a machine-readable form and with stable code 2.
	bad := strings.Replace(string(envelope), protocol.SchemaVersion, "sbx.execution/v99", 1)
	out, _, err = run([]string{"run", "--envelope", "-"}, bad)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("invalid envelope should exit 2, got %v", err)
	}
	var failure protocolFailure
	if err := json.Unmarshal(bytes.TrimSpace(out), &failure); err != nil {
		t.Fatalf("invalid failure record: %v: %s", err, out)
	}
	if failure.ErrorCode != "invalid_envelope" || failure.SchemaVersion != protocol.SchemaVersion {
		t.Fatalf("unexpected failure response: %+v", failure)
	}
	// The public CLI must reject unsupported deployment before provisioning,
	// while malformed envelopes retain their input-error classification.
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[isolation]\nboundary=\"shared-daemon\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"admit", "run"} {
		out, stderr, err := run([]string{command, "--envelope", "-"}, string(envelope))
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
			t.Fatalf("shared daemon %s: %v %s", command, err, stderr)
		}
		var record map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out), &record); err != nil {
			t.Fatal(err, string(out))
		}
		if record["error_code"] != "admission_rejected" {
			result, ok := record["result"].(map[string]any)
			if !ok || result["state"] != "admission_rejected" {
				t.Fatal(record)
			}
		}
	}
	_, _, err = run([]string{"run", "--envelope", "-"}, bad)
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatal("policy masked invalid envelope", err)
	}
	_, stderr, err = run([]string{"login", "codex", "--status"}, "")
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 || !bytes.Contains(stderr, []byte("shared-daemon")) {
		t.Fatalf("login bypassed policy: %v %s", err, stderr)
	}
}

// Enable explicitly with SBX_REAL_DOCKER_E2E=1. The test performs only
// container/volume inspect, docker ps, and docker info; it creates no Docker objects.
func TestProtocolAdmissionAgainstRealDocker(t *testing.T) {
	if os.Getenv("SBX_REAL_DOCKER_E2E") != "1" {
		t.Skip("set SBX_REAL_DOCKER_E2E=1 to use the local Docker daemon")
	}
	if runtime.GOOS == "windows" {
		t.Skip("unsupported git/Docker test environment")
	}
	root, err := os.MkdirTemp("", "sbx-real-docker-admit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	repo, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", repo)
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	_, file, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	cliBin := filepath.Join(root, "sbx")
	build := exec.Command("go", "build", "-o", cliBin, "./cmd/sbx")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, out)
	}
	envelope, _ := json.Marshal(protocol.Envelope{SchemaVersion: protocol.SchemaVersion, TaskID: "docker-readonly", Prompt: "not executed by admit"})
	cmd := exec.Command(cliBin, "admit", "--envelope", "-")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "SBX_HOME="+home)
	cmd.Stdin = bytes.NewReader(envelope)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("real Docker admission: %v; stderr=%s; stdout=%s", err, stderr.String(), stdout.String())
	}
	var admission admissionRecord
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &admission); err != nil {
		t.Fatalf("invalid JSON response: %v: %s", err, stdout.String())
	}
	if !admission.Admitted || admission.TaskID != "docker-readonly" || admission.Existing {
		t.Fatalf("unexpected Docker admission response: %+v", admission)
	}
}
