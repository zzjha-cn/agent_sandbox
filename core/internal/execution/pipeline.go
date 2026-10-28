package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"sandx/internal/artifact"
	"sandx/internal/completion"
	"sandx/internal/sink"
	"sandx/internal/source"
)

const (
	VerificationFailed    ErrorKind = "verification_failed"
	ReviewRequired        ErrorKind = "review_required"
	ProductDeliveryFailed ErrorKind = "product_delivery_failed"
	ProductOutcomeUnknown ErrorKind = "product_outcome_unknown"
)

func pipelineError(kind ErrorKind, phase Phase, err error) error {
	if kind != ProductOutcomeUnknown {
		kind = errorKind(err, kind)
	}
	return &ExecutionError{Kind: kind, Stage: string(phase), Cause: err}
}

func pipelineRoot(req Request) string {
	return filepath.Join(req.Task.Home, "artifacts", req.Task.WS.ID, req.Task.Name)
}

func (s *Service) preparePipeline(ctx context.Context, req Request, result *Result) error {
	p := req.Pipeline
	if err := p.refresh(); err != nil {
		return pipelineError(AdmissionRejected, PreparePhase, err)
	}
	if err := pipelineBoundary(req); err != nil {
		return pipelineError(AdmissionRejected, PreparePhase, err)
	}
	sel := source.Selection{}
	var grant source.Grant
	if p.Spec.Source != nil {
		sel = *p.Spec.Source
		grant = p.Policy.Sources[sel.Ref]
	}
	var previous *PipelineFacts
	for i := len(s.record.Attempts) - 2; i >= 0; i-- {
		f := s.record.Attempts[i].Pipeline
		if f != nil && f.Prepared != nil {
			previous = f
			break
		}
	}
	if previous != nil {
		if contentBinding(sel) != contentBinding(previous.Prepared.Selection) {
			return pipelineError(AdmissionRejected, PreparePhase, errors.New("input selection changed; create a new task"))
		}
		if info, err := os.Lstat(req.Task.Worktree()); err != nil || !info.IsDir() {
			return pipelineError(AdmissionRejected, PreparePhase, errors.New("retained execution workspace is missing or invalid"))
		}
		scratch := filepath.Join(pipelineRoot(req), ".resume-"+result.AttemptID)
		if err := source.CheckResume(ctx, *previous.Prepared, grant, scratch, p.Policy.InputLimits); err != nil {
			return pipelineError(AdmissionRejected, PreparePhase, err)
		}
		baseline := filepath.Join(pipelineRoot(req), "inputs", previous.InputSnapshot)
		if previous.InputSnapshot == "" || filepath.Base(previous.InputSnapshot) != previous.InputSnapshot {
			return pipelineError(AdmissionRejected, PreparePhase, errors.New("input baseline requires reconciliation"))
		}
		if err := controlledDirectories(req.Task.Home, baseline); err != nil {
			return pipelineError(AdmissionRejected, PreparePhase, err)
		}
		m, err := artifact.Scan(ctx, baseline, []string{"."}, nil, p.Policy.InputLimits)
		if err != nil || m.Digest != previous.Prepared.Baseline.Digest {
			return pipelineError(AdmissionRejected, PreparePhase, errors.New("retained input baseline is missing or changed"))
		}
		s.facts.Prepared = previous.Prepared
		s.facts.InputSnapshot = previous.InputSnapshot
	} else {
		prepared, err := source.Prepare(ctx, sel, grant, req.Task.Worktree(), p.Policy.InputLimits)
		if err != nil {
			return pipelineError(EnvironmentFailed, PreparePhase, err)
		}
		s.facts.Prepared = &prepared
		baseline := filepath.Join(pipelineRoot(req), "inputs", result.AttemptID)
		m, err := artifact.Copy(ctx, req.Task.Worktree(), baseline, []string{"."}, nil, p.Policy.InputLimits)
		if err != nil {
			return pipelineError(EnvironmentFailed, PreparePhase, err)
		}
		if m.Digest != prepared.Baseline.Digest {
			return pipelineError(EnvironmentFailed, PreparePhase, errors.New("prepared input changed before baseline retention"))
		}
		s.facts.InputSnapshot = result.AttemptID
	}
	return s.checkpoint(ctx, result, PreparePhase, ProgressRunning)
}

