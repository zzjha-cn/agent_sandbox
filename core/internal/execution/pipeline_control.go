package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sandx/internal/artifact"
	"sandx/internal/source"
	"strings"
	"time"

	"sandx/internal/completion"
)

// Continue operates only on a retained attempt. It never calls Create, Resume
// or Wait, and therefore cannot accidentally re-submit the task prompt.
func (s *Service) Continue(ctx context.Context, req Request, action, reviewer string) (result Result, primary error) {
	if req.Pipeline == nil {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("general task specification is required"))
	}
	if err := req.Pipeline.refresh(); err != nil {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, err)
	}
	if req.Task.Name != req.Pipeline.Spec.TaskID || req.Task.ExecutionDir == "" {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("task identity mismatch"))
	}
	if action != "verify" && action != "deliver" && action != "approve" && action != "reject" && action != "reconcile" && action != "apply" {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("unknown continuation action"))
	}
	if action == "approve" || action == "reject" {
		if strings.TrimSpace(reviewer) == "" || len(reviewer) > 128 || strings.ContainsAny(reviewer, "\x00\r\n") {
			return result, pipelineError(AdmissionRejected, EvaluatePhase, errors.New("an explicit reviewer identity of at most 128 characters is required"))
		}
	}
	if locker, ok := s.driver.(interface{ Lock(Request) (func(), error) }); ok {
		unlock, err := locker.Lock(req)
		if err != nil {
			return result, err
		}
		defer unlock()
	}
	store := s.store
	if store == nil {
		var err error
		store, err = NewFileStore(req.Task)
		if err != nil {
			return result, pipelineError(InternalFailed, AdmissionPhase, err)
		}
	}
	unlock, err := store.Lock()
	if err != nil {
		kind := InternalFailed
		if errors.Is(err, ErrRecordBusy) {
			kind = AdmissionRejected
		}
		return result, pipelineError(kind, AdmissionPhase, err)
	}
	defer unlock()
	record, err := store.Load()
	if err != nil {
		return result, pipelineError(InternalFailed, AdmissionPhase, err)
	}
	if record == nil || record.SchemaVersion != RecordSchema || len(record.Attempts) == 0 {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("no general attempt is recorded"))
	}
	last := &record.Attempts[len(record.Attempts)-1]
	result = last.Result
	facts := last.Pipeline
	if facts == nil || facts.Prepared == nil || result.ExecutionState != Finished || facts.Artifact == nil && action != "verify" && action != "reconcile" && action != "apply" {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("no complete retained artifact is available; reconcile the recorded execution stage"))
	}
	if facts.ToolsDigest != "" && facts.ToolsDigest != contentBinding(req.Pipeline.Spec.Tools) || facts.ToolsDigest == "" && len(req.Pipeline.Spec.Tools) > 0 {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("continuation must select recorded tools"))
	}
	if facts.OperationsDigest != "" && facts.OperationsDigest != contentBinding(req.Pipeline.Spec.Operations) {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("continuation must select recorded external operations"))
	}
	if facts.OperationsDigest == "" && len(req.Pipeline.Spec.Operations) > 0 {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("legacy attempt cannot acquire external write permissions"))
	}
	if contentBinding(facts.Outputs) != contentBinding(req.Pipeline.Spec.Outputs) || contentBinding(facts.Criteria) != contentBinding(req.Pipeline.Spec.Criteria) || contentBinding(facts.Sink) != contentBinding(req.Pipeline.Spec.Sink) {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("continuation must select the recorded outputs, criteria and sink"))
	}
	selection := source.Selection{}
	if req.Pipeline.Spec.Source != nil {
		selection = *req.Pipeline.Spec.Source
		binding, err := source.GrantBinding(req.Pipeline.Policy.Sources[selection.Ref])
		if err != nil || binding != facts.Prepared.Binding {
			return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("source authorization changed since execution"))
		}
	}
	if contentBinding(selection) != contentBinding(facts.Prepared.Selection) {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, errors.New("continuation must select the recorded source"))
	}
	if err := pipelineBoundary(req); err != nil {
		return result, pipelineError(AdmissionRejected, AdmissionPhase, err)
	}
	for _, op := range record.Operations {
		if op.Kind != "delivery" && op.External == nil && (op.State == "intent" || op.State == "unknown") {
			return result, pipelineError(AdmissionRejected, AdmissionPhase, ErrRecoveryRequired)
		}
	}
	oldStore := s.store
	s.store = store
	defer func() { s.store = oldStore }()
	s.record = record
	s.facts = facts
	if len(req.Pipeline.Spec.Tools) > 0 {
		if _, err := ReadToolCalls(req.Task, record); err != nil {
			return result, pipelineError(InternalFailed, AdmissionPhase, err)
		}
	}
	s.eventErr = nil
	s.persistFailed = false
	s.failureNotice = nil
	s.started = false
	if facts.Artifact != nil {
		if err := s.verifyArtifact(ctx, req, *facts.Artifact); err != nil {
			return result, pipelineError(AdmissionRejected, EvaluatePhase, err)
		}
	}
	switch action {
	case "verify", "reconcile", "apply":
		if action == "apply" {
			if err := s.driver.Stop(ctx, req); err != nil {
				primary = pipelineError(EnvironmentFailed, ExecutePhase, err)
				break
			}
			_, running, err := s.driver.Inspect(ctx, req)
			if err != nil || running {
				primary = pipelineError(EnvironmentFailed, ExecutePhase, errors.New("runtime must be stopped before external submission"))
				break
			}
			primary = s.executeExternal(ctx, req, &result)
			if primary != nil {
				break
			}
		}
		if action == "reconcile" {
			for _, call := range req.Pipeline.Spec.Operations {
				_, err := s.externalOperation(ctx, req, &result, call, true)
				if err != nil {
					primary = pipelineError(ProductOutcomeUnknown, EvaluatePhase, err)
					break
				}
			}
			if primary != nil {
				break
			}
		}
		if facts.Outputs == nil {
			primary = s.evaluateExternalOnly(ctx, req, &result)
			break
		}
		if facts.Artifact == nil {
			primary = s.recollect(ctx, req, &result)
			break
		}
		if err := s.checkpoint(ctx, &result, EvaluatePhase, ProgressRunning); err != nil {
			primary = err
			break
		}
		validation, err := completion.EvaluateBundle(ctx, filepath.Join(pipelineRoot(req), "snapshots", facts.Artifact.ID, "outputs"), filepath.Join(pipelineRoot(req), "snapshots", facts.Artifact.ID, "workspace"), facts.Artifact.Manifest, facts.Artifact.WorkspaceManifest, artifact.Filter(facts.Prepared.Baseline, req.Pipeline.Policy.Exclude), facts.Criteria, req.Pipeline.Policy.Criteria, req.Pipeline.Verifier, externalEvaluator{s, req, &result})
		if err != nil {
			primary = pipelineError(EnvironmentFailed, EvaluatePhase, err)
			break
		}
		s.guardExternalCompletion(req, &validation)
		validation.ArtifactDigest = facts.Artifact.Digest
		facts.Validation = &validation
		facts.Review = nil
		result.ValidationState = validation.State
		if err := s.verifyArtifact(ctx, req, *facts.Artifact); err != nil {
			primary = pipelineError(EnvironmentFailed, EvaluatePhase, err)
			break
		}
		progress := ProgressSucceeded
		if validation.State != "passed" {
			progress = ProgressFailed
		}
		if err := s.checkpoint(ctx, &result, EvaluatePhase, progress); err != nil {
			primary = err
			break
		}
		switch validation.State {
		case "unknown":
			primary = pipelineError(ProductOutcomeUnknown, EvaluatePhase, errors.New("external operation requires reconciliation"))
		case "passed":
			if req.Pipeline.Spec.Sink != nil {
				primary = s.deliverPipeline(ctx, req, &result)
			}
		case "needs_review":
			primary = pipelineError(ReviewRequired, EvaluatePhase, errors.New("completion requires trusted review"))
		case "failed":
			primary = pipelineError(VerificationFailed, EvaluatePhase, errors.New("completion requirements failed"))
		case "error":
			primary = pipelineError(EnvironmentFailed, EvaluatePhase, errors.New("completion verifier failed"))
		}
	case "approve", "reject":
		validation := facts.Validation
		binding, err := completion.Binding(facts.Criteria, req.Pipeline.Policy.Criteria)
		if err != nil || validation == nil || validation.State != "needs_review" || validation.ArtifactDigest != facts.Artifact.Digest || binding != validation.CriteriaDigest {
			return result, pipelineError(AdmissionRejected, EvaluatePhase, errors.New("review evidence is missing, stale or no longer awaiting review"))
		}
		decision := "passed"
		if action == "reject" {
			decision = "failed"
		}
		facts.Review = &ReviewRecord{ArtifactDigest: facts.Artifact.Digest, CriteriaDigest: binding, Decision: decision, Reviewer: reviewer, Time: time.Now().UTC().Format(time.RFC3339Nano)}
		for i := range validation.Evidence {
			if validation.Evidence[i].State == "needs_review" {
				validation.Evidence[i].State = decision
				validation.Evidence[i].Reason = "trusted_manual_review"
			}
		}
		validation.State = decision
		result.ValidationState = decision
		progress := ProgressSucceeded
		if decision == "failed" {
			progress = ProgressFailed
		}
		if err := s.checkpoint(ctx, &result, EvaluatePhase, progress); err != nil {
			primary = err
			break
		}
		if decision == "failed" {
			primary = pipelineError(VerificationFailed, EvaluatePhase, errors.New("trusted review rejected the artifact"))
		} else if req.Pipeline.Spec.Sink != nil {
			primary = s.deliverPipeline(ctx, req, &result)
		}
	case "deliver":
		primary = s.deliverPipeline(ctx, req, &result)
	}
	if primary == nil {
		result.State = Finished
	} else {
		var classified *ExecutionError
		if errors.As(primary, &classified) {
			result.State = stateForError(classified.Kind)
		} else {
			result.State = InternalErr
		}
		for i := range result.Stages {
			if result.Stages[i].State == ProgressRunning {
				result.Stages[i].State = ProgressFailed
			}
		}
	}
	s.normalizeArtifactState(&result)
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
	defer cancel()
	if err := s.checkpoint(finalCtx, &result, FinalizePhase, ProgressRunning); err != nil {
		primary = errors.Join(primary, err)
	} else {
		result.setStage(FinalizePhase, ProgressSucceeded)
	}
	if err := s.save(finalCtx, result, primary, true); err != nil {
		return result, errors.Join(primary, err)
	}
	if primary == nil {
		primary = s.deliverResult(finalCtx, &result, "task continuation completed")
	} else {
		kind := InternalFailed
		var classified *ExecutionError
		if errors.As(primary, &classified) {
			kind = classified.Kind
		}
		s.emitFailure(finalCtx, &result, kind, "continuation", "task continuation failed")
		primary = s.withDeliveryError(primary)
	}
	result.EventDeliveryState = "complete"
	if s.eventErr != nil {
		result.EventDeliveryState = "failed"
	}
	return result, errors.Join(primary, s.save(finalCtx, result, primary, true))
}

func (s *Service) recollect(ctx context.Context, req Request, result *Result) error {
	root := filepath.Join(pipelineRoot(req), "snapshots", result.AttemptID)
	if err := controlledDirectories(req.Task.Home, root); err != nil {
		return err
	}
	if _, err := os.Lstat(root); err == nil {
		quarantine := result.AttemptID + "-incomplete-" + newAttemptID()
		s.facts.Quarantines = append(s.facts.Quarantines, quarantine)
		if err := s.checkpoint(ctx, result, CollectPhase, ProgressRunning); err != nil {
			return err
		}
		if err := os.Rename(root, filepath.Join(filepath.Dir(root), quarantine)); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(root)); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.finishPipeline(ctx, req, result)
}
