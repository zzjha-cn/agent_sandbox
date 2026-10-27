package execution

import (
	"context"
	"fmt"
	"time"

	"sandx/internal/docker"
	"sandx/internal/task"
)

// ContainerObserver supplies observations, never an inferred agent exit code.
type ContainerObserver interface {
	Inspect(string) (docker.State, bool, error)
}

// WaitAgent requires the agent's own completion marker. A stopped or removed
// container without that marker is an infrastructure failure, including OOM.
func WaitAgent(ctx context.Context, observer ContainerObserver, t task.Task, interval time.Duration) (AgentOutcome, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return AgentOutcome{}, err
		}
		st, exists, err := observer.Inspect(t.Container())
		if err != nil {
			return AgentOutcome{}, err
		}
		state := ContainerStopped
		if exists && st.Running {
			state = ContainerRunning
		}
		// Read after inspect: completion may have been written while inspect ran.
		if code := t.RunExit(); code != nil {
			return AgentOutcome{ExitCode: code, ContainerState: state}, nil
		}
		if !exists || !st.Running {
			return AgentOutcome{ContainerState: state}, fmt.Errorf("agent completion marker missing (container exists=%t, status=%s, oom=%t)", exists, st.Status, st.OOMKilled)
		}
		select {
		case <-ctx.Done():
			return AgentOutcome{ContainerState: state}, ctx.Err()
		case <-ticker.C:
		}
	}
}
