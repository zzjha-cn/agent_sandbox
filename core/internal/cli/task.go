package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"sandx/internal/completion"
	"sandx/internal/execution"
	"sandx/internal/isolation"
	"sandx/internal/protocol"
	"sandx/internal/task"
)

const generalInputLimit = execution.MaxTaskBytes

func decodeTask(r io.Reader) (execution.TaskSpec, error) {
	return execution.DecodeTaskSpec(r)
}

func readTaskPolicy(home string) (execution.PipelinePolicy, error) {
	policy := execution.DefaultPipelinePolicy()
	name := filepath.Join(home, "execution-policy.toml")
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return policy, nil
	}
	if err != nil {
		return policy, err
	}
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return policy, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return policy, err
	}
	if !info.Mode().IsRegular() || info.Size() > generalInputLimit || info.Mode().Perm()&0022 != 0 {
		return policy, errors.New("execution-policy.toml must be an operator-owned regular file, at most 1 MiB, without group/other write permission")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
		return policy, errors.New("execution policy owner does not match the operator")
	}
	b, err := io.ReadAll(io.LimitReader(f, generalInputLimit+1))
	if err != nil {
		return policy, err
	}
	if len(b) > generalInputLimit {
		return policy, errors.New("execution policy exceeds 1 MiB")
	}
	md, err := toml.Decode(string(b), &policy)
	if err != nil {
		return policy, err
	}
	if len(md.Undecoded()) > 0 {
		return policy, errors.New("execution policy contains unknown fields")
	}
	return policy, nil
}

func (a *App) generalRequest(ctx context.Context, spec execution.TaskSpec) (execution.Request, error) {
	if err := a.loadConfig(); err != nil {
		return execution.Request{}, err
	}
	t, err := task.NewExecution(a.Home, spec.TaskID)
	if err != nil {
		return execution.Request{}, err
	}
	a.WS = t.WS
	initial := a.Organization
	policy, err := a.taskPolicy(spec, initial)
	if err != nil {
		return execution.Request{}, err
	}
	a.setRunAgent(spec.Agent.Provider)
	plan := &execution.PipelinePlan{Spec: spec, Policy: policy, Reauthorize: func() (execution.PipelinePolicy, error) { return a.taskPolicy(spec, initial) }}
	plan.Verifier = execution.DockerTestRunner{Docker: a.Docker, Isolation: isolation.New(a.Docker, a.Cfg.Isolation), TempRoot: filepath.Join(a.Home, "verifications"), Limits: policy.ArtifactLimits, Authorize: func(rule completion.Rule) error {
		current, err := a.taskPolicy(spec, initial)
		if err != nil {
			return err
		}
		for _, criterion := range spec.Criteria {
			if criterion.Kind == "tests" {
				if registered, ok := current.Criteria[criterion.Ref]; ok && reflect.DeepEqual(registered, rule) {
					return nil
				}
			}
		}
		return errors.New("verifier authorization changed")
	}}
	if err := plan.Validate(); err != nil {
		return execution.Request{}, err
	}
	return execution.Request{Task: t, TaskID: spec.TaskID, Mode: execution.Headless, Prompt: spec.Prompt, Config: a.Cfg, OrganizationPolicyDigest: initial.Digest, Pipeline: plan, Authorize: func(req execution.Request) error { return a.checkOrganization(initial, req) }}, nil
}

