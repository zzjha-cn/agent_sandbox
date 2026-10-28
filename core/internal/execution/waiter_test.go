package execution

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"sandx/internal/docker"
)

type observation struct {
	state   docker.State
	exists  bool
	inspect func()
}

func (o observation) Inspect(string) (docker.State, bool, error) {
	if o.inspect != nil {
		o.inspect()
	}
	return o.state, o.exists, nil
}

func TestWaitAgentRequiresCompletionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		exists, running, oom bool
		marker               string
		wantErr              bool
	}{
		{"removed", false, false, false, "", true},
		{"stopped", true, false, false, "", true},
		{"oom", true, false, true, "", true},
		{"corrupt", true, false, false, "invalid", true},
		{"completed", true, false, false, "0", false},
		{"nonzero", true, false, false, "17", false},
		{"shutdown race", true, true, false, "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest("test")
			req.Task.Home = t.TempDir()
			if err := os.MkdirAll(req.Task.StateDir(), 0755); err != nil {
				t.Fatal(err)
			}
			observer := observation{exists: tc.exists, state: docker.State{Running: tc.running, OOMKilled: tc.oom}, inspect: func() {
				if tc.marker != "" {
					if err := os.WriteFile(req.Task.RunExitPath(), []byte(tc.marker), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}}
			out, err := WaitAgent(context.Background(), observer, req.Task, time.Millisecond)
			if (err != nil) != tc.wantErr {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if tc.wantErr && out.ExitCode != nil {
				t.Fatal("invented exit code")
			}
			if !tc.wantErr && out.ExitCode == nil {
				t.Fatal("missing exit code")
			}
		})
	}
}

type interruptedDriver struct {
	testDriver
	interrupt      context.CancelFunc
	stopErr        error
	stopContextErr error
}

func (d *interruptedDriver) Wait(ctx context.Context, _ Request) (AgentOutcome, error) {
	if d.interrupt != nil {
		d.interrupt()
	}
	<-ctx.Done()
	return AgentOutcome{}, ctx.Err()
}
func (d *interruptedDriver) Stop(ctx context.Context, _ Request) error {
	d.steps = append(d.steps, "stop")
	d.stopContextErr = ctx.Err()
	return d.stopErr
}

func TestCancellationStopsExecutionWithIndependentContext(t *testing.T) {
	for _, fail := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		d := &interruptedDriver{interrupt: cancel}
		if fail {
			d.stopErr = errors.New("stop failed")
		}
		result, err := NewService(d).Run(ctx, testRequest("test"))
		cancel()
		if !errors.Is(err, context.Canceled) || result.State != Cancelled {
			t.Fatalf("%+v %v", result, err)
		}
		if d.stopContextErr != nil || d.steps[len(d.steps)-1] != "stop" {
			t.Fatalf("cleanup skipped or cancelled: %+v", d)
		}
		want := CleanupComplete
		if fail {
			want = CleanupFailed
		}
		if result.CleanupState != want {
			t.Fatalf("%+v", result)
		}
		if fail && !errors.Is(err, d.stopErr) {
			t.Fatal("lost cleanup error")
		}
	}
}
