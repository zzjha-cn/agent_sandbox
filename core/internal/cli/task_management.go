package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"sandx/internal/execution"
	"sandx/internal/isolation"
	"sandx/internal/protocol"
	"sandx/internal/task"
)

func (a *App) taskManagementCommands() []*cobra.Command {
	var listJSON bool
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List durable general tasks without Git, configuration or Docker", RunE: func(cmd *cobra.Command, _ []string) error {
		if err := a.setHome(); err != nil {
			return err
		}
		tasks, err := execution.ListTasks(a.Home)
		if err != nil {
			return err
		}
		if listJSON {
			return json.NewEncoder(a.Out).Encode(protocol.TaskList{SchemaVersion: protocol.TaskListSchema, Tasks: tasks})
		}
		for _, t := range tasks {
			if _, err := fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", t.TaskID, t.AttemptID, t.ExecutionState, t.Recovery); err != nil {
				return err
			}
		}
		return nil
	}}
	list.Flags().BoolVar(&listJSON, "json", false, "print versioned task list JSON")
	var attempt string
	var tail int
	var follow bool
	logs := &cobra.Command{Use: "logs <task>", Args: cobra.ExactArgs(1), Short: "Read current or retained attempt logs without Git or Docker", RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.setHome(); err != nil {
			return err
		}
		t, err := task.NewExecution(a.Home, args[0])
		if err != nil {
			return err
		}
		store, _ := execution.NewFileStore(t)
		r, err := store.Load()
		if err != nil {
			return err
		}
		if r == nil {
			return errors.New("task has no execution record")
		}
		id := attempt
		if id == "" {
			id = r.Attempts[len(r.Attempts)-1].Result.AttemptID
		}
		path, err := execution.AttemptLog(t, id)
		if err != nil {
			return err
		}
		b, err := readLog(path)
		if err != nil {
			return err
		}
		if tail < 0 {
			return errors.New("tail must be nonnegative")
		}
		n := tail
		if follow && n == 0 {
			n = 20
		}
		if _, err := a.Out.Write(tailLines(b, n)); err != nil {
			return err
		}
		if !follow {
			return nil
		}
		offset := int64(len(b))
		tick := time.NewTicker(followTick)
		defer tick.Stop()
		for {
			r, err := store.Load()
			if err != nil {
				return err
			}
			finished := false
			for _, at := range r.Attempts {
				if at.Result.AttemptID == id {
					finished = at.Closed
				}
			}
			current, err := execution.AttemptLog(t, id)
			if err != nil {
				return err
			}
			n, err := copyFrom(current, a.Out, offset)
			if err != nil {
				return err
			}
			offset += n
			if finished {
				return nil
			}
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-tick.C:
			}
		}
	}}
	logs.Flags().StringVar(&attempt, "attempt", "", "recorded attempt ID (default latest)")
	logs.Flags().IntVarP(&tail, "tail", "n", 0, "last n lines; 0 means all")
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "follow this attempt until its record closes")
	commands := []*cobra.Command{list, logs}
	for _, action := range []string{"stop", "clean", "recover"} {
		action := action
		cmd := &cobra.Command{Use: action + " <task>", Args: cobra.ExactArgs(1), Short: map[string]string{"stop": "Cancel execution and stop owned containers, retaining all facts", "clean": "Remove idle task runtime resources, retaining workspace, logs, artifacts and receipts", "recover": "Stop orphaned runtime and reconcile interrupted execution without replaying effects"}[action], RunE: func(cmd *cobra.Command, args []string) error {
			return a.manageTask(cmd.Context(), args[0], action)
		}}
		commands = append(commands, cmd)
	}
	return commands
}

func (a *App) manageTask(parent context.Context, name, action string) error {
	a.inspectionOrCleanup = true
	if err := a.loadConfig(); err != nil {
		return err
	}
	t, err := task.NewExecution(a.Home, name)
	if err != nil {
		return err
	}
	if err := a.checkIsolation(parent, false); err != nil {
		return err
	}
	manager := execution.RuntimeManager{Docker: a.Docker, Isolation: isolation.New(a.Docker, a.Cfg.Isolation)}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	if action == "stop" {
		if _, err := execution.RequestStop(t); err != nil {
			return err
		}
	}
	for {
		err = manager.Mutate(ctx, t, action)
		if action != "stop" || !errors.Is(err, execution.ErrRecordBusy) {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("stop requested; supervisor still busy: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		return err
	}
	store, _ := execution.NewFileStore(t)
	record, err := store.Load()
	if err != nil {
		return err
	}
	return json.NewEncoder(a.Out).Encode(protocol.TaskMaintenance{SchemaVersion: protocol.TaskMaintenanceSchema, TaskID: name, Action: action, Recovery: record.Recovery()})
}
