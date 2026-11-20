package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"
	"sandx/internal/projectmemory"
)

func (a *App) addMemoryRetrievalCommands(root *cobra.Command) {
	var project, repo string
	var watch []string
	q := projectmemory.Selection{}
	search := &cobra.Command{Use: "search", Short: "Explain approved memory relevance and applicability without pinning a snapshot (JSON)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(false)
		if e != nil {
			return e
		}
		// Resolve authorization before reading explicitly selected input files.
		if _, e = s.Load(project); e != nil {
			return e
		}
		observation, e := (projectmemory.FileObservationProvider{Root: repo, Watch: watch, Facts: q.Facts}).Observe(cmd.Context())
		facts := observation.Values
		if e != nil {
			return e
		}
		selection := q
		selection.Facts = facts
		v, e := s.Queries().SearchWithin(cmd.Context(), projectmemory.QueryContext{Project: project, Scope: selection}, projectmemory.Query{Text: selection.Query})
		if e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(v)
	}}
	search.Flags().StringVar(&project, "project", "", "registered authorized project")
	search.Flags().StringVar(&q.Query, "query", "", "lexical query (max 2048 bytes / 64 terms); empty explains applicability")
	search.Flags().StringSliceVar(&q.Paths, "paths", nil, "relative affected paths")
	search.Flags().StringSliceVar(&q.Tags, "tags", nil, "matching tags")
	search.Flags().StringToStringVar(&q.Facts, "facts", nil, "operator-observed equality facts, key=value; never credentials")
	search.Flags().StringSliceVar(&watch, "watch", nil, "explicit relative files to hash within repo")
	search.Flags().StringVar(&repo, "repo", ".", "input root for watch files")
	search.Flags().IntVar(&q.Limit, "limit", 0, "entry budget, 1-20 (zero uses 20)")
	search.Flags().IntVar(&q.Bytes, "max-bytes", 0, "context byte budget, 256-16384 (zero uses 16384)")
	_ = search.MarkFlagRequired("project")
	root.AddCommand(search)

	feedback := &cobra.Command{Use: "feedback", Short: "Record or inspect explicit operator feedback; never changes approval"}
	var listProject string
	list := &cobra.Command{Use: "list", Short: "List digest-bound feedback assertions (JSON)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(false)
		if e != nil {
			return e
		}
		p, e := s.Load(listProject)
		if e != nil {
			return e
		}
		v := p.Feedback
		if v == nil {
			v = []projectmemory.Feedback{}
		}
		return json.NewEncoder(a.Out).Encode(v)
	}}
	list.Flags().StringVar(&listProject, "project", "", "registered project")
	_ = list.MarkFlagRequired("project")
	feedback.AddCommand(list)
	var addProject string
	input := projectmemory.Feedback{}
	add := &cobra.Command{Use: "add <entry>", Short: "Record a personal assertion bound to an exact entry digest (not verification)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(true)
		if e != nil {
			return e
		}
		input.EntryID = args[0]
		v, e := s.Management().RecordFeedback(cmd.Context(), addProject, input, 0)
		if e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(v)
	}}
	add.Flags().StringVar(&addProject, "project", "", "registered project")
	add.Flags().StringVar(&input.EntryDigest, "digest", "", "exact displayed entry revision digest")
	add.Flags().StringVar(&input.Kind, "kind", "", "retrieved|injected|helpful|validated|counterexample (operator assertion)")
	add.Flags().StringVar(&input.Note, "note", "", "bounded safe explanation, no raw logs or credentials")
	add.Flags().StringVar(&input.Task, "task", "", "optional task reference (operator declaration)")
	add.Flags().StringVar(&input.Actor, "actor", "operator", "personal declaration; managed identity comes from host UID")
	for _, name := range []string{"project", "digest", "kind", "note"} {
		_ = add.MarkFlagRequired(name)
	}
	feedback.AddCommand(add)
	root.AddCommand(feedback)
}
