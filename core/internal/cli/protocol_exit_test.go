package cli

import (
	"errors"
	"testing"

	"sandx/internal/execution"
	"sandx/internal/protocol"
)

func TestProtocolExitCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&protocol.CodedError{Code: "invalid_envelope", Err: errors.New("bad")}, 2},
		{&protocol.CodedError{Code: "runner_error", Err: errors.New("runner")}, 4},
		{&execution.ExecutionError{Kind: execution.AdmissionRejected, Cause: errors.New("reject")}, 3},
		{&execution.ExecutionError{Kind: execution.AgentRunFailed, Cause: errors.New("agent")}, 5},
		{&execution.ExecutionError{Kind: execution.DeliveryFailedKind, Cause: errors.New("stream")}, 6},
		{&execution.ExecutionError{Kind: execution.TimedOutKind, Cause: errors.New("timeout")}, 124},
		{&execution.ExecutionError{Kind: execution.CancelledKind, Cause: errors.New("cancel")}, 130},
	}
	for _, tc := range cases {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("exitCode(%v)=%d, want %d", tc.err, got, tc.want)
		}
	}
}
