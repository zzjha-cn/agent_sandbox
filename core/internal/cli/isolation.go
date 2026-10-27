package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"sandx/internal/execution"
	"sandx/internal/isolation"
)

func isolationError(err error) error {
	if isolation.IsRejection(err) {
		return &execution.ExecutionError{Kind: execution.AdmissionRejected, Stage: "daemon isolation", Cause: err}
	}
	return err
}

func (a *App) checkIsolation(ctx context.Context, ensure bool) error {
	g := isolation.New(a.Docker, a.Cfg.Isolation)
	var s isolation.Status
	var err error
	if ensure {
		s, err = g.Ensure(ctx)
	} else {
		s, err = g.Status(ctx)
	}
	if err != nil {
		return isolationError(err)
	}
	a.Docker.ResourceLabels = s.Labels()
	return nil
}

func (a *App) isolationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "isolation", Short: "Inspect or adopt this user's dedicated Docker daemon"}
	var asJSON bool
	status := &cobra.Command{Use: "status", Args: cobra.NoArgs, Short: "Read-only daemon ownership and legacy-volume status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.loadConfig(); err != nil {
				return err
			}
			s, err := isolation.New(a.Docker, a.Cfg.Isolation).Status(cmd.Context())
			if asJSON {
				if writeErr := json.NewEncoder(a.Out).Encode(s); writeErr != nil {
					return writeErr
				}
			} else {
				fmt.Fprintf(a.Out, "boundary: %s\nnamespace: %s\nowner: %s\nclaimed: %t\nallowed: %t\n", s.Boundary, s.Namespace, s.Owner, s.Claimed, s.Allowed)
				if s.RecordedOwner != "" {
					fmt.Fprintf(a.Out, "recorded owner: %s\n", s.RecordedOwner)
				}
				if len(s.LegacyVolumes) > 0 {
					fmt.Fprintf(a.Out, "legacy volumes (contents unchanged): %v\n", s.LegacyVolumes)
				}
			}
			return isolationError(err)
		}}
	status.Flags().BoolVar(&asJSON, "json", false, "print non-secret ownership metadata as JSON")
	adopt := &cobra.Command{Use: "adopt", Args: cobra.NoArgs, Short: "Explicitly bind existing volumes to this user's dedicated daemon; preserve credentials",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.loadConfig(); err != nil {
				return err
			}
			s, err := isolation.New(a.Docker, a.Cfg.Isolation).Adopt(cmd.Context())
			if err != nil {
				return isolationError(err)
			}
			fmt.Fprintf(a.Out, "Dedicated daemon adopted: namespace=%s owner=%s\nExisting credentials, volumes and Task resources were preserved.\n", s.Namespace, s.Owner)
			return nil
		}}
	cmd.AddCommand(status, adopt)
	return cmd
}