func (a *App) taskCmd() *cobra.Command {
	root := &cobra.Command{Use: "task", Short: "Run and inspect general tasks with registered inputs and delivery targets"}
	prune := &cobra.Command{Use: "prune <task> <attempt>", Args: cobra.ExactArgs(2), Short: "Explicitly prune a reconciled artifact, retaining history and delivery targets", RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.setHome(); err != nil {
			return err
		}
		t, err := task.NewExecution(a.Home, args[0])
		if err != nil {
			return err
		}
		if err := execution.PruneArtifacts(cmd.Context(), t, args[1]); err != nil {
			kind := execution.AdmissionRejected
			var ioErr *os.PathError
			if errors.Is(err, execution.ErrRecordInvalid) || errors.As(err, &ioErr) {
				kind = execution.InternalFailed
			}
			return &execution.ExecutionError{Kind: kind, Stage: "artifact_retention", Cause: err}
		}
		return json.NewEncoder(a.Out).Encode(protocol.Retention{SchemaVersion: "sbx.artifact-retention/v1", State: "pruned"})
	}}
	root.AddCommand(prune)
	root.AddCommand(a.taskManagementCommands()...)
	for _, action := range []string{"run", "verify", "deliver", "approve", "reject", "reconcile", "apply"} {
		action := action
		var specPath, reviewer string
		cmd := &cobra.Command{Use: action + " --spec <file|->", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			var reader io.Reader = a.In()
			var input *os.File
			if specPath == "" {
				return a.writeProtocolFailure(&protocol.CodedError{Code: "invalid_task", Err: errors.New("--spec is required")})
			}
			if specPath != "-" {
				var err error
				input, err = os.Open(specPath)
				if err != nil {
					return a.writeProtocolFailure(&protocol.CodedError{Code: "invalid_task", Err: err})
				}
				defer input.Close()
				reader = input
			}
			spec, err := decodeTask(reader)
			if err != nil {
				return a.writeProtocolFailure(&protocol.CodedError{Code: "invalid_task", Err: err})
			}
			return a.submitTask(cmd.Context(), spec, action, reviewer)
		}}
		cmd.Flags().StringVar(&specPath, "spec", "", "general task JSON (maximum 1 MiB), or - for stdin")
		if action == "approve" || action == "reject" {
			cmd.Flags().StringVar(&reviewer, "reviewer", "", "operator identity recorded with the review")
		}
		root.AddCommand(cmd)
	}
	var asJSON bool
	status := &cobra.Command{Use: "status <task>", Args: cobra.ExactArgs(1), Short: "Read general-task records without Git, configuration or Docker", RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.setHome(); err != nil {
			return err
		}
		t, err := task.NewExecution(a.Home, args[0])
		if err != nil {
			return err
		}
		store, err := execution.NewFileStore(t)
		if err != nil {
			return err
		}
		record, err := store.Load()
		calls, toolErr := execution.ReadToolCalls(t, record)
		if err == nil {
			err = toolErr
		}
		out := protocol.Status{ToolCalls: calls, SchemaVersion: protocol.TaskStatusSchema, Recorded: record != nil, Recovery: "not_recorded", Record: record}
		if err != nil {
			out.Recovery = "record_invalid"
		} else if record != nil {
			out.Recovery = record.Recovery()
		}
		out.SetRecovery(out.Recovery)
		if asJSON {
			if writeErr := json.NewEncoder(a.Out).Encode(out); writeErr != nil {
				return writeErr
			}
		} else {
			fmt.Fprintf(a.Out, "task %s: %s\n", args[0], out.Recovery)
			if record != nil {
				for _, attempt := range record.Attempts {
					fmt.Fprintf(a.Out, "  %s: execution=%s validation=%s delivery=%s\n", attempt.Result.AttemptID, attempt.Result.ExecutionState, attempt.Result.ValidationState, attempt.Result.ProductDeliveryState)
				}
			}
		}
		if err != nil {
			return &execution.ExecutionError{Kind: execution.InternalFailed, Stage: "execution_record", Cause: fmt.Errorf("cannot verify execution record: %w", err)}
		}
		return nil
	}}
	status.Flags().BoolVar(&asJSON, "json", false, "print versioned general-task status JSON")
	root.AddCommand(status)
	return root
}

func (a *App) submitTask(parent context.Context, spec execution.TaskSpec, action, reviewer string) error {
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if spec.Limits.TimeoutSeconds > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, time.Duration(spec.Limits.TimeoutSeconds)*time.Second)
		defer stop()
	}
	req, err := a.generalRequest(ctx, spec)
	if err != nil {
		return a.writeProtocolFailure(&execution.ExecutionError{Kind: execution.AdmissionRejected, Stage: "task_spec", Cause: err})
	}
	ctx, stopOrganization := a.organizationContext(ctx)
	defer stopOrganization()
	runApp := *a
	runApp.Out = a.Err
	runApp.Docker = a.Docker.WithContext(ctx)
	driver := runApp.executionDriver()
	events := &terminalEventSink{sink: a.teamSink(execution.NewJSONLSink(a.Out), req.Task.WS.ID)}
	service := execution.NewService(driver, events)
	var result execution.Result
	if action == "run" {
		result, err = service.Run(ctx, req)
	} else {
		result, err = service.Continue(ctx, req, action, reviewer)
	}
	if err == nil && result.ExecutionState == execution.AgentFailed {
		err = &execution.ExecutionError{Kind: execution.AgentRunFailed, Stage: "agent", Cause: errors.New("agent exited unsuccessfully")}
	}
	if err != nil && !events.terminal && !events.writeFailed {
		return a.writeProtocolFailure(err)
	}
	return err
}

// Continuation can reject before lifecycle events start. Emit one safe failure
// response in that case, without duplicating a terminal event or retrying a
// broken stdout transport.
type terminalEventSink struct {
	sink        execution.EventSink
	terminal    bool
	writeFailed bool
}

func (s *terminalEventSink) Emit(ctx context.Context, e execution.Event) error {
	err := s.sink.Emit(ctx, e)
	if err != nil {
		s.writeFailed = true
	} else if e.Type == execution.EventCompleted || e.Type == execution.EventFailed {
		s.terminal = true
	}
	return err
}
