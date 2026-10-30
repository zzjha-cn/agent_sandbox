package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/execution"
)

func TestGeneralTaskDecodeBoundsAndVersionedEvents(t *testing.T) {
	for _, input := range []string{`{"schema_version":"sbx.execution/v2","unknown":1}`, `{} {}`, strings.Repeat(" ", generalInputLimit+1), string([]byte{0xff})} {
		if _, err := decodeTask(strings.NewReader(input)); err == nil {
			t.Fatal("invalid task accepted")
		}
	}
	spec, err := decodeTask(strings.NewReader(`{"schema_version":"sbx.execution/v2","task_id":"report","prompt":"task","agent":{"provider":"codex"},"criteria":[]}`))
	if err != nil || spec.Criteria == nil {
		t.Fatalf("explicit opt-out lost: %+v %v", spec, err)
	}
	var out bytes.Buffer
	sink := execution.NewJSONLSink(&out)
	if err := sink.Emit(context.Background(), execution.Event{Type: execution.EventCompleted}); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(out.Bytes(), &event); err != nil || event["schema_version"] != execution.TaskInputSchema {
		t.Fatalf("unversioned event: %s %v", out.Bytes(), err)
	}
}

func TestExecutionPolicyTrustedOnlyAndStrict(t *testing.T) {
	home := t.TempDir()
	name := filepath.Join(home, "execution-policy.toml")
	if err := os.WriteFile(name, []byte("allow_unverified = true\n[sources.input]\nkind = \"files\"\npath = \"/registered/input\"\ninclude = [\".\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := readTaskPolicy(home)
	if err != nil || !p.AllowUnverified || p.InputLimits.Bytes != 64<<20 || p.Sources["input"].Path != "/registered/input" {
		t.Fatalf("%+v %v", p, err)
	}
	if err := os.Chmod(name, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := readTaskPolicy(home); err == nil {
		t.Fatal("accepted writable policy")
	}
	if err := os.Chmod(name, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("unknown = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTaskPolicy(home); err == nil {
		t.Fatal("ignored policy typo")
	}
}
