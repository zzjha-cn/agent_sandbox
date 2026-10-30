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

func readEnvelope(cmd *cobra.Command, source string) (execution.TaskSpec, error) {
	var r io.Reader
	if source == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err := os.Open(source)
		if err != nil {
			return execution.TaskSpec{}, &protocol.CodedError{Code: "invalid_task", Err: err}
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

func (a *App) admitCmd() *cobra.Command {
	var source, alias string
	cmd := &cobra.Command{Use: "admit --spec <file|->", Short: "Check v2 task policy and runtime admission without provisioning", Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			invalid := func(err error) error {
				return a.writeProtocolFailure(&protocol.CodedError{Code: "invalid_task", Err: err})
			}
			if len(args) > 0 || source != "" && alias != "" {
				return invalid(errors.New("admit requires one --spec and no positional arguments"))
			}
			if source == "" {
				source = alias
			}
			if source == "" {
				return invalid(errors.New("--spec is required"))
			}
			spec, err := readEnvelope(cmd, source)
			if err != nil {
				return a.writeProtocolFailure(err)
			}
			req, err := a.generalRequest(cmd.Context(), spec)
			if err != nil {
				return a.writeProtocolFailure(&execution.ExecutionError{Kind: execution.AdmissionRejected, Stage: "task_spec", Cause: err})
			}
			runApp := *a
			runApp.Out = a.Err
			runApp.Docker = a.Docker.WithContext(cmd.Context())
			admission, err := execution.NewService(runApp.executionDriver()).Check(cmd.Context(), req)
			if err != nil {
				return a.writeProtocolFailure(err)
			}
			return a.writeProtocolRecord(admissionRecord{SchemaVersion: protocol.SchemaVersion, Type: "admitted", TaskID: spec.TaskID, Admitted: true, Existing: admission.Existing, Running: admission.Running})
		}}
	cmd.Flags().StringVar(&source, "spec", "", "v2 task file or - for stdin")
	cmd.Flags().StringVar(&alias, "envelope", "", "alias for --spec; accepts v2 tasks only")
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
