package protocol

import (
	"sandx/internal/execution"
	"sandx/internal/toolgate"
)

const TaskStatusSchema = "sbx.task-status/v2"
const ExecutionStatusSchema = "sbx.execution-status/v2"

// RecoveryAction is guidance, never permission or an automatic retry instruction.
// Commands contain placeholders so consumers need not interpolate untrusted text.
type RecoveryAction struct {
	Action       string `json:"action"`
	Command      string `json:"command,omitempty"`
	RequiresSpec bool   `json:"requires_spec"`
	Reason       string `json:"reason"`
}

type Status struct {
	SchemaVersion string                `json:"schema_version"`
	Recorded      bool                  `json:"recorded"`
	Recovery      string                `json:"recovery"`
	Record        *execution.TaskRecord `json:"record,omitempty"`
	ToolCalls     []toolgate.Audit      `json:"tool_calls,omitempty"`
	NextActions   []RecoveryAction      `json:"next_actions"`
}

func (s *Status) SetRecovery(recovery string) {
	s.Recovery = recovery
	a := RecoveryAction{Action: "inspect", Reason: "Inspect recorded facts before changing task state."}
	general := s.SchemaVersion == TaskStatusSchema
	switch recovery {
	case "reconcile_operations":
		a = RecoveryAction{Action: "reconcile", Command: "sbx task reconcile --spec <task.json>", RequiresSpec: true, Reason: "Query receipts and business state first. Unknown operations must not be resubmitted."}
	case "continue_verification":
		a = RecoveryAction{Action: "review", Command: "sbx task approve --spec <task.json> --reviewer <identity>", RequiresSpec: true, Reason: "Inspect immutable artifact and evidence, then explicitly approve or reject; verify alone does not grant approval."}
	case "retry_delivery":
		a = RecoveryAction{Action: "deliver", Command: "sbx task deliver --spec <task.json>", RequiresSpec: true, Reason: "Reauthorize and reconcile the existing delivery receipt; do not rerun the agent."}
	case "continue_completion":
		a = RecoveryAction{Action: "verify", Command: "sbx task verify --spec <task.json>", RequiresSpec: true, Reason: "Continue collection and verification from recorded facts without replaying the prompt."}
	case "record_invalid":
		a.Reason = "Preserve the home and original binary. Inspect version, integrity and identity; never delete records to bypass recovery gates."
	case "reconcile_cleanup":
		a.Reason = "Inspect owned runtime resources before reconciling cleanup."
		if general {
			a.Action = "recover"
			a.Command = "sbx task recover <task>"
			a.Reason = "Independently stop and inspect owned task containers, then reconcile the recorded cleanup failure without replaying effects."
		}
	case "reconcile_prepare", "reconcile_retention":
		a.Reason = "Inspect owned runtime resources and retained inputs/artifacts; manual reconciliation is required before a new attempt."
	case "inspect_runtime":
		a.Reason = "Inspect the running or interrupted session before choosing attach, stop or a new attempt."
		if general {
			a.Action = "recover"
			a.Command = "sbx task recover <task>"
			a.Reason = "Stop orphaned task containers and reconcile the interrupted record; a live supervisor must first be cancelled with sbx task stop <task>."
		}
	case "not_recorded", "new_attempt_allowed":
		a.Action = "run"
		a.Reason = "No recorded recovery gate blocks a new attempt; admission and current policy must still pass."
		if general {
			a.Command = "sbx task run --spec <task.json>"
			a.RequiresSpec = true
		} else {
			a.Command = "sbx run <task>"
		}
	}
	// Git convenience tasks never infer a general TaskSpec or authority.
	if !general && a.RequiresSpec {
		a.Command = ""
		a.RequiresSpec = false
		a.Action = "inspect"
	}
	s.NextActions = []RecoveryAction{a}
}

type Failure struct {
	SchemaVersion string `json:"schema_version"`
	Type          string `json:"type"`
	ErrorCode     string `json:"error_code"`
}
type Admission struct {
	SchemaVersion string `json:"schema_version"`
	Type          string `json:"type"`
	TaskID        string `json:"task_id"`
	Admitted      bool   `json:"admitted"`
	Existing      bool   `json:"existing"`
	Running       bool   `json:"running"`
}
type Retention struct {
	SchemaVersion string `json:"schema_version"`
	State         string `json:"state"`
}

const TaskListSchema = "sbx.task-list/v1"
const TaskMaintenanceSchema = "sbx.task-maintenance/v1"

type TaskList struct {
	SchemaVersion string                  `json:"schema_version"`
	Tasks         []execution.TaskSummary `json:"tasks"`
}
type TaskMaintenance struct {
	SchemaVersion string `json:"schema_version"`
	TaskID        string `json:"task_id"`
	Action        string `json:"action"`
	Recovery      string `json:"recovery"`
}