func (s *Service) finishPipeline(ctx context.Context, req Request, result *Result) error {
	p := req.Pipeline
	if p.Spec.Outputs == nil {
		return nil
	}
	result.ArtifactState = ArtifactPending
	if err := s.checkpoint(ctx, result, CollectPhase, ProgressRunning); err != nil {
		return err
	}
	// A completed agent command alone does not prove that its descendants can
	// no longer modify bind mounts. Stop and re-inspect the runtime first.
	if err := s.driver.Stop(ctx, req); err != nil {
		return pipelineError(EnvironmentFailed, CollectPhase, err)
	}
	_, running, err := s.driver.Inspect(ctx, req)
	if err != nil {
		return pipelineError(EnvironmentFailed, CollectPhase, err)
	}
	if running {
		return pipelineError(EnvironmentFailed, CollectPhase, errors.New("runtime still has active writers"))
	}
	result.ContainerState = string(ContainerStopped)
	root := filepath.Join(pipelineRoot(req), "snapshots", result.AttemptID)
	all, err := artifact.Copy(ctx, req.Task.Worktree(), filepath.Join(root, "workspace"), []string{"."}, p.Policy.Exclude, p.Policy.ArtifactLimits)
	if err != nil {
		return pipelineError(EnvironmentFailed, CollectPhase, err)
	}
	if len(p.Spec.Outputs.Include) > 0 {
		if _, err := artifact.Copy(ctx, filepath.Join(root, "workspace"), filepath.Join(root, "outputs"), p.Spec.Outputs.Include, p.Policy.Exclude, p.Policy.ArtifactLimits); err != nil {
			return pipelineError(EnvironmentFailed, CollectPhase, err)
		}
	} else if err := os.Mkdir(filepath.Join(root, "outputs"), 0700); err != nil {
		return pipelineError(EnvironmentFailed, CollectPhase, err)
	}
	if p.Spec.Outputs.Patch {
		if err := s.collectPatch(ctx, req, root); err != nil {
			return pipelineError(EnvironmentFailed, CollectPhase, err)
		}
	}
	outputs, err := artifact.Scan(ctx, filepath.Join(root, "outputs"), []string{"."}, nil, p.Policy.ArtifactLimits)
	if err != nil {
		return pipelineError(EnvironmentFailed, CollectPhase, err)
	}
	if len(outputs.Files) == 0 && !p.Spec.Outputs.AllowEmpty {
		return pipelineError(VerificationFailed, CollectPhase, errors.New("selected outputs are empty"))
	}
	if err := errors.Join(syncDirectory(filepath.Join(root, "outputs")), syncDirectory(root)); err != nil {
		return pipelineError(EnvironmentFailed, CollectPhase, err)
	}
	ref := &ArtifactRef{ID: result.AttemptID, Manifest: outputs, WorkspaceManifest: all, Retention: "retained"}
	ref.Digest = contentBinding(struct{ Outputs, Workspace string }{outputs.Digest, all.Digest})
	s.facts.Artifact = ref
	result.ArtifactState = ArtifactCollected
	if err := s.checkpoint(ctx, result, CollectPhase, ProgressSucceeded); err != nil {
		return err
	}
	if p.Spec.Criteria == nil {
		result.ValidationState = "not_requested"
	} else {
		if err := s.checkpoint(ctx, result, EvaluatePhase, ProgressRunning); err != nil {
			return err
		}
		if err := p.refresh(); err != nil {
			return pipelineError(AdmissionRejected, EvaluatePhase, err)
		}
		validation, err := completion.EvaluateBundle(ctx, filepath.Join(root, "outputs"), filepath.Join(root, "workspace"), outputs, all, artifact.Filter(s.facts.Prepared.Baseline, p.Policy.Exclude), p.Spec.Criteria, p.Policy.Criteria, p.Verifier)
		if err != nil {
			return pipelineError(EnvironmentFailed, EvaluatePhase, err)
		}
		validation.ArtifactDigest = ref.Digest
		s.facts.Validation = &validation
		result.ValidationState = validation.State
		if err := s.verifyArtifact(ctx, req, *ref); err != nil {
			return pipelineError(EnvironmentFailed, EvaluatePhase, err)
		}
		progress := ProgressSucceeded
		if validation.State != "passed" {
			progress = ProgressFailed
		}
		if err := s.checkpoint(ctx, result, EvaluatePhase, progress); err != nil {
			return err
		}
		switch validation.State {
		case "failed":
			return pipelineError(VerificationFailed, EvaluatePhase, errors.New("completion requirements failed"))
		case "needs_review":
			return pipelineError(ReviewRequired, EvaluatePhase, errors.New("completion requires trusted review"))
		case "error":
			return pipelineError(EnvironmentFailed, EvaluatePhase, errors.New("completion verifier failed"))
		}
	}
	if p.Spec.Sink != nil {
		return s.deliverPipeline(ctx, req, result)
	}
	return nil
}

