package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"sandx/internal/config"
	"sandx/internal/execution"
	"sandx/internal/projectmemory"
	"sandx/internal/task"
	"sandx/internal/taskmemory"
	"sandx/internal/workspace"
)

func (a *App) memoryTask(name, scope string) (task.Task, error) {
	switch scope {
	case "general":
		return task.NewExecution(a.Home, name)
	case "git":
		cwd, e := os.Getwd()
		if e != nil {
			return task.Task{}, e
		}
		ws, e := workspace.Resolve(cwd)
		if e != nil {
			return task.Task{}, e
		}
		return task.New(a.Home, ws, name)
	default:
		return task.Task{}, errors.New("--scope must be git or general")
	}
}

// Extraction uses host-global/workspace settings, never repository/prep settings.
func (a *App) memoryConfig(t task.Task) (config.Config, error) {
	layers := config.Paths(a.Home, "", "")
	if t.WS.ID != "tasks" {
		layers = append(layers, config.Layer{Name: config.LayerWorkspace, Path: filepath.Join(a.Home, "workspaces", t.WS.ID+".toml")})
	}
	ld, e := config.LoadLayers(layers)
	return ld.Config, e
}
func (a *App) extractionModel(cfg config.Config, provider string) (taskmemory.Model, error) {
	if a.MemoryModel != nil {
		return a.MemoryModel, nil
	}
	ag := cfg.Agents[provider]
	key, e := taskmemory.ResolveKey(ag.APIKeyEnv, ag.APIKeyFile)
	if e != nil {
		return nil, e
	}
	return taskmemory.HTTPModel{APIKey: key}, nil
}
func (a *App) addExtractionCommands(root *cobra.Command) {
	var ps, pa string
	processing := &cobra.Command{Use: "processing <task>", Short: "Inspect automatic memory post-processing independently of business status (JSON)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if _, e := a.memoryStore(false); e != nil {
			return e
		}
		t, e := a.memoryTask(args[0], ps)
		if e != nil {
			return e
		}
		attempt := pa
		if attempt == "" {
			store, e := execution.NewFileStore(t)
			if e != nil {
				return e
			}
			r, e := store.Load()
			if e != nil {
				return e
			}
			if r == nil || len(r.Attempts) == 0 {
				return errors.New("task has no execution record")
			}
			attempt = r.Attempts[len(r.Attempts)-1].Result.AttemptID
		}
		v, e := taskmemory.ReadProcessing(a.Home, t.ID(), attempt)
		if e != nil {
			return e
		}
		return json.NewEncoder(a.Out).Encode(v)
	}}
	processing.Flags().StringVar(&ps, "scope", "git", "task namespace: git or general")
	processing.Flags().StringVar(&pa, "attempt", "", "attempt ID (default: latest)")
	root.AddCommand(processing)
	var hs, ha, hsummary string
	handoff := &cobra.Command{Use: "handoff <task>", Short: "Rebuild a task handoff from durable facts without running the task (JSON)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if _, e := a.memoryStore(hsummary != ""); e != nil {
			return e
		}
		t, e := a.memoryTask(args[0], hs)
		if e != nil {
			return e
		}
		h, e := execution.RefreshHandoff(t, ha)
		if e != nil {
			return e
		}
		if hsummary != "" {
			b, e := projectmemory.ReadFile(hsummary, 16<<10)
			if e != nil {
				return e
			}
			s, e := taskmemory.DecodeSummary(b)
			if e != nil {
				return e
			}
			if e = taskmemory.SaveSummary(a.Home, t.ID(), h.Attempt, s); e != nil {
				return e
			}
			h, e = execution.RefreshHandoff(t, h.Attempt)
			if e != nil {
				return e
			}
		}
		return json.NewEncoder(a.Out).Encode(h)
	}}
	handoff.Flags().StringVar(&hs, "scope", "git", "task namespace: git or general")
	handoff.Flags().StringVar(&hsummary, "summary", "", "attach operator-authored safe goal/progress/next steps JSON")
	handoff.Flags().StringVar(&ha, "attempt", "", "attempt ID (default: latest)")
	root.AddCommand(handoff)
	var scope, attempt, project, summary string
	extract := &cobra.Command{Use: "extract <task>", Short: "Freeze safe task material and call a tool-free model to propose candidates (may incur API charges)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, e := a.memoryStore(true)
		if e != nil {
			return e
		}
		t, e := a.memoryTask(args[0], scope)
		if e != nil {
			return e
		}
		cfg, e := a.memoryConfig(t)
		if e != nil {
			return e
		}
		h, e := execution.RefreshHandoff(t, attempt)
		if e != nil {
			return e
		}
		projectID := project
		if projectID == "" {
			projectID = cfg.Memory.Project
		}
		id, e := store.Resolve(projectID, t.WS.Root)
		if e != nil {
			return e
		}
		m := taskmemory.Material{Handoff: h}
		if h.OperatorSummary != nil {
			m.Summary = *h.OperatorSummary
		}
		if summary != "" {
			b, e := projectmemory.ReadFile(summary, 16<<10)
			if e != nil {
				return e
			}
			m.Summary, e = taskmemory.DecodeSummary(b)
			if e != nil {
				return e
			}
		}
		provider := cfg.DefaultAgent
		// Attempt preparation is a provider reference, not proof of model success.
		if b, e := projectmemory.ReadFile(filepath.Join(execution.MemoryTaskDir(a.Home, t.ID()), h.Attempt+".json"), 1<<20); e == nil {
			var session execution.MemorySession
			if e = json.Unmarshal(b, &session); e != nil {
				return e
			}
			if session.TaskKey != t.ID() {
				return errors.New("memory session task mismatch")
			}
			provider = session.Provider
		} else if !os.IsNotExist(e) {
			return e
		}
		ec := cfg.Memory.Extract
		jobs := taskmemory.Jobs{Home: a.Home}
		j, e := jobs.Create(id, provider, ec.Model, m, taskmemory.Limits{TimeoutSeconds: ec.TimeoutSeconds, MaxOutputTokens: ec.MaxOutputTokens, MaxCalls: ec.MaxCalls})
		if e != nil {
			return e
		}
		// Resolve authentication lazily: missing keys leave a queryable failed job.
		model := lazyExtractionModel{app: a, cfg: cfg, provider: provider}
		j, e = jobs.Run(cmd.Context(), j.ID, model, false)
		if outErr := json.NewEncoder(a.Out).Encode(j); outErr != nil {
			return outErr
		}
		return e
	}}
	extract.Flags().StringVar(&scope, "scope", "git", "task namespace: git or general")
	extract.Flags().StringVar(&attempt, "attempt", "", "attempt ID (default: latest)")
	extract.Flags().StringVar(&project, "project", "", "target local project (required for general tasks)")
	extract.Flags().StringVar(&summary, "summary", "", "explicitly authorized, scrubbed summary JSON (max 16 KiB)")
	root.AddCommand(extract)
	for _, action := range []string{"job", "retry", "recover"} {
		action := action
		c := &cobra.Command{Use: action + " <job>", Short: map[string]string{"job": "Inspect a frozen extraction job (JSON)", "retry": "Retry extraction using frozen material; may incur another API charge", "recover": "Mark an abandoned extraction interrupted without a model call"}[action], Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if _, e := a.memoryStore(action != "job"); e != nil {
				return e
			}
			jobs := taskmemory.Jobs{Home: a.Home}
			j, e := jobs.Load(args[0])
			if e != nil {
				return e
			}
			switch action {
			case "recover":
				j, e = jobs.Recover(j.ID)
			case "retry":
				// Retry does not reload task configuration, material or source. Only current
				// host authentication may change; the provider/model/budgets stay frozen.
				layers := config.Paths(a.Home, "", "")
				wsid, _, ok := strings.Cut(j.Material.Handoff.TaskKey, ".")
				if ok && wsid != "tasks" && workspace.ValidateTaskName(wsid) == nil {
					layers = append(layers, config.Layer{Name: config.LayerWorkspace, Path: filepath.Join(a.Home, "workspaces", wsid+".toml")})
				}
				ld, err := config.LoadLayers(layers)
				if err != nil {
					return err
				}
				j, e = jobs.Run(cmd.Context(), j.ID, lazyExtractionModel{app: a, cfg: ld.Config, provider: j.Provider}, true)
			}
			if err := json.NewEncoder(a.Out).Encode(j); err != nil {
				return err
			}
			return e
		}}
		root.AddCommand(c)
	}
	root.AddCommand(&cobra.Command{Use: "jobs", Short: "List locally persisted extraction jobs without Docker (JSON)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if _, e := a.memoryStore(false); e != nil {
			return e
		}
		entries, e := os.ReadDir(filepath.Join(a.Home, "memory-extractions"))
		if os.IsNotExist(e) {
			entries = nil
			e = nil
		}
		if e != nil {
			return e
		}
		if len(entries) > 10000 {
			return errors.New("too many jobs to list")
		}
		jobs := []taskmemory.Job{}
		for _, entry := range entries {
			if !entry.IsDir() || !projectmemory.ValidID(entry.Name()) {
				continue
			}
			j, e := (taskmemory.Jobs{Home: a.Home}).Load(entry.Name())
			if e != nil {
				return e
			}
			jobs = append(jobs, j)
		}
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.Before(jobs[j].CreatedAt) })
		return json.NewEncoder(a.Out).Encode(jobs)
	}})
}

type lazyExtractionModel struct {
	app      *App
	cfg      config.Config
	provider string
}

func (m lazyExtractionModel) Generate(ctx context.Context, j taskmemory.Job) ([]byte, error) {
	model, e := m.app.extractionModel(m.cfg, m.provider)
	if e != nil {
		return nil, e
	}
	return model.Generate(ctx, j)
}
