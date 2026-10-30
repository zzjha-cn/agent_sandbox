package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// Published files are contract artifacts. A wire-type change must regenerate
// them and pass the independent client validator, rather than silently drifting.
func TestPublishedSchemasMatchWireTypes(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "../../../docs/schemas")
	for _, name := range SchemaNames() {
		t.Run(name, func(t *testing.T) {
			schema, err := Schema(name)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			published, err := os.ReadFile(filepath.Join(root, name+".schema.json"))
			if err != nil {
				t.Fatal(err)
			}
			var a, b any
			if err = json.Unmarshal(actual, &a); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(published, &b); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatal("schema drift; run scripts/check-schemas.py --sbx <binary> --write and verify consumers")
			}
		})
	}
}

func TestRecoveryGuidanceNeverReplaysUnknownWrite(t *testing.T) {
	for _, schema := range []string{TaskStatusSchema, ExecutionStatusSchema} {
		for _, recovery := range []string{"reconcile_operations", "record_invalid", "reconcile_prepare", "reconcile_cleanup", "reconcile_retention", "inspect_runtime"} {
			s := Status{SchemaVersion: schema}
			s.SetRecovery(recovery)
			if len(s.NextActions) != 1 {
				t.Fatal(s)
			}
			a := s.NextActions[0]
			if strings.Contains(a.Command, "task apply") || strings.Contains(a.Command, "task run") || strings.Contains(a.Command, "--fresh") {
				t.Fatalf("unsafe automatic retry guidance: %+v", s)
			}
			if recovery == "reconcile_operations" && schema == TaskStatusSchema && a.Action != "reconcile" {
				t.Fatal(s)
			}
			if schema == ExecutionStatusSchema && a.RequiresSpec {
				t.Fatal("inferred a spec from Git status")
			}
		}
	}
	for recovery, command := range map[string]string{"continue_verification": "approve", "continue_completion": "verify", "retry_delivery": "deliver", "new_attempt_allowed": "run"} {
		s := Status{SchemaVersion: TaskStatusSchema}
		s.SetRecovery(recovery)
		if !strings.Contains(s.NextActions[0].Command, "task "+command+" ") || !s.NextActions[0].RequiresSpec {
			t.Fatal(s)
		}
	}
}