func (s *Service) normalizeArtifactState(result *Result) {
	if s.facts == nil || s.facts.Outputs == nil || s.facts.Artifact != nil {
		return
	}
	for _, stage := range result.Stages {
		if stage.Phase == CollectPhase && (stage.State == ProgressFailed || stage.State == ProgressUnknown) {
			result.ArtifactState = ArtifactFailed
		}
	}
}

func (s *Service) deliverPipeline(ctx context.Context, req Request, result *Result) error {
	p := req.Pipeline
	sel := p.Spec.Sink
	ref := s.facts.Artifact
	if sel == nil || ref == nil {
		return pipelineError(AdmissionRejected, DeliverPhase, errors.New("delivery requires a retained artifact"))
	}
	if err := p.refresh(); err != nil {
		return pipelineError(AdmissionRejected, DeliverPhase, err)
	}
	if err := pipelineBoundary(req); err != nil {
		return pipelineError(AdmissionRejected, DeliverPhase, err)
	}
	if p.Spec.Source != nil {
		binding, err := source.GrantBinding(p.Policy.Sources[p.Spec.Source.Ref])
		if err != nil || s.facts.Prepared == nil || binding != s.facts.Prepared.Binding {
			return pipelineError(AdmissionRejected, DeliverPhase, errors.New("source authorization changed since execution"))
		}
	}
	validation := s.facts.Validation
	if validation == nil || validation.ArtifactDigest != ref.Digest || validation.State != "passed" {
		return pipelineError(VerificationFailed, DeliverPhase, errors.New("delivery requires valid completion evidence"))
	}
	currentBinding, err := completion.Binding(p.Spec.Criteria, p.Policy.Criteria)
	if err != nil || currentBinding != validation.CriteriaDigest {
		return pipelineError(AdmissionRejected, DeliverPhase, errors.New("completion policy changed; verification must run again"))
	}
	if err := s.verifyArtifact(ctx, req, *ref); err != nil {
		return pipelineError(AdmissionRejected, DeliverPhase, err)
	}
	g, ok := p.Policy.Sinks[sel.TargetRef]
	if !ok {
		return pipelineError(AdmissionRejected, DeliverPhase, errors.New("delivery authorization was revoked"))
	}
	binding := sink.Binding(*sel, g)
	opIndex := -1
	wasUnresolved := false
	for i, op := range s.record.Operations {
		if op.Kind == "delivery" && op.Key == sel.DeliveryKey {
			if op.ObjectRef != ref.Digest || op.TargetRef != binding {
				return pipelineError(AdmissionRejected, DeliverPhase, errors.New("delivery_key is already bound to a different artifact or target"))
			}
			if op.State == "completed" {
				receipt, err := sink.Query(ctx, ref.Manifest, ref.Digest, *sel, g, p.Policy.ArtifactLimits)
				if err != nil || receipt.State != "delivered" {
					return pipelineError(ProductDeliveryFailed, DeliverPhase, errors.New("delivery already completed but target changed; use a new logical delivery instead of replaying it"))
				}
				s.facts.Receipt = &receipt
				result.ProductDeliveryState = "delivered"
				result.ArtifactState = ArtifactDelivered
				return nil
			}
			opIndex = i
			wasUnresolved = op.State == "intent" || op.State == "unknown"
			break
		}
	}
	if opIndex < 0 {
		s.record.Operations = append(s.record.Operations, OperationRecord{Key: sel.DeliveryKey, AttemptID: result.AttemptID, Kind: "delivery", State: "intent", ObjectRef: ref.Digest, TargetRef: binding})
		opIndex = len(s.record.Operations) - 1
	} else {
		s.record.Operations[opIndex].State = "intent"
	}
	result.ProductDeliveryState = "pending"
	if err := s.checkpoint(ctx, result, DeliverPhase, ProgressRunning); err != nil {
		return err
	}
	receipt, err := sink.Publish(ctx, filepath.Join(pipelineRoot(req), "snapshots", ref.ID, "outputs"), ref.Manifest, ref.Digest, *sel, g, p.Policy.ArtifactLimits)
	s.facts.Receipt = &receipt
	if err != nil {
		state := "failed"
		kind := ProductDeliveryFailed
		var outcome *sink.OutcomeError
		if wasUnresolved || errors.As(err, &outcome) && outcome.Unknown {
			state = "unknown"
			kind = ProductOutcomeUnknown
		}
		receipt.State = state
		s.record.Operations[opIndex].State = state
		result.ProductDeliveryState = state
		progress := ProgressFailed
		if state == "unknown" {
			progress = ProgressUnknown
		}
		if saveErr := s.checkpoint(ctx, result, DeliverPhase, progress); saveErr != nil {
			return errors.Join(pipelineError(kind, DeliverPhase, err), saveErr)
		}
		return pipelineError(kind, DeliverPhase, err)
	}
	s.record.Operations[opIndex].State = "completed"
	s.record.Operations[opIndex].ReceiptRef = receipt.ObjectRef
	result.ProductDeliveryState = "delivered"
	result.ArtifactState = ArtifactDelivered
	return s.checkpoint(ctx, result, DeliverPhase, ProgressSucceeded)
}

func (s *Service) verifyArtifact(ctx context.Context, req Request, ref ArtifactRef) error {
	if ref.Retention != "retained" {
		return errors.New("artifact is not retained")
	}
	for _, f := range append(append([]artifact.File(nil), ref.Manifest.Files...), ref.WorkspaceManifest.Files...) {
		if artifact.Reserved(f.Path) || artifact.Selected(req.Pipeline.Policy.Exclude, f.Path) {
			return errors.New("retained output is excluded by current policy")
		}
	}
	if ref.ID == "" || filepath.Base(ref.ID) != ref.ID {
		return ErrRecordInvalid
	}
	root := filepath.Join(pipelineRoot(req), "snapshots", ref.ID)
	if err := controlledDirectories(req.Task.Home, root); err != nil {
		return err
	}
	for _, entry := range []struct{ name, digest string }{{"workspace", ref.WorkspaceManifest.Digest}, {"outputs", ref.Manifest.Digest}} {
		m, err := artifact.Scan(ctx, filepath.Join(root, entry.name), []string{"."}, nil, req.Pipeline.Policy.ArtifactLimits)
		if err != nil {
			return err
		}
		if m.Digest != entry.digest {
			return errors.New("retained artifact content changed")
		}
	}
	return nil
}
