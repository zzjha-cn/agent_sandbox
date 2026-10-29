package execution

import (
	"context"
	"errors"

	"sandx/internal/artifact"
	"sandx/internal/completion"
	"sandx/internal/external"
)

type externalEvaluator struct {
	service *Service
	req     Request
	result  *Result
}

func (s *Service) externalAuthorize(req Request) external.Authorizer {
	return func(call external.Call, action string) (external.Grant, error) {
		if err := req.Pipeline.refresh(); err != nil {
			return external.Grant{}, err
		}
		if err := pipelineBoundary(req); err != nil {
			return external.Grant{}, err
		}
		g, ok := req.Pipeline.Policy.Tools[call.GrantRef]
		if !ok {
			return g, external.ErrDenied
		}
		return g, g.Authorize(call, action)
	}
}

func (s *Service) externalOperation(ctx context.Context, req Request, result *Result, call external.Call, queryOnly bool) (*external.Record, error) {
	key := external.Key(externalTaskKey(req), call.ID)
	index := -1
	var old *external.Record
	for i, op := range s.record.Operations {
		if op.Kind == "tool" && op.Key == key {
			index = i
			old = op.External
			if old == nil {
				return nil, ErrRecordInvalid
			}
			break
		}
	}
	if queryOnly && old == nil {
		return nil, errors.New("operation was never submitted; verification cannot create a write")
	}
	save := func(ctx context.Context, r *external.Record) error {
		state := "unknown"
		switch r.State {
		case "completed":
			state = "completed"
		case "rejected", "not_found":
			state = "failed"
		case "intent":
			state = "intent"
		}
		copy := *r
		op := OperationRecord{Key: key, AttemptID: result.AttemptID, Kind: "tool", State: state, ObjectRef: r.RequestDigest, TargetRef: r.GrantBinding, ReceiptRef: r.OperationID, External: &copy}
		if index < 0 {
			s.record.Operations = append(s.record.Operations, op)
			index = len(s.record.Operations) - 1
		} else {
			op.AttemptID = s.record.Operations[index].AttemptID
			s.record.Operations[index] = op
		}
		return s.save(ctx, *result, nil, false)
	}
	if queryOnly {
		return external.Reconcile(ctx, externalTaskKey(req), call, old, s.externalAuthorize(req), external.HTTP{}, save)
	}
	return external.Execute(ctx, externalTaskKey(req), call, old, s.externalAuthorize(req), external.HTTP{}, save)
}

func (s *Service) executeExternal(ctx context.Context, req Request, result *Result) error {
	for _, call := range req.Pipeline.Spec.Operations {
		r, err := s.externalOperation(ctx, req, result, call, false)
		if err != nil {
			if errors.Is(err, external.ErrUnknown) || r != nil && (r.State == "intent" || r.State == "unknown") {
				return pipelineError(ProductOutcomeUnknown, ExecutePhase, external.ErrUnknown)
			}
			return pipelineError(AdmissionRejected, ExecutePhase, err)
		}
		if r.State == "accepted" {
			return pipelineError(ProductOutcomeUnknown, ExecutePhase, errors.New("external request accepted; business result pending"))
		}
		if r.State != "completed" {
			return pipelineError(VerificationFailed, ExecutePhase, errors.New("external operation was not applied"))
		}
	}
	return nil
}

func (e externalEvaluator) VerifyExternal(ctx context.Context, rule completion.Rule) (completion.ExternalEvidence, error) {
	observed := completion.ExternalEvidence{State: "unknown", Reason: "operation_unresolved"}
	var call *external.Call
	for _, candidate := range e.req.Pipeline.Spec.Operations {
		if candidate.ID == rule.Operation {
			copy := candidate
			call = &copy
			break
		}
	}
	if call == nil {
		return observed, external.ErrDenied
	}
	r, err := e.service.externalOperation(ctx, e.req, e.result, *call, true)
	if r != nil {
		observed.OperationID = r.OperationID
		observed.ReceiptDigest = external.Digest(r.Receipt)
	}
	if err != nil {
		if errors.Is(err, external.ErrUnknown) {
			return observed, nil
		}
		return observed, err
	}
	if r.State == "rejected" {
		observed.State = "failed"
		observed.Reason = "operation_rejected"
		return observed, nil
	}
	if r.State != "completed" {
		observed.Reason = "request_accepted_business_pending"
		return observed, nil
	}
	g, err := e.service.externalAuthorize(e.req)(*call, "read")
	if err != nil {
		return observed, err
	}
	if g.Binding() != r.GrantBinding {
		return observed, external.ErrDenied
	}
	state, err := (external.HTTP{}).Read(ctx, g, *call)
	if err != nil {
		return observed, err
	}
	observed.StateDigest = external.Digest(state)
	observed.State = "failed"
	observed.Reason = "business_state_mismatch"
	if state.OperationID != r.OperationID {
		return observed, nil
	}
	for key, value := range rule.Expected {
		if got, ok := state.Value[key]; !ok || got != value {
			return observed, nil
		}
	}
	observed.State = "passed"
	observed.Reason = "receipt_and_business_state_confirmed"
	return observed, nil
}

// evaluateExternalOnly supplies completion evidence for tasks with no files.
func (s *Service) evaluateExternalOnly(ctx context.Context, req Request, result *Result) error {
	if err := s.checkpoint(ctx, result, EvaluatePhase, ProgressRunning); err != nil {
		return err
	}
	validation, err := completion.EvaluateBundle(ctx, "", "", artifact.Manifest{}, artifact.Manifest{}, artifact.Manifest{}, req.Pipeline.Spec.Criteria, req.Pipeline.Policy.Criteria, req.Pipeline.Verifier, externalEvaluator{s, req, result})
	if err != nil {
		return pipelineError(EnvironmentFailed, EvaluatePhase, err)
	}
	s.guardExternalCompletion(req, &validation)
	s.facts.Validation = &validation
	result.ValidationState = validation.State
	progress := ProgressSucceeded
	if validation.State != "passed" && validation.State != "not_requested" {
		progress = ProgressFailed
	}
	if err := s.checkpoint(ctx, result, EvaluatePhase, progress); err != nil {
		return err
	}
	switch validation.State {
	case "unknown":
		return pipelineError(ProductOutcomeUnknown, EvaluatePhase, external.ErrUnknown)
	case "failed":
		return pipelineError(VerificationFailed, EvaluatePhase, errors.New("external business state failed completion"))
	case "error":
		return pipelineError(EnvironmentFailed, EvaluatePhase, errors.New("external state verification failed"))
	}
	return nil
}

// All requested writes must be resolved, even when a criterion selects just one.
func (s *Service) guardExternalCompletion(req Request, validation *completion.Result) {
	for _, op := range s.record.Operations {
		if op.Kind == "tool" && (op.State == "intent" || op.State == "unknown") {
			validation.State = "unknown"
			return
		}
	}
	for _, call := range req.Pipeline.Spec.Operations {
		found := false
		for _, op := range s.record.Operations {
			if op.Kind == "tool" && op.Key == external.Key(externalTaskKey(req), call.ID) && op.ObjectRef == external.Digest(call) {
				found = true
				if op.State != "completed" && validation.State != "unknown" && validation.State != "error" {
					validation.State = "failed"
				}
				break
			}
		}
		if !found {
			validation.State = "unknown"
			return
		}
	}
}

func externalTaskKey(req Request) string {
	return contentBinding([]string{req.Task.Home, req.Task.ID()})
}
