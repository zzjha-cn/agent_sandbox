// Package external implements authorized, journaled external operations. It
// never treats transport failure as proof that a write did not happen.
package external

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"
)

var ErrUnknown = errors.New("external operation outcome is unknown; reconcile before retry")
var ErrDenied = errors.New("external operation is not authorized")
var name = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

// Capability describes connector behavior, not permission. The first connector
// supports idempotent writes only; non-idempotent writes require another adapter.
type Capability struct {
	Mode   string // read_only, idempotent_write, non_idempotent_write
	Lookup bool
}

type Call struct {
	ID       string            `json:"id"`
	GrantRef string            `json:"grant_ref"`
	Target   string            `json:"target"`
	Value    map[string]string `json:"value"`
}

func (c Call) Validate() error {
	if !name.MatchString(c.ID) || !name.MatchString(c.GrantRef) || !name.MatchString(c.Target) || len(c.Value) == 0 || len(c.Value) > 100 {
		return errors.New("operation requires safe id, grant_ref, target and 1..100 value fields")
	}
	b, err := json.Marshal(c.Value)
	if err != nil || len(b) > 64<<10 {
		return errors.New("operation value exceeds 64 KiB")
	}
	for k := range c.Value {
		if !name.MatchString(k) {
			return errors.New("invalid operation field")
		}
	}
	return nil
}

// Grant is operator-owned. Task input cannot supply endpoints or credentials.
type Grant struct {
	Connector        string    `toml:"connector" json:"connector"`
	Endpoint         string    `toml:"endpoint" json:"endpoint"`
	CredentialFile   string    `toml:"credential_file" json:"credential_file"`
	Targets          []string  `toml:"targets" json:"targets"`
	ReadFields       []string  `toml:"read_fields" json:"read_fields,omitempty"`
	Fields           []string  `toml:"fields" json:"fields"`
	Actions          []string  `toml:"actions" json:"actions"`
	ExpiresAt        time.Time `toml:"expires_at" json:"expires_at"`
	Revoked          bool      `toml:"revoked" json:"revoked"`
	TimeoutSeconds   int       `toml:"timeout_seconds" json:"timeout_seconds"`
	MaxResponseBytes int64     `toml:"max_response_bytes" json:"max_response_bytes"`
}

func (g Grant) Authorize(c Call, action string) error {
	if g.Connector != "record_http" || g.Revoked || g.ExpiresAt.IsZero() || !time.Now().Before(g.ExpiresAt) || !slices.Contains(g.Actions, action) || !slices.Contains(g.Targets, c.Target) || g.TimeoutSeconds < 1 || g.TimeoutSeconds > 300 || g.MaxResponseBytes < 1 || g.MaxResponseBytes > 1<<20 {
		return ErrDenied
	}
	if action == "write" {
		for key := range c.Value {
			if !slices.Contains(g.Fields, key) {
				return ErrDenied
			}
		}
	}
	return validateEndpoint(g)
}

// Binding excludes expiry/actions so an operator can renew or grant read-only
// reconciliation after revoking writes, without redirecting an old operation.
func (g Grant) Binding() string {
	return Digest(struct{ Connector, Endpoint, CredentialFile string }{g.Connector, g.Endpoint, g.CredentialFile})
}
func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Key(task, id string) string { return Digest([]string{"sbx.external/v1", task, id}) }

type Receipt struct {
	OperationID   string `json:"operation_id"`
	Target        string `json:"target"`
	RequestDigest string `json:"request_digest"`
	State         string `json:"state"` // accepted is not completed business work
}

