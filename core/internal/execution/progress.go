package execution

import (
	"fmt"
	"time"
)

// Phase is an internal lifecycle boundary, independent of the v1 event schema.
type Phase string

const (
	AdmissionPhase Phase = "admission"
	PreparePhase   Phase = "prepare"
	ExecutePhase   Phase = "execute"
	CollectPhase   Phase = "collect"
	EvaluatePhase  Phase = "evaluate"
	DeliverPhase   Phase = "deliver"
	FinalizePhase  Phase = "finalize"
)

type ProgressState string

const (
	ProgressPending   ProgressState = "pending"
	ProgressRunning   ProgressState = "running"
	ProgressSucceeded ProgressState = "succeeded"
	ProgressFailed    ProgressState = "failed"
	ProgressSkipped   ProgressState = "skipped"
	ProgressHandedOff ProgressState = "handed_off"
	ProgressUnknown   ProgressState = "unknown"
)

type StageProgress struct {
	Phase Phase         `json:"phase"`
	State ProgressState `json:"state"`
}

// Requirements guards the legacy execution path. General tasks select their
// implemented Source/artifact/validation/Sink adapters through PipelinePlan;
// legacy callers cannot opt into them implicitly with these flags.
type Requirements struct {
	SourceKind string
	Collect    bool
	Evaluate   bool
	Deliver    bool
	Tools      []string
}

func (r Requirements) check() error {
	if (r.SourceKind != "" && r.SourceKind != "git") || r.Collect || r.Evaluate || r.Deliver || len(r.Tools) != 0 {
		return fmt.Errorf("requested source, artifact, validation, delivery or tool capability is not implemented")
	}
	return nil
}

const CleanupTimeout = 15 * time.Second

func initialStages() []StageProgress {
	return []StageProgress{
		{AdmissionPhase, ProgressPending}, {PreparePhase, ProgressPending}, {ExecutePhase, ProgressPending},
		{CollectPhase, ProgressSkipped}, {EvaluatePhase, ProgressSkipped}, {DeliverPhase, ProgressSkipped},
		{FinalizePhase, ProgressPending},
	}
}

func (r *Result) setStage(phase Phase, state ProgressState) {
	for i := range r.Stages {
		if r.Stages[i].Phase == phase {
			r.Stages[i].State = state
			return
		}
	}
}

// Recovery describes what the recorded facts permit; it never replays a prompt
// or claims that a crashed process performed no external effects.
func (r TaskRecord) Recovery() string {
	for _, op := range r.Operations {
		if op.State == "intent" || op.State == "unknown" {
			return "reconcile_operations"
		}
	}
	for _, a := range r.Attempts {
		if a.Pipeline != nil && a.Pipeline.Prepared != nil && a.Pipeline.InputSnapshot == "" {
			return "reconcile_prepare"
		}
		if a.Pipeline != nil && a.Pipeline.Artifact != nil && a.Pipeline.Artifact.Retention == "pruning" {
			return "reconcile_retention"
		}
		for _, stage := range a.Result.Stages {
			if stage.Phase == PreparePhase && (stage.State == ProgressRunning || stage.State == ProgressUnknown) {
				return "reconcile_prepare"
			}
		}
		if a.Result.CleanupState == CleanupFailed || a.Result.CleanupState == CleanupPending {
			return "reconcile_cleanup"
		}
	}
	if len(r.Attempts) == 0 {
		return "not_recorded"
	}
	last := r.Attempts[len(r.Attempts)-1]
	if last.Result.State == OutcomeUnknown {
		return "reconcile_operations"
	}
	if r.SchemaVersion == GeneralRecordSchema && last.Pipeline != nil {
		if last.Pipeline.Artifact != nil && last.Pipeline.Artifact.Retention == "pruning" {
			return "reconcile_retention"
		}
		if last.Pipeline.Outputs != nil && last.Pipeline.Artifact == nil && last.Result.ExecutionState == Finished {
			return "continue_completion"
		}
		if last.Pipeline.Validation != nil && last.Pipeline.Validation.State == "needs_review" {
			return "continue_verification"
		}
		if last.Pipeline.Receipt != nil && last.Pipeline.Receipt.State != "delivered" {
			return "retry_delivery"
		}
		if last.Pipeline.Sink != nil && last.Pipeline.Artifact != nil && last.Pipeline.Validation != nil && last.Pipeline.Validation.State == "passed" && last.Result.ProductDeliveryState != "delivered" {
			return "retry_delivery"
		}
		for _, stage := range last.Result.Stages {
			if (stage.Phase == CollectPhase || stage.Phase == EvaluatePhase || stage.Phase == DeliverPhase) && stage.State == ProgressRunning {
				return "continue_completion"
			}
		}
	}
	if !last.Closed {
		return "inspect_runtime"
	}
	for _, stage := range last.Result.Stages {
		if stage.Phase == ExecutePhase && (stage.State == ProgressHandedOff || stage.State == ProgressRunning) {
			return "inspect_runtime"
		}
	}
	return "new_attempt_allowed"
}
