// Package execution owns the application level contract and lifecycle
// orchestration for running one task. The CLI supplies adapters for the
// existing Docker, prep, proxy, and sidecar operations.
package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"sandx/internal/agent"
	"sandx/internal/config"
	"sandx/internal/task"
)

type Mode string

const (
	Interactive Mode = "interactive"
	Headless    Mode = "headless"
)

type Request struct {
	Task         task.Task
	TaskID       string // optional caller-owned stable task identifier for protocol requests
	Mode         Mode
	Prompt       string
	Base         string
	Fresh        bool
	Detach       bool
	Config       config.Config
	Requirements Requirements
	Pipeline     *PipelinePlan
}

type State string

const (
	Ready               State = "ready"
	Running             State = "running"
	Finished            State = "finished"
	AgentFailed         State = "agent_failed"
	TimedOut            State = "timed_out"
	Cancelled           State = "cancelled"
	DeliveryFailed      State = "delivery_failed"
	Rejected            State = "admission_rejected"
	EnvironmentErr      State = "environment_failed"
	AgentStartErr       State = "agent_start_failed"
	InternalErr         State = "internal_failed"
	VerificationErr     State = "verification_failed"
	ReviewPending       State = "needs_review"
	ArtifactDeliveryErr State = "product_delivery_failed"
	OutcomeUnknown      State = "product_outcome_unknown"
)

type Disposition string

const (
	Attach    Disposition = "attach"
	Detached  Disposition = "detached"
	Completed Disposition = "completed"
)

type Result struct {
	TaskID               string          `json:"task_id"`
	AttemptID            string          `json:"attempt_id"`
	State                State           `json:"state"`
	Next                 Disposition     `json:"next,omitempty"`
	AgentExitCode        *int            `json:"agent_exit_code,omitempty"`
	ContainerState       string          `json:"container_state,omitempty"`
	ArtifactState        ArtifactState   `json:"artifact_state"`
	CleanupState         CleanupState    `json:"cleanup_state"`
	ExecutionState       State           `json:"execution_state"`
	ValidationState      string          `json:"validation_state"`
	ProductDeliveryState string          `json:"product_delivery_state"`
	EventDeliveryState   string          `json:"event_delivery_state"`
	Stages               []StageProgress `json:"stages"`
}

type ArtifactState string

const (
	ArtifactNotProduced ArtifactState = "not_produced"
	ArtifactPending     ArtifactState = "pending"
	ArtifactCollected   ArtifactState = "collected"
	ArtifactDelivered   ArtifactState = "delivered"
	ArtifactFailed      ArtifactState = "failed"
)

type CleanupState string

const (
	CleanupNotNeeded CleanupState = "not_needed"
	CleanupPending   CleanupState = "pending"
	CleanupComplete  CleanupState = "complete"
	CleanupFailed    CleanupState = "failed"
)

type ContainerState string

const (
	ContainerUnknown ContainerState = "unknown"
	ContainerRunning ContainerState = "running"
	ContainerStopped ContainerState = "stopped"
)

type AgentOutcome struct {
	ExitCode       *int
	ContainerState ContainerState
}

type EventType string

const (
	EventAdmitted      EventType = "admitted"
	EventPhaseStarted  EventType = "phase_started"
	EventPhaseFinished EventType = "phase_finished"
	EventAgentStarted  EventType = "agent_started"
	EventAgentExited   EventType = "agent_exited"
	EventCompleted     EventType = "completed"
	EventFailed        EventType = "failed"
)

