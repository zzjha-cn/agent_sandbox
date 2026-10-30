package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"sandx/internal/config"
	"sandx/internal/execution"
	"sandx/internal/governance"
)

func (a *App) constrainOrganization() error {
	if a.inspectionOrCleanup {
		return nil
	}
	s, err := governance.Load()
	if err != nil {
		return err
	}
	a.Organization = s
	if err = s.Constrain(&a.Cfg); err != nil {
		return err
	}
	a.Organization = s
	a.Loaded.Config = a.Cfg
	if s.Managed {
		if a.Loaded.Sources == nil {
			a.Loaded.Sources = map[string]config.Source{}
		}
		for _, key := range []string{"network.mode", "network.proxy", "network.cloud_mcp", "network.allow", "git.protect"} {
			a.Loaded.Sources[key] = config.Source{Layers: []string{"organization:" + s.Digest}}
		}
	}
	return nil
}
func (a *App) checkOrganization(initial governance.Snapshot, req execution.Request) error {
	s, err := governance.Load()
	if err != nil {
		return err
	}
	if s.Managed != initial.Managed || s.Digest != initial.Digest {
		return errors.New("organization policy changed; stop and resubmit with current policy")
	}
	return s.CheckRequest(req)
}
func (a *App) taskPolicy(spec execution.TaskSpec, initial governance.Snapshot) (execution.PipelinePolicy, error) {
	p, err := readTaskPolicy(a.Home)
	if err != nil {
		return p, err
	}
	s, err := governance.Load()
	if err != nil {
		return p, err
	}
	if s.Managed != initial.Managed || s.Digest != initial.Digest {
		return p, errors.New("organization policy changed; restart continuation with current policy")
	}
	if err = s.CheckSpec(spec); err != nil {
		return p, err
	}
	err = s.ConstrainPipeline(&p)
	return p, err
}
func (a *App) teamSink(public execution.EventSink, scope string) execution.EventSink {
	if !a.Organization.Managed {
		return public
	}
	return governance.Fanout{Queue: governance.Queue{Home: a.Home, Snapshot: a.Organization, Scope: scope}, Public: public}
}
func (a *App) organizationContext(ctx context.Context) (context.Context, func()) {
	return governance.Watch(ctx, a.Organization, governance.Load)
}
func (a *App) policyCmd() *cobra.Command {
	root := &cobra.Command{Use: "policy", Short: "Inspect the fixed administrator policy and effective ceilings"}
	root.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := governance.Load()
		if err != nil {
			return err
		}
		if err = a.setHome(); err != nil {
			return err
		}
		// Same global config used by TaskSpec; no provider/Docker/credential access.
		ld, err := config.LoadLayers(config.Paths(a.Home, "", ""))
		if err != nil {
			return err
		}
		constraintErr := s.Constrain(&ld.Config)
		network := map[string]any{"mode": ld.Config.Network.Mode, "proxy": ld.Config.Network.Proxy, "cloud_mcp": ld.Config.Network.CloudMCP, "allow": ld.Config.Network.Allow}
		if constraintErr != nil {
			network = nil
		}
		reason := ""
		if constraintErr != nil {
			reason = constraintErr.Error()
		}
		return json.NewEncoder(a.Out).Encode(struct {
			SchemaVersion string              `json:"schema_version"`
			Organization  governance.Snapshot `json:"organization"`
			Network       map[string]any      `json:"effective_network"`
			Allowed       bool                `json:"allowed"`
			Reason        string              `json:"reason,omitempty"`
			Mode          string              `json:"execution_mode"`
		}{"sbx.effective-policy/v1", s, network, constraintErr == nil, reason, "managed: synchronous TaskSpec only; unmanaged: existing modes"})
	}})
	return root
}
func (a *App) eventsCmd() *cobra.Command {
	root := &cobra.Command{Use: "events", Short: "Inspect or flush the private durable team-event outbox"}
	root.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.setHome(); err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(a.Home, "team-events"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		count := 0
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".json" {
				count++
			}
		}
		return json.NewEncoder(a.Out).Encode(map[string]any{"schema_version": "sbx.event-outbox/v1", "pending": count})
	}})
	root.AddCommand(&cobra.Command{Use: "flush", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.setHome(); err != nil {
			return err
		}
		s, err := governance.Load()
		if err != nil {
			return err
		}
		if err = s.Active(); err != nil {
			return err
		}
		q := governance.Queue{Home: a.Home, Snapshot: s}
		result, err := q.Flush(cmd.Context(), func() error {
			current, e := governance.Load()
			if e != nil {
				return e
			}
			if current.Digest != s.Digest || !current.Managed {
				return errors.New("organization policy changed during export")
			}
			return current.Active()
		})
		if e := json.NewEncoder(a.Out).Encode(result); e != nil {
			return e
		}
		if err != nil {
			return fmt.Errorf("event export: %w", err)
		}
		return nil
	}})
	return root
}
