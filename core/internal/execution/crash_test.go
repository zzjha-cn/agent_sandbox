package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The child is a real process. Only the runtime is a controlled replacement;
// locks, records, process death and recovery use their production implementations.
type crashDriver struct {
	testDriver
	home, at string
}

func (d *crashDriver) pause() {
	if err := os.WriteFile(filepath.Join(d.home, "ready"), []byte("ready"), 0600); err != nil {
		panic(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}
func (d *crashDriver) Create(context.Context, Request) error {
	f, err := os.OpenFile(filepath.Join(d.home, "effects"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString("create\n")
	_ = f.Close()
	if err != nil {
		return err
	}
	if d.at == "prepare" {
		d.pause()
	}
	return nil
}
func (d *crashDriver) Wait(context.Context, Request) (AgentOutcome, error) {
	d.pause()
	return AgentOutcome{}, nil
}

func TestExecutionCrashHelper(t *testing.T) {
	mode := os.Getenv("SBX_G1_CHILD")
	if mode == "" {
		return
	}
	req := testRequest("never-replay-secret")
	req.Task.Home = os.Getenv("SBX_G1_HOME")
	store, err := NewFileStore(req.Task)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "lock" {
		unlock, err := store.Lock()
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		(&crashDriver{home: req.Task.Home}).pause()
	}
	_, err = NewService(&crashDriver{home: req.Task.Home, at: mode}).WithStore(store).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
}

func startCrashChild(t *testing.T, mode, home string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecutionCrashHelper$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "SBX_G1_CHILD="+mode, "SBX_G1_HOME="+home)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(home, "ready")); err == nil {
			return cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child never reached durable crash boundary")
	return nil
}

func TestProcessDeathPreservesIntentAndDoesNotReplay(t *testing.T) {
	for _, phase := range []string{"prepare", "execute"} {
		t.Run(phase, func(t *testing.T) {
			req, store := durableRequest(t)
			child := startCrashChild(t, phase, req.Task.Home)
			before, err := store.Load()
			if err != nil || stageState(before.Attempts[0].Result, Phase(phase)) != ProgressRunning {
				t.Fatalf("%+v %v", before, err)
			}
			_ = child.Process.Kill()
			_ = child.Wait()
			d := &testDriver{exists: true, outcome: AgentOutcome{ExitCode: new(int), ContainerState: ContainerStopped}}
			r, err := NewService(d).WithStore(store).Run(context.Background(), req)
			if phase == "prepare" {
				if !errors.Is(err, ErrRecoveryRequired) || r.State != Rejected || len(d.steps) != 0 {
					t.Fatalf("%+v %v %v", r, err, d.steps)
				}
			} else {
				if err != nil || strings.Join(d.steps, ",") != "inspect,admit,resume,wait" {
					t.Fatalf("%v %v", err, d.steps)
				}
				after, _ := store.Load()
				if len(after.Attempts) != 2 || after.Attempts[0].Closed || after.Attempts[0].Result.AttemptID == r.AttemptID {
					t.Fatalf("%+v", after)
				}
			}
			b, _ := os.ReadFile(filepath.Join(req.Task.Home, "effects"))
			if string(b) != "create\n" {
				t.Fatalf("duplicated side effect: %q", b)
			}
		})
	}
}

func TestRecordLockIsCrossProcessAndReleasedOnDeath(t *testing.T) {
	req, store := durableRequest(t)
	child := startCrashChild(t, "lock", req.Task.Home)
	if unlock, err := store.Lock(); !errors.Is(err, ErrRecordBusy) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("lock not exclusive: %v", err)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
