// Package toolgate mediates read-only provider tools outside the sandbox.
package toolgate

import (
	"context"
	"errors"
	"regexp"
	"sandx/internal/external"
	"slices"
	"time"
)

var safeName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,47}$`)
var safeTarget = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

type Selection struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	GrantRef string   `json:"grant_ref"`
	Targets  []string `json:"targets"`
	Fields   []string `json:"fields"`
}

func (s Selection) Validate() error {
	if !safeName.MatchString(s.ID) || s.Kind != "records.get" || !safeTarget.MatchString(s.GrantRef) || len(s.Targets) == 0 || len(s.Targets) > 100 || len(s.Fields) == 0 || len(s.Fields) > 100 {
		return errors.New("tools require a safe id, records.get, grant_ref, targets and fields")
	}
	seen := map[string]bool{}
	for _, target := range s.Targets {
		if !safeTarget.MatchString(target) || seen[target] {
			return errors.New("invalid or duplicate tool target")
		}
		seen[target] = true
	}
	seen = map[string]bool{}
	for _, field := range s.Fields {
		if !safeTarget.MatchString(field) || seen[field] {
			return errors.New("invalid or duplicate tool field")
		}
		seen[field] = true
	}
	return nil
}
func (s Selection) Authorize(g external.Grant, target string) error {
	if s.Validate() != nil || !slices.Contains(s.Targets, target) {
		return external.ErrDenied
	}
	for _, t := range s.Targets {
		if !slices.Contains(g.Targets, t) {
			return external.ErrDenied
		}
	}
	for _, f := range s.Fields {
		if !slices.Contains(g.ReadFields, f) {
			return external.ErrDenied
		}
	}
	return g.Authorize(external.Call{Target: target}, "read")
}

type Policy struct {
	// Defaults support a host gateway reachable from Docker Desktop/Linux bridge.
	Listen   string `toml:"listen"`
	Host     string `toml:"host"`
	MaxCalls int    `toml:"max_calls"`
}

func (p Policy) Defaults() Policy {
	if p.Listen == "" {
		p.Listen = "0.0.0.0:0"
	}
	if p.Host == "" {
		p.Host = "host.docker.internal"
	}
	if p.MaxCalls == 0 {
		p.MaxCalls = 128
	}
	return p
}

type Audit struct {
	TaskID          string    `json:"task_id"`
	AttemptID       string    `json:"attempt_id"`
	CallID          string    `json:"call_id"`
	ToolRef         string    `json:"tool_ref"`
	GrantRef        string    `json:"grant_ref"`
	Target          string    `json:"target,omitempty"`
	Action          string    `json:"action"`
	Authorization   string    `json:"authorization"`
	State           string    `json:"state"`
	Reason          string    `json:"reason,omitempty"`
	GrantBinding    string    `json:"grant_binding,omitempty"`
	SelectionDigest string    `json:"selection_digest,omitempty"`
	ResponseDigest  string    `json:"response_digest,omitempty"`
	ObservedAt      time.Time `json:"observed_at"`
}

type Authorizer func(Selection, string) (external.Grant, error)
type Reader interface {
	Read(context.Context, external.Grant, external.Call) (external.State, error)
}

type Observation struct {
	SourceRef  string            `json:"source_ref"`
	Target     string            `json:"target"`
	ObservedAt time.Time         `json:"observed_at"`
	Value      map[string]string `json:"value"`
	Digest     string            `json:"digest"`
	Untrusted  bool              `json:"untrusted"`
}
