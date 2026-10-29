package external

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeConnector struct {
	cap              Capability
	submits, lookups int
	receipt          *Receipt
	submitErr        error
	submit           func()
}

func (f *fakeConnector) Capability() Capability { return f.cap }
func (f *fakeConnector) Submit(_ context.Context, _ Grant, c Call, id, digest string) (Receipt, error) {
	f.submits++
	if f.submit != nil {
		f.submit()
	}
	r := Receipt{id, c.Target, digest, "applied"}
	f.receipt = &r
	return r, f.submitErr
}
func (f *fakeConnector) Lookup(context.Context, Grant, Call, string) (*Receipt, error) {
	f.lookups++
	return f.receipt, nil
}
func (f *fakeConnector) Read(context.Context, Grant, Call) (State, error) { return State{}, nil }
func fixture(t *testing.T) (Call, Grant) {
	t.Helper()
	return Call{"update", "records", "r1", map[string]string{"status": "done"}}, Grant{Connector: "record_http", Endpoint: "http://127.0.0.1:1", CredentialFile: filepath.Join(t.TempDir(), "token"), Targets: []string{"r1"}, Fields: []string{"status"}, Actions: []string{"write", "lookup", "read"}, ExpiresAt: time.Now().Add(time.Hour), TimeoutSeconds: 1, MaxResponseBytes: 1024}
}
func TestJournalLostResponseCancellationAndReconciliation(t *testing.T) {
	for _, mode := range []string{"response_lost", "cancel", "receipt_save_failed"} {
		t.Run(mode, func(t *testing.T) {
			c, g := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fakeConnector{cap: Capability{"idempotent_write", true}}
			if mode != "receipt_save_failed" {
				f.submitErr = errors.New("transport lost")
			}
			if mode == "cancel" {
				f.submit = cancel
			}
			var durable *Record
			saves := 0
			save := func(ctx context.Context, r *Record) error {
				if err := ctx.Err(); err != nil {
					t.Fatal("final save inherited cancellation")
				}
				saves++
				if saves == 2 && mode == "receipt_save_failed" {
					return errors.New("disk unavailable")
				}
				copy := *r
				durable = &copy
				return nil
			}
			auth := func(c Call, a string) (Grant, error) { return g, g.Authorize(c, a) }
			r, err := Execute(ctx, "task", c, nil, auth, f, save)
			if !errors.Is(err, ErrUnknown) || f.submits != 1 || durable == nil {
				t.Fatalf("%+v %v %d", r, err, f.submits)
			}
			if mode == "receipt_save_failed" && durable.State != "intent" {
				t.Fatal("lost intent")
			}
			if _, err = Execute(context.Background(), "task", c, durable, auth, f, save); err != nil {
				t.Fatal(err)
			}
			if f.submits != 1 || f.lookups != 1 || durable.State != "completed" {
				t.Fatalf("write replayed: %+v", durable)
			}
		})
	}
}
func TestMissingLookupAnd404NeverRetryWrites(t *testing.T) {
	for _, lookup := range []bool{false, true} {
		t.Run(map[bool]string{false: "non_idempotent_without_lookup", true: "idempotent_lookup_404"}[lookup], func(t *testing.T) {
			c, g := fixture(t)
			f := &fakeConnector{cap: Capability{"non_idempotent_write", lookup}, submitErr: errors.New("lost")}
			save := func(context.Context, *Record) error { return nil }
			auth := func(c Call, a string) (Grant, error) { return g, g.Authorize(c, a) }
			r, _ := Execute(context.Background(), "task", c, nil, auth, f, save)
			f.receipt = nil
			for i := 0; i < 3; i++ {
				_, err := Execute(context.Background(), "task", c, r, auth, f, save)
				if !errors.Is(err, ErrUnknown) {
					t.Fatal(err)
				}
			}
			if f.submits != 1 {
				t.Fatal("unknown write retried")
			}
		})
	}
}
func TestAuthorizationAndKeyBinding(t *testing.T) {
	c, g := fixture(t)
	f := &fakeConnector{cap: Capability{"idempotent_write", true}}
	save := func(context.Context, *Record) error { return nil }
	auth := func(c Call, a string) (Grant, error) { return g, g.Authorize(c, a) }
	r, err := Execute(context.Background(), "task", c, nil, auth, f, save)
	if err != nil {
		t.Fatal(err)
	}
	c.Value["status"] = "changed"
	if _, err := Execute(context.Background(), "task", c, r, auth, f, save); err == nil {
		t.Fatal("key rebound")
	}
	c.Value["status"] = "done"
	g.Actions = []string{"lookup", "read"}
	if _, err := Execute(context.Background(), "task", c, r, auth, f, save); err != nil {
		t.Fatal("read-only recovery denied", err)
	}
	g.Revoked = true
	if _, err := Execute(context.Background(), "task", c, r, auth, f, save); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if f.submits != 1 {
		t.Fatal("authorization failure sent write")
	}
	for _, modify := range []func(*Grant){func(g *Grant) { g.ExpiresAt = time.Now().Add(-time.Second) }, func(g *Grant) { g.Targets = nil }, func(g *Grant) { g.Fields = nil }, func(g *Grant) { g.Connector = "unknown" }} {
		_, g = fixture(t)
		modify(&g)
		if g.Authorize(c, "write") == nil {
			t.Fatal("out of scope allowed")
		}
	}
}
func TestIntentFailureAndRevocationBeforeSubmit(t *testing.T) {
	for _, mode := range []string{"disk", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			c, g := fixture(t)
			f := &fakeConnector{cap: Capability{"idempotent_write", true}}
			auth := func(c Call, a string) (Grant, error) { return g, g.Authorize(c, a) }
			save := func(context.Context, *Record) error {
				if mode == "disk" {
					return errors.New("disk")
				}
				g.Revoked = true
				return nil
			}
			if _, err := Execute(context.Background(), "task", c, nil, auth, f, save); err == nil {
				t.Fatal("expected refusal")
			}
			if f.submits != 0 {
				t.Fatal("write preceded durable intent or authorization")
			}
		})
	}
}
func TestHTTPConnectorBoundsAndNoCredentialLeak(t *testing.T) {
	c, g := fixture(t)
	if err := os.WriteFile(g.CredentialFile, []byte("test-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	mode := "ok"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing delegated credential")
		}
		switch mode {
		case "redirect":
			http.Redirect(w, r, "http://127.0.0.1:1", 302)
		case "large":
			w.Write([]byte(strings.Repeat("x", 2000)))
		case "malformed":
			w.Write([]byte(`{"operation_id":"secret","extra":1}`))
		case "timeout":
			<-r.Context().Done()
		case "missing":
			w.WriteHeader(404)
		default:
			if r.Header.Get("Idempotency-Key") != "op" {
				t.Error("missing idempotency key")
			}
			json.NewEncoder(w).Encode(Receipt{"op", c.Target, Digest(c), "applied"})
		}
	}))
	defer server.Close()
	g.Endpoint = server.URL
	if _, err := (HTTP{}).Submit(context.Background(), g, c, "op", Digest(c)); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"redirect", "large", "malformed"} {
		mode = m
		if _, err := (HTTP{}).Submit(context.Background(), g, c, "op", Digest(c)); err == nil || strings.Contains(err.Error(), "test-secret") {
			t.Fatalf("unsafe response %s %v", m, err)
		}
	}
	mode = "missing"
	if r, err := (HTTP{}).Lookup(context.Background(), g, c, "op"); r != nil || err != nil {
		t.Fatalf("%+v %v", r, err)
	}
	// A revoked, expired or out-of-target grant makes no HTTP call.
	before := calls
	g.Revoked = true
	if _, err := (HTTP{}).Submit(context.Background(), g, c, "op", Digest(c)); err == nil {
		t.Fatal("revoked")
	}
	if calls != before {
		t.Fatal("denial contacted service")
	}
	g.Revoked = false
	if err := os.Chmod(g.CredentialFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := (HTTP{}).Submit(context.Background(), g, c, "op", Digest(c)); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestReceiptCannotBeReboundOrRegress(t *testing.T) {
	r := &Record{OperationID: "op", Target: "r1", RequestDigest: "digest", State: "completed", Receipt: &Receipt{"op", "r1", "digest", "applied"}}
	for _, receipt := range []Receipt{{"other", "r1", "digest", "applied"}, {"op", "other", "digest", "applied"}, {"op", "r1", "different", "applied"}, {"op", "r1", "digest", "accepted"}} {
		if err := accept(r, receipt); err == nil {
			t.Fatal("invalid receipt")
		}
		if r.State != "completed" {
			t.Fatal("failed check mutated state")
		}
	}
}

func TestDurablyNeverSubmittedCanBeRetried(t *testing.T) {
	c, g := fixture(t)
	f := &fakeConnector{cap: Capability{"idempotent_write", true}}
	authorizations := 0
	auth := func(c Call, a string) (Grant, error) {
		authorizations++
		if authorizations == 2 {
			return g, ErrDenied
		}
		return g, g.Authorize(c, a)
	}
	var durable *Record
	save := func(_ context.Context, r *Record) error { copy := *r; durable = &copy; return nil }
	_, err := Execute(context.Background(), "task", c, nil, auth, f, save)
	if err == nil || durable.State != "not_found" || f.submits != 0 {
		t.Fatal("unsafe never-submitted state")
	}
	// Explicit Execute may submit this proven absence; query-only Reconcile may not.
	if _, err := Execute(context.Background(), "task", c, durable, auth, f, save); err != nil {
		t.Fatal(err)
	}
	if f.submits != 1 || durable.State != "completed" {
		t.Fatal("authorized retry failed")
	}
}