// Event intentionally has no free-form message field: summaries are fixed,
// reviewed values so prompts, credentials, and environment values cannot leak.
type Event struct {
	Type      EventType `json:"type"`
	TaskID    string    `json:"task_id"`
	AttemptID string    `json:"attempt_id"`
	Time      time.Time `json:"time"`
	Phase     string    `json:"phase,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	ErrorKind ErrorKind `json:"error_kind,omitempty"`
	Stage     string    `json:"stage,omitempty"`
	Result    *Result   `json:"result,omitempty"`
}

type EventSink interface {
	Emit(context.Context, Event) error
}

type NopEventSink struct{}

func (NopEventSink) Emit(context.Context, Event) error { return nil }

// JSONLSink serializes one event per line for machine consumers.
type JSONLSink struct{ Encoder *json.Encoder }

func NewJSONLSink(w io.Writer) *JSONLSink                      { return &JSONLSink{Encoder: json.NewEncoder(w)} }
func (s *JSONLSink) Emit(_ context.Context, event Event) error { return s.Encoder.Encode(event) }

func newAttemptID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

type ErrorKind string

const (
	AdmissionRejected  ErrorKind = "admission_rejected"
	EnvironmentFailed  ErrorKind = "environment_failed"
	AgentStartFailed   ErrorKind = "agent_start_failed"
	AgentRunFailed     ErrorKind = "agent_failed"
	TimedOutKind       ErrorKind = "timed_out"
	CancelledKind      ErrorKind = "cancelled"
	InternalFailed     ErrorKind = "internal_failed"
	DeliveryFailedKind ErrorKind = "delivery_failed"
)

type ExecutionError struct {
	Kind             ErrorKind
	Stage            string
	Cause            error
	Cleanup          []error
	CleanupAttempted bool
}

func (e *ExecutionError) Error() string {
	message := e.Cause.Error()
	if len(e.Cleanup) == 0 {
		return message
	}
	parts := make([]string, 0, len(e.Cleanup))
	for _, cleanupErr := range e.Cleanup {
		parts = append(parts, cleanupErr.Error())
	}
	return fmt.Sprintf("%s; cleanup errors: %s", message, strings.Join(parts, "; "))
}

func (e *ExecutionError) Unwrap() []error {
	errs := make([]error, 0, 1+len(e.Cleanup))
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}
	errs = append(errs, e.Cleanup...)
	return errs
}

// Driver supplies runtime observations and admission checks to the service.
// Provisioner owns create/resume ordering; terminal interaction stays in CLI.
type Driver interface {
	Inspect(ctx context.Context, req Request) (exists, running bool, err error)
	Admit(ctx context.Context, req Request, exists, running bool) error
	Create(ctx context.Context, req Request) error
	Resume(ctx context.Context, req Request, running bool) error
	Wait(ctx context.Context, req Request) (AgentOutcome, error)
	Stop(ctx context.Context, req Request) error
}

type Service struct {
	driver        Driver
	events        EventSink
	eventErr      error
	store         RecordStore
	record        *TaskRecord
	failureNotice *Event
	persistFailed bool
	started       bool
	facts         *PipelineFacts
}

func NewService(driver Driver, sinks ...EventSink) *Service {
	sink := EventSink(NopEventSink{})
	if len(sinks) > 0 && sinks[0] != nil {
		sink = sinks[0]
	}
	return &Service{driver: driver, events: sink}
}

func (s *Service) emit(ctx context.Context, result Result, typ EventType, phase, summary string) {
	s.eventErr = errors.Join(s.eventErr, s.events.Emit(ctx, Event{Type: typ, TaskID: result.TaskID, AttemptID: result.AttemptID, Time: time.Now().UTC(), Phase: phase, Summary: summary}))
}

func (s *Service) emitResult(ctx context.Context, result Result, summary string) {
	s.eventErr = errors.Join(s.eventErr, s.events.Emit(ctx, Event{Type: EventCompleted, TaskID: result.TaskID, AttemptID: result.AttemptID, Time: time.Now().UTC(), Phase: "result", Summary: summary, Result: &result}))
}

func (s *Service) emitFailure(ctx context.Context, result *Result, kind ErrorKind, stage, summary string) {
	result.State = stateForError(kind)
	s.eventErr = errors.Join(s.eventErr, s.events.Emit(ctx, Event{Type: EventFailed, TaskID: result.TaskID, AttemptID: result.AttemptID, Time: time.Now().UTC(), Phase: stage, Summary: summary, ErrorKind: kind, Stage: stage, Result: result}))
}

func (s *Service) deferFailure(result *Result, kind ErrorKind, stage, summary string) {
	result.State = stateForError(kind)
	s.failureNotice = &Event{ErrorKind: kind, Stage: stage, Summary: summary}
}

func (s *Service) eventDeliveryError(result *Result) error {
	if s.eventErr == nil {
		return nil
	}
	result.State = DeliveryFailed
	return &ExecutionError{Kind: DeliveryFailedKind, Stage: "writing execution events", Cause: s.eventErr}
}

// If an earlier event failed but the writer recovers, publish an honest final
// record. A terminal write failure itself is detectable only through the exit
// status/error: a broken transport cannot guarantee a replacement record.
func (s *Service) deliverResult(ctx context.Context, result *Result, summary string) error {
	if s.eventErr != nil {
		s.emitFailure(ctx, result, DeliveryFailedKind, "event_delivery", "execution event delivery failed")
	} else {
		s.emitResult(ctx, *result, summary)
	}
	return s.eventDeliveryError(result)
}

// A failed execution remains the primary error even if its reporting also
// fails. Preserve both causes without treating delivery as resource cleanup.
func (s *Service) withDeliveryError(primary error) error {
	if s.eventErr == nil {
		return primary
	}
	return errors.Join(primary, &ExecutionError{Kind: DeliveryFailedKind, Stage: "writing execution events", Cause: s.eventErr})
}

func stateForError(kind ErrorKind) State {
	switch kind {
	case AdmissionRejected:
		return Rejected
	case AgentStartFailed:
		return AgentStartErr
	case AgentRunFailed:
		return AgentFailed
	case TimedOutKind:
		return TimedOut
	case CancelledKind:
		return Cancelled
	case DeliveryFailedKind:
		return DeliveryFailed
	case InternalFailed:
		return InternalErr
	case VerificationFailed:
		return VerificationErr
	case ReviewRequired:
		return ReviewPending
	case ProductDeliveryFailed:
		return ArtifactDeliveryErr
	case ProductOutcomeUnknown:
		return OutcomeUnknown
	default:
		return EnvironmentErr
	}
}

func errorKind(err error, fallback ErrorKind) ErrorKind {
	if errors.Is(err, agent.ErrSmokeTimeout) {
		return TimedOutKind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return TimedOutKind
	}
	if errors.Is(err, context.Canceled) {
		return CancelledKind
	}
	return fallback
}

func (s *Service) run(ctx context.Context, req Request, result Result) (Result, error) {
	if err := s.checkpoint(ctx, &result, AdmissionPhase, ProgressRunning); err != nil {
		return result, err
	}

	exists, running, err := s.driver.Inspect(ctx, req)
	if err != nil {
		kind := errorKind(err, EnvironmentFailed)
		s.deferFailure(&result, kind, "inspecting_task", "task inspection failed")
		return result, &ExecutionError{Kind: kind, Stage: "inspecting task", Cause: err}
	}
	if err = s.driver.Admit(ctx, req, exists, running); err != nil {
		kind := errorKind(err, AdmissionRejected)
		var classified *ExecutionError
		if errors.As(err, &classified) {
			kind = errorKind(err, classified.Kind)
		}
		s.deferFailure(&result, kind, "admission", "admission check failed")
		return result, &ExecutionError{Kind: kind, Stage: "checking admission", Cause: err}
	}
	if err := s.checkpoint(ctx, &result, AdmissionPhase, ProgressSucceeded); err != nil {
		return result, err
	}
	if err := s.checkpoint(ctx, &result, PreparePhase, ProgressRunning); err != nil {
		return result, err
	}
	if req.Pipeline != nil {
		if err := s.preparePipeline(ctx, req, &result); err != nil {
			return result, err
		}
		if err := s.authorizePrepared(req); err != nil {
			return result, pipelineError(AdmissionRejected, PreparePhase, err)
		}
	}
	s.emit(ctx, result, EventAdmitted, "admission", "task admitted")
	s.emit(ctx, result, EventPhaseStarted, "provision", "preparing task environment")
	if exists {
		err = s.driver.Resume(ctx, req, running)
	} else {
		err = s.driver.Create(ctx, req)
	}
	if err != nil {
		var executionErr *ExecutionError
		kind := errorKind(err, EnvironmentFailed)
		if errors.As(err, &executionErr) {
			kind = executionErr.Kind
			if len(executionErr.Cleanup) > 0 {
				result.CleanupState = CleanupFailed
			} else if executionErr.CleanupAttempted || !exists {
				// Provisioner.Create owns rollback and returns only after it has
				// attempted cleanup of resources created by this attempt.
				result.CleanupState = CleanupComplete
			}
		}
		stage := "creating task"
		if exists {
			stage = "resuming task"
		}
		s.emit(ctx, result, EventPhaseFinished, "provision", "task environment failed")
		s.deferFailure(&result, kind, stage, "task execution failed")
		if executionErr != nil {
			return result, err
		}
		return result, &ExecutionError{Kind: kind, Stage: stage, Cause: err}
	}
	s.started = !exists || !running
	if err := s.checkpoint(ctx, &result, PreparePhase, ProgressSucceeded); err != nil {
		return result, err
	}
	if err := s.checkpoint(ctx, &result, ExecutePhase, ProgressRunning); err != nil {
		return result, err
	}
	s.emit(ctx, result, EventPhaseFinished, "provision", "task environment ready")
	s.emit(ctx, result, EventAgentStarted, "agent", "agent started")
	if req.Mode == Headless && !req.Detach {
		outcome, waitErr := s.driver.Wait(ctx, req)
		if waitErr == nil && outcome.ExitCode == nil {
			waitErr = errors.New("missing agent exit code")
		}
		if waitErr != nil {
			kind := errorKind(waitErr, EnvironmentFailed)
			result.ContainerState = string(outcome.ContainerState)
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
			stopErr := s.driver.Stop(cleanupCtx, req)
			cancel()
			result.CleanupState = CleanupComplete
			var cleanup []error
			if stopErr != nil {
				result.CleanupState = CleanupFailed
				cleanup = append(cleanup, stopErr)
			} else {
				result.ContainerState = string(ContainerStopped)
			}
			s.deferFailure(&result, kind, "waiting_for_agent", "agent wait failed")
			return result, &ExecutionError{Kind: kind, Stage: "waiting for agent", Cause: waitErr, Cleanup: cleanup}
		}
		result.State = Finished
		result.Next = Completed
		result.AgentExitCode = outcome.ExitCode
		if outcome.ContainerState == "" {
			outcome.ContainerState = ContainerUnknown
		}
		result.ContainerState = string(outcome.ContainerState)
		if outcome.ExitCode != nil && *outcome.ExitCode != 0 {
			result.State = AgentFailed
		}
		summary := "execution completed"
		if result.State == AgentFailed {
			summary = "agent exited unsuccessfully"
		}
		s.emit(ctx, result, EventAgentExited, "agent", summary)
		result.ExecutionState = result.State
		progress := ProgressSucceeded
		if result.State == AgentFailed {
			progress = ProgressFailed
		}
		if err := s.checkpoint(ctx, &result, ExecutePhase, progress); err != nil {
			return result, err
		}
		if req.Pipeline != nil && result.ExecutionState == Finished {
			if err := s.finishPipeline(ctx, req, &result); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	if req.Detach {
		result.Next = Detached
		result.State = Running
	} else if req.Mode == Headless {
		result.Next = Detached
		result.State = Running
	} else {
		result.Next = Attach
		result.State = Ready
	}
	result.ExecutionState = result.State
	if err := s.checkpoint(ctx, &result, ExecutePhase, ProgressHandedOff); err != nil {
		return result, err
	}
	return result, nil
}
