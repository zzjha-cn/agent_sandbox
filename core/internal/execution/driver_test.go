package execution

import (
	"context"
	"errors"
	"testing"

	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/isolation"
	"sandx/internal/task"
)

func TestIsolationInfrastructureFailurePreservesEnvironmentClassification(t *testing.T) {
	req := testRequest("synthetic")
	d := &RuntimeDriver{Isolation: isolation.New(&docker.Client{Bin: "/missing-docker"}, isolation.Policy{})}
	err := d.Admit(context.Background(), req, false, false)
	var classified *ExecutionError
	if !errors.As(err, &classified) || classified.Kind != EnvironmentFailed {
		t.Fatal(err)
	}
	request := testRequest("synthetic")
	driver := &admissionFailureDriver{testDriver: testDriver{}, admissionErr: err}
	result, err := NewService(driver).Run(context.Background(), request)
	if !errors.As(err, &classified) || classified.Kind != EnvironmentFailed || result.State != EnvironmentErr {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestTaskScopeRejectsForeignOrRestoredMetadataWithoutModification(t *testing.T) {
	for _, meta := range []task.Meta{{}, {Namespace: "ns-a", Owner: "a"}, {Namespace: "ns-b", Owner: "a"}, {Namespace: "ns-a", Owner: "b"}} {
		req := testRequest("test")
		req.Task.Home = t.TempDir()
		if err := req.Task.WriteMeta(meta); err != nil {
			t.Fatal(err)
		}
		err := checkTaskScope(req.Task, isolation.Status{Namespace: "ns-a", Owner: "a"})
		reject := (meta.Namespace != "" && meta.Namespace != "ns-a") || (meta.Owner != "" && meta.Owner != "a")
		if (err != nil) != reject {
			t.Fatalf("%+v %v", meta, err)
		}
		after, _, err := req.Task.ReadMeta()
		if err != nil || after.Namespace != meta.Namespace || after.Owner != meta.Owner {
			t.Fatal("admission modified task metadata")
		}
	}
}

type admissionFailureDriver struct {
	testDriver
	admissionErr error
}

func (d *admissionFailureDriver) Admit(context.Context, Request, bool, bool) error {
	return d.admissionErr
}

func TestAdmissionRejectsProviderChangeBeforePreparingResources(t *testing.T) {
	for _, tc := range []struct {
		stored, requested string
		reject            bool
	}{
		{"", "claude", false}, {"", "codex", true}, {"codex", "claude", true}, {"codex", "codex", false}, {"claude", "unknown", true},
	} {
		t.Run(tc.stored+"->"+tc.requested, func(t *testing.T) {
			req := testRequest("test")
			req.Task.Home = t.TempDir()
			req.Config = config.Default()
			req.Config.DefaultAgent = tc.requested
			if err := req.Task.WriteMeta(task.Meta{Agent: tc.stored}); err != nil {
				t.Fatal(err)
			}
			checks := 0
			d := &RuntimeDriver{CheckConcurrency: func(task.Task) error { checks++; return nil }, WarnMemoryBudget: func() {}}
			err := d.Admit(context.Background(), req, true, false)
			if (err != nil) != tc.reject {
				t.Fatalf("reject=%t err=%v", tc.reject, err)
			}
			if tc.reject && checks != 0 {
				t.Fatal("provider mismatch reached resource admission")
			}
		})
	}
}

func TestTaskLockRejectsConcurrentExecutionAndReleases(t *testing.T) {
	req := testRequest("test")
	req.Task.Home = t.TempDir()
	first := &RuntimeDriver{}
	unlock, err := first.Lock(req)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewService(&RuntimeDriver{}).Run(context.Background(), req)
	if err == nil || result.State != Rejected {
		t.Fatalf("concurrent run reached infrastructure: %+v %v", result, err)
	}
	unlock()
	release, err := (&RuntimeDriver{}).Lock(req)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	release()
}
