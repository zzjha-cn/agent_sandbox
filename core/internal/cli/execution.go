package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"sandx/internal/execution"
)

const executionStatusSchema = "sbx.execution-status/v1"

type executionStatus struct {
	SchemaVersion string                `json:"schema_version"`
	Recorded      bool                  `json:"recorded"`
	Recovery      string                `json:"recovery"`
	Record        *execution.TaskRecord `json:"record,omitempty"`
}

func (a *App) executionCmd() *cobra.Command {
	var asJSON bool
	root := &cobra.Command{Use: "execution", Short: "Inspect durable execution progress"}
	status := &cobra.Command{Use: "status <task>", Short: "Read recorded attempts without contacting Docker", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.locate(); err != nil {
				return err
			}
			t, err := a.task(args[0])
			if err != nil {
				return err
			}
			store, err := execution.NewFileStore(t)
			if err != nil {
				return err
			}
			record, err := store.Load()
			if err != nil {
				if asJSON {
					if writeErr := json.NewEncoder(a.Out).Encode(executionStatus{SchemaVersion: executionStatusSchema, Recovery: "record_invalid"}); writeErr != nil {
						return writeErr
					}
				}
				return &execution.ExecutionError{Kind: execution.InternalFailed, Stage: "execution_record", Cause: fmt.Errorf("cannot verify execution record; inspect and reconcile it before running")}
			}
			out := executionStatus{SchemaVersion: executionStatusSchema, Recovery: "not_recorded", Recorded: record != nil, Record: record}
			if record != nil {
				out.Recovery = record.Recovery()
			}
			if asJSON {
				return json.NewEncoder(a.Out).Encode(out)
			}
			fmt.Fprintf(a.Out, "execution %s: %s\n", args[0], out.Recovery)
			if record != nil {
				for _, attempt := range record.Attempts {
					fmt.Fprintf(a.Out, "  attempt %s: %s (closed=%t)\n", attempt.Result.AttemptID, attempt.Result.ExecutionState, attempt.Closed)
					for _, stage := range attempt.Result.Stages {
						fmt.Fprintf(a.Out, "    %s: %s\n", stage.Phase, stage.State)
					}
				}
			}
			return nil
		}}
	status.Flags().BoolVar(&asJSON, "json", false, "print versioned execution status JSON")
	root.AddCommand(status)
	return root
}
