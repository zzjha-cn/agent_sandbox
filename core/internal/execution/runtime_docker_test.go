package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"sandx/internal/docker"
)

type liveDriver struct {
	testDriver
	runtime   *RuntimeDriver
	interrupt context.CancelFunc
}

func (d *liveDriver) Wait(ctx context.Context, req Request) (AgentOutcome, error) {
	if d.interrupt != nil {
		d.interrupt()
	}
	return d.runtime.Wait(ctx, req)
}
func (d *liveDriver) Stop(ctx context.Context, req Request) error { return d.runtime.Stop(ctx, req) }

// This integration test needs only a cached busybox image; it never reads
// authentication, calls a model, or touches existing task containers/volumes.
func TestRealDockerCancellationTerminatesTask(t *testing.T) {
	if os.Getenv("SBX_REAL_DOCKER_E2E") != "1" {
		t.Skip("set SBX_REAL_DOCKER_E2E=1")
	}
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			req := testRequest("controlled process")
			req.Task.Home = t.TempDir()
			req.Task.Name = fmt.Sprintf("control-%d", time.Now().UnixNano())
			client := docker.New(false)
			if _, err := client.Run("run", "-d", "--pull=never", "--network=none", "--name", req.Task.Container(), "busybox:latest", "sh", "-c", "sleep 3600 & wait"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := client.Rm(req.Task.Container()); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := &liveDriver{runtime: &RuntimeDriver{Docker: client, PollInterval: 10 * time.Millisecond}, interrupt: cancel}
			want := Cancelled
			cause := context.Canceled
			if deadline {
				var end context.CancelFunc
				ctx, end = context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer end()
				d.interrupt = nil
				want = TimedOut
				cause = context.DeadlineExceeded
			}
			store, err := NewFileStore(req.Task)
			if err != nil {
				t.Fatal(err)
			}
			result, err := NewService(d).WithStore(store).Run(ctx, req)
			if !errors.Is(err, cause) || result.State != want || result.CleanupState != CleanupComplete {
				t.Fatalf("%+v %v", result, err)
			}
			state, exists, err := client.Inspect(req.Task.Container())
			if err != nil || !exists || state.Running {
				t.Fatalf("agent/tools still running: %+v exists=%t err=%v", state, exists, err)
			}
			if _, err := os.Stat(req.Task.Worktree()); !os.IsNotExist(err) {
				t.Fatalf("test unexpectedly created a worktree: %v", err)
			}
			record, err := store.Load()
			if err != nil || len(record.Attempts) != 1 || !record.Attempts[0].Closed || record.Attempts[0].Result.AttemptID != result.AttemptID || record.Attempts[0].Result.CleanupState != CleanupComplete {
				t.Fatalf("container outcome was not durably recorded: %+v %v", record, err)
			}
		})
	}
}
