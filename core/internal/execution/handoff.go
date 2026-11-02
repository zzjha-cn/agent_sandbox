package execution

import (
	"context"
	"errors"
	"sandx/internal/config"
	"sandx/internal/projectmemory"
	"sandx/internal/task"
	"sandx/internal/taskmemory"
)

func projectHandoff(r *TaskRecord, attempt string) (taskmemory.Handoff, error) {
	if r == nil || len(r.Attempts) == 0 {
		return taskmemory.Handoff{}, errors.New("task has no durable execution record")
	}
	a := r.Attempts[len(r.Attempts)-1]
	if attempt != "" {
		found := false
		for _, v := range r.Attempts {
			if v.Result.AttemptID == attempt {
				a = v
				found = true
				break
			}
		}
		if !found {
			return taskmemory.Handoff{}, errors.New("attempt not found")
		}
	}
	v := a.Result
	h := taskmemory.Handoff{Schema: taskmemory.HandoffSchema, TaskKey: r.TaskKey, Attempt: v.AttemptID, Sequence: r.Sequence, UpdatedAt: a.UpdatedAt, Closed: a.Closed, Incomplete: !a.Closed, Recovery: r.Recovery(), State: string(v.State), ExecutionState: string(v.ExecutionState), AgentExitCode: v.AgentExitCode, ValidationState: v.ValidationState, DeliveryState: v.ProductDeliveryState, Stages: []taskmemory.Stage{}, Validation: []taskmemory.Check{}, Operations: []taskmemory.Operation{}}
	for _, s := range v.Stages {
		h.Stages = append(h.Stages, taskmemory.Stage{Phase: string(s.Phase), State: string(s.State)})
		if s.State == ProgressRunning || s.State == ProgressUnknown || s.State == ProgressHandedOff {
			h.Incomplete = true
		}
	}
	if p := a.Pipeline; p != nil {
		h.InputDigest = p.InputSnapshot
		if p.Artifact != nil {
			h.ArtifactDigest = p.Artifact.Digest
		}
		if p.Receipt != nil {
			h.ReceiptDigest = projectmemory.Digest(p.Receipt)
		}
		if p.Validation != nil {
			for _, c := range p.Validation.Evidence {
				h.Validation = append(h.Validation, taskmemory.Check{ID: c.ID, Kind: c.Kind, State: c.State, ExitCode: c.ExitCode})
			}
		}
	}
	// All unresolved operations across attempts remain relevant; do not copy
	// request/receipt bodies, credential locations or free-form target references.
	for _, op := range r.Operations {
		if op.AttemptID == h.Attempt || op.State == "unknown" || op.State == "intent" {
			h.Operations = append(h.Operations, taskmemory.Operation{ID: op.Key, Kind: op.Kind, State: op.State})
		}
	}
	return taskmemory.SealHandoff(h), nil
}

// RefreshHandoff reconstructs from authoritative records even after SIGKILL or
// task cleanup. It never checks or changes a live runtime or external operation.
func RefreshHandoff(t task.Task, attempt string) (taskmemory.Handoff, error) {
	s, e := NewFileStore(t)
	if e != nil {
		return taskmemory.Handoff{}, e
	}
	r, e := s.Load()
	if e != nil {
		return taskmemory.Handoff{}, e
	}
	h, e := projectHandoff(r, attempt)
	if e != nil {
		return h, e
	}
	h, e = taskmemory.WithSummary(t.Home, h)
	if e != nil {
		return h, e
	}
	return h, taskmemory.SaveHandoff(t.Home, h)
}

type extractionModel struct {
	cfg      config.Config
	provider string
}

func (m extractionModel) Generate(ctx context.Context, j taskmemory.Job) ([]byte, error) {
	ag := m.cfg.Agents[m.provider]
	key, e := taskmemory.ResolveKey(ag.APIKeyEnv, ag.APIKeyFile)
	if e != nil {
		return nil, e
	}
	return (taskmemory.HTTPModel{APIKey: key}).Generate(ctx, j)
}

// autoExtract is independent post-processing: neither failure nor cancellation
// changes the terminal business result, events or operation records.
func autoExtract(ctx context.Context, req Request, result Result, model taskmemory.Model) {
	if EffectiveMemoryMode(req) != "managed" || !req.Config.Memory.Extract.Auto || req.OrganizationPolicyDigest != "" || req.Mode != Headless || result.Next == Detached || result.Next == Attach {
		return
	}
	processing := taskmemory.Processing{TaskKey: req.Task.ID(), Attempt: result.AttemptID, State: "pending"}
	_ = taskmemory.SaveProcessing(req.Task.Home, processing)
	fail := func(code string) {
		processing.State = "failed"
		processing.Error = code
		_ = taskmemory.SaveProcessing(req.Task.Home, processing)
	}
	h, e := RefreshHandoff(req.Task, result.AttemptID)
	if e != nil {
		fail("handoff_projection_failed")
		return
	}
	if !h.Closed {
		fail("attempt_not_closed")
		return
	}
	s := projectmemory.Store{Home: req.Task.Home}
	id, e := s.Resolve(req.Config.Memory.Project, req.Task.WS.Root)
	if e != nil {
		fail("project_resolution_failed")
		return
	}
	ec := req.Config.Memory.Extract
	jobs := taskmemory.Jobs{Home: req.Task.Home}
	material := taskmemory.Material{Handoff: h}
	if h.OperatorSummary != nil {
		material.Summary = *h.OperatorSummary
	}
	j, e := jobs.Create(id, req.Config.DefaultAgent, ec.Model, material, taskmemory.Limits{TimeoutSeconds: ec.TimeoutSeconds, MaxOutputTokens: ec.MaxOutputTokens, MaxCalls: ec.MaxCalls})
	if e != nil {
		fail("job_creation_failed")
		return
	}
	processing.Job = j.ID
	processing.State = "running"
	_ = taskmemory.SaveProcessing(req.Task.Home, processing)
	if model == nil {
		model = extractionModel{cfg: req.Config, provider: j.Provider}
	}
	j, e = jobs.Run(context.WithoutCancel(ctx), j.ID, model, false)
	processing.State = j.State
	processing.Error = j.Error
	if e != nil && processing.Error == "" {
		processing.State = "failed"
		processing.Error = "extraction_persistence_failed"
	}
	_ = taskmemory.SaveProcessing(req.Task.Home, processing)
}

// WithExtractionModel supplies a text-only model adapter for embedded callers
// and deterministic acceptance tests; TaskSpec cannot set it.
func (s *Service) WithExtractionModel(model taskmemory.Model) *Service {
	s.extractionModel = model
	return s
}
