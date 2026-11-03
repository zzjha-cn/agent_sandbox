package cli

import (
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"
	"sandx/internal/projectmemory"
)

func (a *App) addMemoryMaintenanceCommands(root *cobra.Command) {
	var exportProject string
	var ids []string
	export := &cobra.Command{Use: "export", Short: "Print a portable bundle of explicitly selected approved experience (JSON)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		// Sharing requires the same project grant as mutation in managed mode.
		s, e := a.memoryStore(true)
		if e != nil {
			return e
		}
		b, e := s.Export(exportProject, ids)
		if e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(b)
	}}
	export.Flags().StringVar(&exportProject, "project", "", "source local project")
	export.Flags().StringSliceVar(&ids, "entries", nil, "explicit approved entry IDs, comma-separated (1-20)")
	_ = export.MarkFlagRequired("project")
	_ = export.MarkFlagRequired("entries")
	root.AddCommand(export)
	var importProject, file string
	imp := &cobra.Command{Use: "import", Short: "Map a portable bundle to a local project as unapproved candidates", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(true)
		if e != nil {
			return e
		}
		raw, e := projectmemory.ReadFile(file, projectmemory.MaxBundle)
		if e != nil {
			return e
		}
		b, e := projectmemory.DecodeBundle(raw)
		if e != nil {
			return e
		}
		entries, e := s.ImportBundle(importProject, b)
		if e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(entries)
	}}
	imp.Flags().StringVar(&importProject, "project", "", "explicit destination local project")
	imp.Flags().StringVar(&file, "file", "", "reviewed portable bundle JSON, max 256 KiB")
	_ = imp.MarkFlagRequired("project")
	_ = imp.MarkFlagRequired("file")
	root.AddCommand(imp)
	for _, name := range []string{"inspect", "stats", "check"} {
		name := name
		var project string
		c := &cobra.Command{Use: name, Short: map[string]string{"inspect": "Report duplicate/conflict hints, review expiry and storage usage (JSON)", "stats": "Print project memory occupancy and current status counts (JSON)", "check": "Validate authoritative history and derive current views without a separate index (JSON)"}[name], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			s, e := a.memoryStore(false)
			if e != nil {
				return e
			}
			if project == "" {
				return errors.New("project required")
			}
			v, e := s.Inspect(project)
			if e != nil {
				return e
			}
			return json.NewEncoder(a.Out).Encode(v)
		}}
		c.Flags().StringVar(&project, "project", "", "registered project")
		_ = c.MarkFlagRequired("project")
		root.AddCommand(c)
	}
}
