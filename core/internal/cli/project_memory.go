package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"sandx/internal/execution"
	"sandx/internal/governance"
	"sandx/internal/projectmemory"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

// Local memory commands never require Docker or a Git working directory. Shared
// governance identities are not yet implemented; managed profiles are read-only.
func (a *App) memoryStore(mutate bool) (projectmemory.Store, error) {
	if a.Home == "" {
		if e := a.setHome(); e != nil {
			return projectmemory.Store{}, e
		}
	}
	org, e := governance.Load()
	if e != nil {
		return projectmemory.Store{}, e
	}
	if org.Managed && mutate {
		return projectmemory.Store{}, errors.New("project-memory mutation requires authenticated organization review (not available yet); use a personal profile")
	}
	return projectmemory.Store{Home: a.Home}, nil
}
func (a *App) addProjectMemoryCommands(root *cobra.Command) {
	project := &cobra.Command{Use: "project", Short: "Register and bind local project knowledge"}
	project.AddCommand(&cobra.Command{Use: "create <project>", Short: "Create an empty local project memory store", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(true)
		if e != nil {
			return e
		}
		v, e := s.Create(args[0])
		if e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(v)
	}})
	var bindProject, repo string
	bind := &cobra.Command{Use: "bind", Short: "Bind a Git repository and all its worktrees to one project", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(true)
		if e != nil {
			return e
		}
		ws, e := workspace.Resolve(repo)
		if e != nil {
			return e
		}
		return s.Bind(bindProject, ws.Root)
	}}
	bind.Flags().StringVar(&bindProject, "project", "", "registered project")
	bind.Flags().StringVar(&repo, "repo", ".", "Git repository or worktree directory")
	_ = bind.MarkFlagRequired("project")
	project.AddCommand(bind)
	project.AddCommand(&cobra.Command{Use: "show <project>", Short: "Print project metadata and immutable review history as JSON", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(false)
		if e != nil {
			return e
		}
		v, e := s.Load(args[0])
		if e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(v)
	}})
	root.AddCommand(project)
	var snapProject string
	var snapPaths, snapTags []string
	snapshot := &cobra.Command{Use: "snapshot", Short: "Freeze approved project knowledge and print a reusable snapshot (JSON)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(true)
		if e != nil {
			return e
		}
		v, e := s.Select(snapProject, projectmemory.Selection{Paths: snapPaths, Tags: snapTags})
		if e != nil {
			return e
		}
		if e = s.Pin(v); e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(v)
	}}
	snapshot.Flags().StringVar(&snapProject, "project", "", "registered project")
	snapshot.Flags().StringSliceVar(&snapPaths, "paths", nil, "relative affected paths")
	snapshot.Flags().StringSliceVar(&snapTags, "tags", nil, "matching tags")
	_ = snapshot.MarkFlagRequired("project")
	root.AddCommand(snapshot)
	var addProject, file string
	add := &cobra.Command{Use: "add", Short: "Submit a manual experience candidate (never automatically approved)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, e := a.memoryStore(true)
		if e != nil {
			return e
		}
		b, e := projectmemory.ReadFile(file, projectmemory.MaxBytes)
		if e != nil {
			return e
		}
		v, e := projectmemory.DecodeInput(bytes.NewReader(b))
		if e != nil {
			return e
		}
		entry, e := s.Add(addProject, v)
		if e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(entry)
	}}
	add.Flags().StringVar(&addProject, "project", "", "registered project")
	add.Flags().StringVar(&file, "file", "", "JSON with kind/title/content/source and optional paths/tags/supersedes")
	_ = add.MarkFlagRequired("project")
	_ = add.MarkFlagRequired("file")
	root.AddCommand(add)
	for _, name := range []string{"list", "candidates", "show"} {
		name := name
		var project string
		c := &cobra.Command{Use: name, Short: "Read project memory entries as JSON", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			s, e := a.memoryStore(false)
			if e != nil {
				return e
			}
			p, e := s.Load(project)
			if e != nil {
				return e
			}
			if name == "show" {
				for _, entry := range p.Latest() {
					if entry.ID == args[0] {
						return json.NewEncoder(a.Out).Encode(entry)
					}
				}
				return errors.New("memory entry not found")
			}
			entries := []projectmemory.Entry{}
			for _, entry := range p.Latest() {
				if name != "candidates" || entry.Status == "candidate" {
					entries = append(entries, entry)
				}
			}
			return json.NewEncoder(a.Out).Encode(entries)
		}}
		if name == "show" {
			c.Use = "show <entry>"
			c.Args = cobra.ExactArgs(1)
		}
		c.Flags().StringVar(&project, "project", "", "registered project")
		_ = c.MarkFlagRequired("project")
		root.AddCommand(c)
	}
	for name, status := range map[string]string{"approve": "approved", "reject": "rejected", "archive": "archived"} {
		name, status := name, status
		var project, digest, reviewer, reason string
		c := &cobra.Command{Use: name + " <entry>", Short: "Record an explicit review bound to the displayed entry digest", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			s, e := a.memoryStore(true)
			if e != nil {
				return e
			}
			entry, e := s.Review(project, args[0], digest, status, reviewer, reason)
			if e != nil {
				return e
			}
			return json.NewEncoder(a.Out).Encode(entry)
		}}
		c.Flags().StringVar(&project, "project", "", "registered project")
		c.Flags().StringVar(&digest, "digest", "", "current entry digest from show/candidates")
		c.Flags().StringVar(&reviewer, "reviewer", "", "local operator declaration (not authenticated team identity)")
		c.Flags().StringVar(&reason, "reason", "", "review explanation")
		for _, f := range []string{"project", "digest", "reviewer", "reason"} {
			_ = c.MarkFlagRequired(f)
		}
		root.AddCommand(c)
	}
	var scope, attempt string
	status := &cobra.Command{Use: "status <task>", Short: "Read the recorded memory mode and snapshot without Docker (JSON)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if _, e := a.memoryStore(false); e != nil {
			return e
		}
		var t task.Task
		var e error
		switch scope {
		case "general":
			t, e = task.NewExecution(a.Home, args[0])
		case "git":
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ws, err := workspace.Resolve(cwd)
			if err != nil {
				return err
			}
			t, e = task.New(a.Home, ws, args[0])
		default:
			return errors.New("--scope must be git or general")
		}
		if e != nil {
			return e
		}
		if attempt == "" {
			v, e := execution.ReadMemorySession(a.Home, t.ID())
			if e != nil {
				return e
			}
			return json.NewEncoder(a.Out).Encode(v)
		}
		if !projectmemory.ValidID(attempt) {
			return errors.New("invalid attempt ID")
		}
		b, e := projectmemory.ReadFile(filepath.Join(execution.MemoryTaskDir(a.Home, t.ID()), attempt+".json"), 1<<20)
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(a.Out, string(b))
		return e
	}}
	status.Flags().StringVar(&scope, "scope", "git", "task namespace: git or general")
	status.Flags().StringVar(&attempt, "attempt", "", "specific recorded attempt ID (default: current session)")
	root.AddCommand(status)
}
