package execution

import (
	"context"
	"errors"
	"time"
)

// WithStore supplies the same durable lifecycle to application callers and
// fault-injection tests. RuntimeDriver supplies its default store automatically.
func (s *Service) WithStore(store RecordStore) *Service { s.store = store; return s }

func (d *RuntimeDriver) ExecutionStore(req Request) (RecordStore, error) {
	return NewFileStore(req.Task)
}

func (s *Service) Run(ctx context.Context, req Request) (Result, error) {
	if req.Mode == "" {
		req.Mode = Interactive
	}
	req.ToolGateway = false
	req.ToolEndpoint = ""
	s.closeTools = nil
	s.eventErr, s.record = nil, nil
	s.failureNotice = nil
	s.persistFailed, s.started = false, false
	s.facts = nil
	id := req.TaskID
	if id == "" {
		id = req.Task.ID()
	}
	result := Result{Mode: req.Mode, WorkspaceKind: req.WorkspaceKind(), TaskID: id, AttemptID: newAttemptID(), ArtifactState: ArtifactNotProduced,
		CleanupState: CleanupNotNeeded, ValidationState: "not_requested", ProductDeliveryState: "not_requested",
		EventDeliveryState: "pending", Stages: initialStages()}
	reject := func(kind ErrorKind, stage string, err error) (Result, error) {
		result.setStage(AdmissionPhase, ProgressFailed)
		s.emitFailure(ctx, &result, kind, stage, "execution admission failed")
		result.ExecutionState = result.State
		return result, s.withDeliveryError(&ExecutionError{Kind: kind, Stage: stage, Cause: err})
	}
	if err := req.Requirements.check(); err != nil {
		return reject(AdmissionRejected, "admission", err)
	}
	if err := validateRequest(req); err != nil {
		var ee *ExecutionError
		if errors.As(err, &ee) {
			return reject(ee.Kind, ee.Stage, ee.Cause)
		}
		return reject(AdmissionRejected, "task_spec", err)
	}
	if guard, ok := s.driver.(interface{ ValidateRuntime(Request) error }); ok {
		if err := guard.ValidateRuntime(req); err != nil {
			return reject(AdmissionRejected, "organization_runtime", err)
		}
	}
	if req.Pipeline != nil {
		s.facts = &PipelineFacts{Outputs: req.Pipeline.Spec.Outputs, Criteria: req.Pipeline.Spec.Criteria, Sink: req.Pipeline.Spec.Sink, OperationsDigest: contentBinding(req.Pipeline.Spec.Operations), ToolsDigest: contentBinding(req.Pipeline.Spec.Tools)}
	}

	if locker, ok := s.driver.(interface{ Lock(Request) (func(), error) }); ok {
		unlock, err := locker.Lock(req)
		if err != nil {
			kind := AdmissionRejected
			var ee *ExecutionError
			if errors.As(err, &ee) {
				kind = ee.Kind
			}
			return reject(kind, "admission", err)
		}
		defer unlock()
	}
	store := s.store
	if store == nil {
		if provider, ok := s.driver.(interface {
			ExecutionStore(Request) (RecordStore, error)
		}); ok {
			var err error
			store, err = provider.ExecutionStore(req)
			if err != nil {
				return reject(InternalFailed, "execution_record", err)
			}
		}
	}
	if store != nil {
		unlock, err := store.Lock()
		if err != nil {
			kind := InternalFailed
			if errors.Is(err, ErrRecordBusy) {
				kind = AdmissionRejected
			}
			return reject(kind, "execution_record", err)
		}
		defer unlock()
		r, err := store.Load()
		if err != nil {
			return reject(InternalFailed, "execution_record", err)
		}
		if r == nil {
			r = &TaskRecord{SchemaVersion: RecordSchema, TaskKey: req.Task.ID()}
		}
		if r.SchemaVersion != RecordSchema {
			return reject(AdmissionRejected, "execution_record", ErrRecordInvalid)
		}
		switch r.Recovery() {
		case "reconcile_prepare", "reconcile_cleanup", "reconcile_operations", "continue_verification", "retry_delivery", "continue_completion", "reconcile_retention":
			return reject(AdmissionRejected, "execution_record", ErrRecoveryRequired)
		}
		now := time.Now().UTC()
		r.Attempts = append(r.Attempts, AttemptRecord{Result: result, StartedAt: now, UpdatedAt: now})
		s.record = r
	}
	if req.Pipeline != nil && store == nil {
		return reject(AdmissionRejected, "execution_record", errors.New("general tasks require durable records"))
	}
	// The effective store is local to this run; an explicitly supplied test store
	// remains reusable, while RuntimeDriver resolves a fresh per-task FileStore.
	result, err := s.runWithStore(ctx, req, result, store)
	return result, err
}

