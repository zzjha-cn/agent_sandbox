package cli

import (
	"bytes"
	"errors"
	"testing"

	"sandx/internal/execution"
	"sandx/internal/protocol"
)

type rejectedProtocolWriter struct{ err error }

func (w rejectedProtocolWriter) Write([]byte) (int, error) { return 0, w.err }

func TestProtocolResponsesPreserveDeliveryFailures(t *testing.T) {
	broken := errors.New("response writer unavailable")
	a := &App{Out: rejectedProtocolWriter{broken}, Err: &bytes.Buffer{}}
	cmd := a.versionCmd()
	cmd.SetArgs([]string{"--json"})
	err := cmd.Execute()
	if !errors.Is(err, broken) || exitCode(err) != 6 {
		t.Fatalf("valid response delivery: %v", err)
	}
	invalid := &protocol.CodedError{Code: "invalid_envelope", Err: errors.New("invalid input")}
	err = a.writeProtocolFailure(invalid)
	if !errors.Is(err, invalid) || !errors.Is(err, broken) || exitCode(err) != 2 {
		t.Fatalf("primary failure and failed report: %v", err)
	}
}

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
		{&execution.ExecutionError{Kind: execution.InternalFailed, Cause: errors.New("internal")}, 7},
		{&execution.ExecutionError{Kind: execution.TimedOutKind, Cause: errors.New("timeout")}, 124},
		{&execution.ExecutionError{Kind: execution.CancelledKind, Cause: errors.New("cancel")}, 130},
	}
	for _, tc := range cases {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("exitCode(%v)=%d, want %d", tc.err, got, tc.want)
		}
	}
}
