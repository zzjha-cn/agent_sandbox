package protocol

import (
	"fmt"
	"reflect"
	"sandx/internal/execution"
	"sandx/internal/governance"
	"strings"
	"time"
)

type JSONSchema map[string]any

func SchemaNames() []string {
	return []string{"task", "event", "result", "task-status", "execution-status", "version", "admission", "retention", "team-event", "task-list", "task-maintenance"}
}

// Schema derives wire shapes from the actual CLI types. Input objects are
// closed; v2 output consumers must tolerate additive fields.
func Schema(name string) (JSONSchema, error) {
	var value any
	switch name {
	case "task-list":
		value = TaskList{}
	case "task-maintenance":
		value = TaskMaintenance{}
	case "team-event":
		value = governance.TeamEvent{}
	case "task":
		value = execution.TaskSpec{}
	case "event":
		value = Event{}
	case "result":
		value = execution.Result{}
	case "task-status", "execution-status":
		value = Status{}
	case "version":
		value = VersionInfo{}
	case "admission":
		value = Admission{}
	case "retention":
		value = Retention{}
	default:
		return nil, fmt.Errorf("unknown schema %q; use sbx schema to list names", name)
	}
	s := shape(reflect.TypeOf(value), name == "task")
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["$id"] = "urn:sbx:schema:" + name + ":v2"
	s["title"] = "sbx " + name
	switch name {
	case "task-list":
		s["$id"] = "urn:sbx:schema:task-list:v1"
		prop(s, "schema_version")["const"] = TaskListSchema
	case "task-maintenance":
		s["$id"] = "urn:sbx:schema:task-maintenance:v1"
		prop(s, "schema_version")["const"] = TaskMaintenanceSchema
		prop(s, "action")["enum"] = []string{"stop", "clean", "recover"}
	case "team-event":
		s["$id"] = "urn:sbx:schema:team-event:v1"
		s["additionalProperties"] = false
		prop(s, "schema_version")["const"] = governance.TeamEventSchema
		prop(s, "id")["pattern"] = "^[0-9a-f]{32}$"
		for _, key := range []string{"subject", "policy_digest", "task_key", "attempt_key"} {
			prop(s, key)["pattern"] = "^[0-9a-f]{64}$"
		}
		prop(s, "organization")["minLength"] = 1
		prop(s, "organization")["maxLength"] = 80
		prop(s, "type")["enum"] = []string{"admitted", "phase_started", "phase_finished", "agent_started", "agent_exited", "completed", "failed"}
		prop(s, "phase")["enum"] = []string{"admission", "prepare", "execute", "collect", "evaluate", "deliver", "finalize"}
		prop(s, "outcome")["enum"] = []string{"ready", "running", "finished", "agent_failed", "timed_out", "cancelled", "delivery_failed", "admission_rejected", "environment_failed", "agent_start_failed", "internal_failed", "verification_failed", "needs_review", "product_delivery_failed", "product_outcome_unknown"}
	case "task":
		taskRules(s)
	case "event":
		full := s
		delete(full, "$schema")
		delete(full, "$id")
		delete(full, "title")
		prop(full, "schema_version")["const"] = SchemaVersion
		prop(full, "type")["enum"] = []string{"admitted", "phase_started", "phase_finished", "agent_started", "agent_exited", "completed", "failed"}
		failure := shape(reflect.TypeFor[Failure](), false)
		prop(failure, "schema_version")["const"] = SchemaVersion
		prop(failure, "type")["const"] = "failed"
		// Ensure full failed events cannot also match the early-failure branch.
		failure["not"] = JSONSchema{"required": []string{"attempt_id"}}
		s = JSONSchema{"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "urn:sbx:schema:event:v2", "title": "sbx event", "oneOf": []any{full, failure}}
	case "task-status":
		prop(s, "schema_version")["const"] = TaskStatusSchema
	case "execution-status":
		prop(s, "schema_version")["const"] = ExecutionStatusSchema
	case "version", "admission":
		prop(s, "schema_version")["const"] = SchemaVersion
	case "retention":
		s["$id"] = "urn:sbx:schema:retention:v1"
		prop(s, "schema_version")["const"] = "sbx.artifact-retention/v1"
	}
	if name == "admission" {
		prop(s, "type")["const"] = "admitted"
		prop(s, "admitted")["const"] = true
	}
	return s, nil
}

func shape(t reflect.Type, input bool) JSONSchema {
	if t.Kind() == reflect.Pointer {
		return shape(t.Elem(), input)
	}
	if t == reflect.TypeFor[time.Time]() {
		return JSONSchema{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Struct:
		p := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			if f.PkgPath != "" || tag[0] == "-" {
				continue
			}
			if f.Anonymous && tag[0] == "" {
				embedded := shape(f.Type, input)
				for k, v := range embedded["properties"].(map[string]any) {
					p[k] = v
				}
				required = append(required, embedded["required"].([]string)...)
				continue
			}
			name := tag[0]
			if name == "" {
				name = f.Name
			}
			p[name] = shape(f.Type, input)
			if !input && !strings.Contains(f.Tag.Get("json"), ",omitempty") {
				required = append(required, name)
			}
		}
		return JSONSchema{"type": "object", "properties": p, "required": required, "additionalProperties": !input}
	case reflect.Slice, reflect.Array:
		var kind any = "array"
		if !input && t.Kind() == reflect.Slice {
			kind = []string{"array", "null"}
		}
		return JSONSchema{"type": kind, "items": shape(t.Elem(), input)}
	case reflect.Map:
		var kind any = "object"
		if !input {
			kind = []string{"object", "null"}
		}
		return JSONSchema{"type": kind, "additionalProperties": shape(t.Elem(), input)}
	case reflect.String:
		return JSONSchema{"type": "string"}
	case reflect.Bool:
		return JSONSchema{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return JSONSchema{"type": "integer"}
	default:
		panic("unsupported schema type: " + t.String())
	}
}
func prop(s JSONSchema, name string) JSONSchema {
	return s["properties"].(map[string]any)[name].(JSONSchema)
}
func item(s JSONSchema) JSONSchema          { return s["items"].(JSONSchema) }
func nonempty(s JSONSchema)                 { s["minLength"] = 1 }
func require(s JSONSchema, names ...string) { s["required"] = names }
func taskRules(s JSONSchema) {
	require(s, "schema_version", "task_id", "prompt", "agent")
	prop(s, "schema_version")["const"] = SchemaVersion
	prop(s, "task_id")["pattern"] = `^[a-z0-9][a-z0-9-]{0,39}$`
	prop(s, "prompt")["pattern"] = `\S`
	agent := prop(s, "agent")
	require(agent, "provider")
	prop(agent, "provider")["enum"] = []string{"claude", "codex"}
	timeout := prop(prop(s, "limits"), "timeout_seconds")
	timeout["minimum"] = 0
	timeout["maximum"] = 86400
	source := prop(s, "source")
	require(source, "kind", "ref")
	prop(source, "kind")["enum"] = []string{"files", "git"}
	nonempty(prop(source, "ref"))
	source["allOf"] = []any{
		JSONSchema{"if": JSONSchema{"properties": JSONSchema{"kind": JSONSchema{"const": "files"}}}, "then": JSONSchema{"properties": JSONSchema{"revision": JSONSchema{"const": ""}, "working_tree": JSONSchema{"const": false}, "untracked": JSONSchema{"const": false}}}},
		JSONSchema{"if": JSONSchema{"properties": JSONSchema{"untracked": JSONSchema{"const": true}}, "required": []string{"untracked"}}, "then": JSONSchema{"properties": JSONSchema{"working_tree": JSONSchema{"const": true}}, "required": []string{"working_tree"}}},
	}
	outputs := prop(s, "outputs")
	outputs["description"] = "Safe relative artifact patterns; runtime validates path/glob semantics and policy limits."
	outputs["anyOf"] = []any{JSONSchema{"required": []string{"include"}, "properties": JSONSchema{"include": JSONSchema{"minItems": 1}}}, JSONSchema{"required": []string{"patch"}, "properties": JSONSchema{"patch": JSONSchema{"const": true}}}}
	criteria := item(prop(s, "criteria"))
	require(criteria, "id", "kind", "ref")
	nonempty(prop(criteria, "id"))
	nonempty(prop(criteria, "ref"))
	prop(criteria, "kind")["enum"] = []string{"files", "tests", "manual", "external_state"}
	sink := prop(s, "sink")
	require(sink, "kind", "target_ref", "delivery_key")
	prop(sink, "kind")["enum"] = []string{"local_directory", "git_local"}
	nonempty(prop(sink, "target_ref"))
	prop(sink, "delivery_key")["pattern"] = `^[a-z0-9][a-z0-9-]{0,39}$`
	prop(sink, "conflict")["enum"] = []string{"", "reject", "replace"}
	safe := `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`
	tools := prop(s, "tools")
	tools["maxItems"] = 16
	t := item(tools)
	require(t, "id", "kind", "grant_ref", "targets", "fields")
	prop(t, "id")["pattern"] = `^[a-zA-Z][a-zA-Z0-9_]{0,47}$`
	prop(t, "kind")["const"] = "records.get"
	prop(t, "grant_ref")["pattern"] = safe
	for _, n := range []string{"targets", "fields"} {
		a := prop(t, n)
		a["minItems"] = 1
		a["maxItems"] = 100
		a["uniqueItems"] = true
		item(a)["pattern"] = safe
	}
	ops := prop(s, "operations")
	ops["maxItems"] = 32
	o := item(ops)
	require(o, "id", "grant_ref", "target", "value")
	for _, n := range []string{"id", "grant_ref", "target"} {
		prop(o, n)["pattern"] = safe
	}
	v := prop(o, "value")
	v["minProperties"] = 1
	v["maxProperties"] = 100
	v["propertyNames"] = JSONSchema{"pattern": safe}
	s["allOf"] = []any{
		JSONSchema{"if": JSONSchema{"required": []string{"sink"}}, "then": JSONSchema{"required": []string{"outputs"}}},
		JSONSchema{"if": JSONSchema{"required": []string{"criteria"}, "properties": JSONSchema{"criteria": JSONSchema{"contains": JSONSchema{"properties": JSONSchema{"kind": JSONSchema{"enum": []string{"files", "tests", "manual"}}}}}}}, "then": JSONSchema{"required": []string{"outputs"}}},
	}
	s["$comment"] = "Structural client validation only. Runtime additionally rejects duplicate JSON keys, non-UTF-8, >1 MiB input, duplicate logical ids, invalid artifact patterns and operation values >64 KiB; it validates registered grants, provider versions and runtime policy. Schema validity never grants authority. Omit optional fields rather than null."
}
