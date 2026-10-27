package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"sandx/internal/execution"
	"sandx/internal/protocol"
	"sandx/internal/task"
)

type protocolFailure struct {
	SchemaVersion string `json:"schema_version"`
	Type          string `json:"type"`
	ErrorCode     string `json:"error_code"`
}

type admissionRecord struct {
	SchemaVersion string `json:"schema_version"`
	Type          string `json:"type"`
	TaskID        string `json:"task_id"`
	Admitted      bool   `json:"admitted"`
	Existing      bool   `json:"existing"`
	Running       bool   `json:"running"`
}

func readEnvelope(cmd *cobra.Command, source string) (protocol.Envelope, error) {
	var r io.Reader
	if source == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err := os.Open(source)
		if err != nil {
			return protocol.Envelope{}, &protocol.CodedError{Code: "invalid_envelope", Err: err}
		}
		defer f.Close()
		r = f
	}
	return protocol.Decode(r)
}

func protocolErrorCode(err error) string {
	var coded *protocol.CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	var executionErr *execution.ExecutionError
	if errors.As(err, &executionErr) {
		switch executionErr.Kind {
		case execution.AdmissionRejected:
			return "admission_rejected"
		case execution.TimedOutKind:
			return "timed_out"
		case execution.CancelledKind:
			return "cancelled"
		case execution.AgentRunFailed:
			return "agent_failed"
		case execution.DeliveryFailedKind:
			return "delivery_failed"
		case execution.AgentStartFailed:
			return "agent_start_failed"
		case execution.InternalFailed:
			return "internal_failed"
		default:
			return "environment_failed"
		}
	}
	return "runner_error"
}

func (a *App) writeProtocolFailure(err error) error {
	if writeErr := a.writeProtocolRecord(protocolFailure{SchemaVersion: protocol.SchemaVersion, Type: "failed", ErrorCode: protocolErrorCode(err)}); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func (a *App) writeProtocolRecord(record any) error {
	if err := json.NewEncoder(a.Out).Encode(record); err != nil {
		return &execution.ExecutionError{Kind: execution.DeliveryFailedKind, Stage: "writing protocol response", Cause: err}
	}
	return nil
}

func (a *App) loadEnvelopeTask(env protocol.Envelope) (task.Task, error) {
	if err := a.load(); err != nil {
		return task.Task{}, &protocol.CodedError{Code: "runner_error", Err: err}
	}
	t, err := a.task(env.TaskID)
	if err != nil {
		return task.Task{}, &protocol.CodedError{Code: "runner_error", Err: err}
	}
	if err := a.requireTrust(); err != nil {
		return task.Task{}, &protocol.CodedError{Code: "runner_error", Err: err}
	}
	return t, nil
}

func (a *App) admitCmd() *cobra.Command {
	var source string
	cmd := &cobra.Command{Use: "admit --envelope <file|->", Short: "Validate and check admission for an execution envelope", Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				err := &protocol.CodedError{Code: "invalid_envelope", Err: fmt.Errorf("admit accepts no positional arguments")}
				return a.writeProtocolFailure(err)
			}
			if source == "" {
				err := &protocol.CodedError{Code: "invalid_envelope", Err: fmt.Errorf("--envelope is required")}
				return a.writeProtocolFailure(err)
			}
			env, err := readEnvelope(cmd, source)
			if err != nil {
				return a.writeProtocolFailure(err)
			}
			t, err := a.loadEnvelopeTask(env)
			if err != nil {
				return a.writeProtocolFailure(err)
			}
			driver := a.executionDriver()
			exists, running, err := driver.Inspect(cmd.Context(), execution.Request{Task: t, Mode: execution.Headless, Config: a.Cfg})
			if err != nil {
				err = &execution.ExecutionError{Kind: execution.EnvironmentFailed, Stage: "inspecting task", Cause: err}
			}
			if err == nil {
				err = driver.Admit(cmd.Context(), execution.Request{Task: t, Mode: execution.Headless, Config: a.Cfg}, exists, running)
				if err != nil {
					var classified *execution.ExecutionError
					if !errors.As(err, &classified) {
						err = &execution.ExecutionError{Kind: execution.AdmissionRejected, Stage: "checking admission", Cause: err}
					}
				}
			}
			if err != nil {
				return a.writeProtocolFailure(err)
			}
			return a.writeProtocolRecord(admissionRecord{SchemaVersion: protocol.SchemaVersion, Type: "admitted", TaskID: env.TaskID, Admitted: true, Existing: exists, Running: running})
		}}
	cmd.Flags().StringVar(&source, "envelope", "", "read the JSON envelope from a file or '-' for stdin")
	return cmd
}

func (a *App) versionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "version", Short: "Print application and protocol version information", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !asJSON {
				fmt.Fprintln(a.Out, Version)
				return nil
			}
			return a.writeProtocolRecord(protocol.Version(Version))
		}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable protocol capabilities")
	return cmd
}