type Record struct {
	CallID        string    `json:"call_id"`
	GrantRef      string    `json:"grant_ref"`
	Target        string    `json:"target"`
	OperationID   string    `json:"operation_id"`
	RequestDigest string    `json:"request_digest"`
	GrantBinding  string    `json:"grant_binding"`
	State         string    `json:"state"` // intent/unknown/accepted/completed/rejected/not_found
	Receipt       *Receipt  `json:"receipt,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
}

type State struct {
	OperationID string            `json:"operation_id"`
	Value       map[string]string `json:"value"`
}

type Connector interface {
	Capability() Capability
	Submit(context.Context, Grant, Call, string, string) (Receipt, error)
	Lookup(context.Context, Grant, Call, string) (*Receipt, error)
	Read(context.Context, Grant, Call) (State, error)
}

type Authorizer func(Call, string) (Grant, error)
type Save func(context.Context, *Record) error

// Execute writes an intent before sending, then saves even under cancellation.
// An existing record is query-only. Repeating a task never repeats a write.
func Execute(ctx context.Context, task string, call Call, old *Record, auth Authorizer, connector Connector, save Save) (*Record, error) {
	if err := call.Validate(); err != nil {
		return old, err
	}
	if old != nil && old.State == "not_found" {
		// This state is written only when submission was never entered. A durable
		// intent left by a crash never takes this path.
		if old.OperationID != Key(task, call.ID) || old.RequestDigest != Digest(call) || old.GrantRef != call.GrantRef || old.Target != call.Target {
			return old, errors.New("operation key cannot be rebound")
		}
		g, err := auth(call, "write")
		if err != nil {
			return old, err
		}
		if g.Binding() != old.GrantBinding {
			return old, ErrDenied
		}
		old = nil
	}
	if old != nil {
		return Reconcile(ctx, task, call, old, auth, connector, save)
	}
	g, err := auth(call, "write")
	if err != nil {
		return nil, err
	}
	cap := connector.Capability()
	if cap.Mode != "idempotent_write" && cap.Mode != "non_idempotent_write" {
		return nil, ErrDenied
	}
	r := &Record{CallID: call.ID, GrantRef: call.GrantRef, Target: call.Target, OperationID: Key(task, call.ID), RequestDigest: Digest(call), GrantBinding: g.Binding(), State: "intent", ObservedAt: time.Now().UTC()}
	if err := save(ctx, r); err != nil {
		return r, err
	}
	// Recheck immediately at submission; revocation never erases a prior intent.
	g, err = auth(call, "write")
	if err == nil && g.Binding() != r.GrantBinding {
		err = ErrDenied
	}
	if err != nil {
		r.State = "not_found"
		return r, errors.Join(err, persist(ctx, r, save))
	}
	if err = ctx.Err(); err != nil {
		r.State = "not_found"
		return r, errors.Join(err, persist(ctx, r, save))
	}
	receipt, err := connector.Submit(ctx, g, call, r.OperationID, r.RequestDigest)
	if err != nil {
		r.State = "unknown"
		return r, errors.Join(ErrUnknown, persist(ctx, r, save))
	}
	if err = accept(r, receipt); err != nil {
		r.State = "unknown"
		return r, errors.Join(ErrUnknown, persist(ctx, r, save))
	}
	if err := persist(ctx, r, save); err != nil {
		return r, errors.Join(ErrUnknown, err)
	}
	return r, nil
}

func Reconcile(ctx context.Context, task string, call Call, r *Record, auth Authorizer, connector Connector, save Save) (*Record, error) {
	if r.OperationID != Key(task, call.ID) || r.RequestDigest != Digest(call) || r.GrantRef != call.GrantRef || r.Target != call.Target {
		return r, errors.New("operation key cannot be rebound")
	}
	g, err := auth(call, "lookup")
	if err != nil {
		return r, err
	}
	if g.Binding() != r.GrantBinding {
		return r, ErrDenied
	}
	if !connector.Capability().Lookup {
		r.State = "unknown"
		return r, errors.Join(ErrUnknown, persist(ctx, r, save))
	}
	receipt, err := connector.Lookup(ctx, g, call, r.OperationID)
	if err != nil {
		r.State = "unknown"
		return r, errors.Join(ErrUnknown, persist(ctx, r, save))
	}
	if receipt == nil {
		// Absence after a timed-out request may race an in-flight commit. It is
		// evidence of absence now, never permission to automatically resubmit.
		r.State = "unknown"
		return r, errors.Join(ErrUnknown, persist(ctx, r, save))
	}
	if err := accept(r, *receipt); err != nil {
		r.State = "unknown"
		return r, errors.Join(ErrUnknown, persist(ctx, r, save))
	}
	if err := persist(ctx, r, save); err != nil {
		return r, errors.Join(ErrUnknown, err)
	}
	return r, nil
}

func accept(r *Record, receipt Receipt) error {
	if receipt.OperationID != r.OperationID || receipt.Target != r.Target || receipt.RequestDigest != r.RequestDigest {
		return errors.New("receipt binding mismatch")
	}
	if r.Receipt != nil && ((r.Receipt.State == "applied" && receipt.State != "applied") || (r.Receipt.State == "rejected" && receipt.State != "rejected")) {
		return errors.New("receipt regressed")
	}
	switch receipt.State {
	case "accepted":
		r.State = "accepted"
	case "applied":
		r.State = "completed"
	case "rejected":
		r.State = "rejected"
	default:
		return errors.New("unsupported receipt state")
	}
	r.Receipt = &receipt
	r.ObservedAt = time.Now().UTC()
	return nil
}

func persist(ctx context.Context, r *Record, save Save) error {
	final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return save(final, r)
}

// Validate checks the safe journal representation without recovering a payload.
func (r Record) Validate() error {
	if !name.MatchString(r.CallID) || !name.MatchString(r.GrantRef) || !name.MatchString(r.Target) || r.ObservedAt.IsZero() {
		return errors.New("invalid external operation record")
	}
	for _, value := range []string{r.OperationID, r.RequestDigest, r.GrantBinding} {
		b, err := hex.DecodeString(value)
		if err != nil || len(b) != 32 || hex.EncodeToString(b) != value {
			return errors.New("invalid external operation binding")
		}
	}
	switch r.State {
	case "intent", "unknown", "accepted", "completed", "rejected", "not_found":
	default:
		return errors.New("invalid external operation state")
	}
	if r.Receipt != nil {
		if r.Receipt.OperationID != r.OperationID || r.Receipt.RequestDigest != r.RequestDigest || r.Receipt.Target != r.Target {
			return errors.New("invalid external receipt binding")
		}
		switch r.Receipt.State {
		case "accepted", "applied", "rejected":
		default:
			return errors.New("invalid external receipt state")
		}
	}
	if r.State == "completed" && (r.Receipt == nil || r.Receipt.State != "applied") || r.State == "accepted" && (r.Receipt == nil || r.Receipt.State != "accepted") || r.State == "rejected" && (r.Receipt == nil || r.Receipt.State != "rejected") {
		return errors.New("external result lacks receipt")
	}
	return nil
}