func (s *Service) runWithStore(ctx context.Context, req Request, result Result, store RecordStore) (Result, error) {
	previous := s.store
	s.store = store
	defer func() { s.store = previous }()
	result, primary := s.run(ctx, req, result)
	if result.ExecutionState == "" {
		result.ExecutionState = result.State
	}
	s.stopOnControlFailure(ctx, req, &result, &primary)
	if primary != nil {
		kind := errorKind(primary, InternalFailed)
		var ee *ExecutionError
		if errors.As(primary, &ee) {
			kind = ee.Kind
		}
		result.State = stateForError(kind)
		if result.ExecutionState == "" {
			result.ExecutionState = result.State
		}
		for i := range result.Stages {
			if result.Stages[i].State == ProgressRunning {
				result.Stages[i].State = ProgressFailed
			}
		}
	}
	// A skipped phase means it was not requested, not that it succeeded.
	s.normalizeArtifactState(&result)
	for i := range result.Stages {
		if result.Stages[i].State == ProgressPending && result.Stages[i].Phase != FinalizePhase {
			result.Stages[i].State = ProgressSkipped
		}
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
	defer cancel()
	if err := s.checkpoint(finalCtx, &result, FinalizePhase, ProgressRunning); err != nil {
		primary = errors.Join(primary, err)
		result.setStage(FinalizePhase, ProgressFailed)
	} else {
		result.setStage(FinalizePhase, ProgressSucceeded)
	}
	// Persist the execution result before emitting a terminal notification.
	if err := s.save(finalCtx, result, primary, true); err != nil {
		primary = errors.Join(primary, err)
		result.setStage(FinalizePhase, ProgressFailed)
	}
	s.stopOnControlFailure(finalCtx, req, &result, &primary)
	if primary != nil {
		kind := errorKind(primary, InternalFailed)
		var ee *ExecutionError
		if errors.As(primary, &ee) {
			kind = ee.Kind
		}
		if notice := s.failureNotice; notice != nil {
			s.emitFailure(finalCtx, &result, notice.ErrorKind, notice.Stage, notice.Summary)
		} else {
			s.emitFailure(finalCtx, &result, kind, "execution_record", "execution finalization failed")
		}
	}
	if primary == nil {
		summary := "execution completed"
		if result.Next != Completed {
			summary = "agent session ready"
		}
		if result.ExecutionState == AgentFailed {
			summary = "agent exited unsuccessfully"
		}
		primary = s.deliverResult(finalCtx, &result, summary)
	} else {
		primary = s.withDeliveryError(primary)
	}
	result.EventDeliveryState = "complete"
	if s.eventErr != nil {
		result.EventDeliveryState = "failed"
	}
	// A final writer failure changes only event delivery, never the execution fact.
	if err := s.save(finalCtx, result, primary, true); err != nil {
		if primary == nil {
			result.State = InternalErr
		}
		primary = errors.Join(primary, err)
		s.stopOnControlFailure(finalCtx, req, &result, &primary)
	}
	return result, primary
}

func (s *Service) checkpoint(ctx context.Context, r *Result, phase Phase, state ProgressState) error {
	r.setStage(phase, state)
	if err := ctx.Err(); err != nil {
		kind := InternalFailed
		if phase != FinalizePhase {
			kind = errorKind(err, InternalFailed)
		}
		return &ExecutionError{Kind: kind, Stage: string(phase), Cause: err}
	}
	err := s.save(ctx, *r, nil, false)
	// A deadline may expire during filesystem I/O, after the initial check.
	// Keep activity cancellation distinct from a storage/finalization failure.
	if err != nil && ctx.Err() != nil && phase != FinalizePhase {
		return &ExecutionError{Kind: errorKind(ctx.Err(), InternalFailed), Stage: string(phase), Cause: errors.Join(ctx.Err(), err)}
	}
	if err == nil && s.facts != nil {
		switch state {
		case ProgressRunning:
			s.emit(ctx, *r, EventPhaseStarted, string(phase), "task phase started")
		case ProgressSucceeded, ProgressFailed:
			s.emit(ctx, *r, EventPhaseFinished, string(phase), "task phase finished")
		}
	}
	if err == nil {
		err = s.requiredEventError(r)
	}
	return err
}

// Startup can finish immediately before a deadline or a record write fails.
// Such a headless process must be stopped even when Wait was never reached.
func (s *Service) stopOnControlFailure(ctx context.Context, req Request, result *Result, primary *error) {
	if *primary == nil || !s.started || req.Mode != Headless || result.CleanupState != CleanupNotNeeded || result.Next == Completed {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
	defer cancel()
	if err := s.driver.Stop(cleanupCtx, req); err != nil {
		result.CleanupState = CleanupFailed
		*primary = errors.Join(*primary, &ExecutionError{Kind: InternalFailed, Stage: "stopping after control failure", Cause: err, Cleanup: []error{err}})
	} else {
		result.CleanupState = CleanupComplete
		result.ContainerState = string(ContainerStopped)
	}
}

func (s *Service) save(ctx context.Context, result Result, primary error, closed bool) error {
	if s.record == nil {
		return nil
	}
	a := &s.record.Attempts[len(s.record.Attempts)-1]
	a.Result, a.UpdatedAt, a.Closed = result, time.Now().UTC(), closed
	a.Pipeline = s.facts
	a.ErrorKind = ""
	a.SecondaryErrors = nil
	if primary != nil {
		a.ErrorKind = errorKind(primary, InternalFailed)
		var ee *ExecutionError
		if errors.As(primary, &ee) {
			a.ErrorKind = ee.Kind
		}
		if s.eventErr != nil {
			a.SecondaryErrors = []ErrorKind{DeliveryFailedKind}
		}
		if s.persistFailed {
			a.SecondaryErrors = append(a.SecondaryErrors, InternalFailed)
		}
	}
	if err := ctx.Err(); err != nil {
		s.persistFailed = true
		return &ExecutionError{Kind: InternalFailed, Stage: "execution_record", Cause: err}
	}
	if err := s.store.Save(ctx, s.record); err != nil {
		s.persistFailed = true
		return &ExecutionError{Kind: InternalFailed, Stage: "execution_record", Cause: err}
	}
	return nil
}

func validateRequest(req Request) error {
	if req.Authorize != nil {
		if err := req.Authorize(req); err != nil {
			return &ExecutionError{Kind: AdmissionRejected, Stage: "organization_policy", Cause: err}
		}
	}
	if req.Mode != "" && req.Mode != Interactive && req.Mode != Headless {
		return &ExecutionError{Kind: AdmissionRejected, Stage: "task_spec", Cause: errors.New("unsupported execution mode")}
	}
	if req.Pipeline != nil {
		if len(req.Pipeline.Spec.Tools) > 0 {
			if err := validateToolProvider(req); err != nil {
				return &ExecutionError{Kind: AdmissionRejected, Stage: "tool_capability", Cause: err}
			}
		}
		if _, err := NewFileStore(req.Task); err != nil {
			return &ExecutionError{Kind: AdmissionRejected, Stage: "task_spec", Cause: err}
		}
		if err := req.Pipeline.refresh(); err != nil {
			return &ExecutionError{Kind: AdmissionRejected, Stage: "task_spec", Cause: err}
		}
		if req.Task.ExecutionDir == "" || req.Task.WS.ID != "tasks" || req.Task.Name != req.Pipeline.Spec.TaskID || req.TaskID != "" && req.TaskID != req.Pipeline.Spec.TaskID || req.Mode != Headless || req.Detach || req.Prompt != req.Pipeline.Spec.Prompt || req.Config.DefaultAgent != req.Pipeline.Spec.Agent.Provider {
			return &ExecutionError{Kind: AdmissionRejected, Stage: "task_spec", Cause: errors.New("execution request does not match its task specification")}
		}
	}
	return nil
}

type Admission struct{ Existing, Running bool }

// Check is read-only preflight, not a reservation. Run rechecks under its locks.
func (s *Service) Check(ctx context.Context, req Request) (Admission, error) {
	if err := req.Requirements.check(); err != nil {
		return Admission{}, &ExecutionError{Kind: AdmissionRejected, Stage: "admission", Cause: err}
	}
	if err := validateRequest(req); err != nil {
		return Admission{}, err
	}
	store, err := NewFileStore(req.Task)
	if err != nil {
		return Admission{}, &ExecutionError{Kind: AdmissionRejected, Stage: "execution_record", Cause: err}
	}
	record, err := store.Load()
	if err != nil {
		return Admission{}, &ExecutionError{Kind: InternalFailed, Stage: "execution_record", Cause: err}
	}
	if record != nil {
		switch record.Recovery() {
		case "reconcile_prepare", "reconcile_cleanup", "reconcile_operations", "continue_verification", "retry_delivery", "continue_completion", "reconcile_retention":
			return Admission{}, &ExecutionError{Kind: AdmissionRejected, Stage: "execution_record", Cause: ErrRecoveryRequired}
		}
	}
	exists, running, err := s.driver.Inspect(ctx, req)
	if err != nil {
		return Admission{}, &ExecutionError{Kind: EnvironmentFailed, Stage: "inspect", Cause: err}
	}
	if err = s.driver.Admit(ctx, req, exists, running); err != nil {
		var ee *ExecutionError
		if errors.As(err, &ee) {
			return Admission{}, err
		}
		return Admission{}, &ExecutionError{Kind: AdmissionRejected, Stage: "admission", Cause: err}
	}
	return Admission{exists, running}, nil
}
