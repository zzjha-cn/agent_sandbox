package execution

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sandx/internal/completion"
	"sandx/internal/external"
)

type recordServer struct {
	sync.Mutex
	writes, queries, reads   int
	receipt                  *external.Receipt
	value                    map[string]string
	drop, accepted, mismatch bool
}

func (s *recordServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Lock()
	defer s.Unlock()
	if r.Header.Get("Authorization") != "Bearer synthetic-token" {
		w.WriteHeader(403)
		return
	}
	if r.Method == "PUT" {
		s.writes++
		var body struct {
			OperationID   string            `json:"operation_id"`
			RequestDigest string            `json:"request_digest"`
			Value         map[string]string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		if s.receipt != nil && s.receipt.OperationID != body.OperationID {
			w.WriteHeader(409)
			return
		}
		s.receipt = &external.Receipt{OperationID: body.OperationID, Target: "r1", RequestDigest: body.RequestDigest, State: "applied"}
		s.value = body.Value
		if s.accepted {
			s.receipt.State = "accepted"
		}
		if s.drop {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
			return
		}
		json.NewEncoder(w).Encode(s.receipt)
		return
	}
	if len(r.URL.Path) > 12 && r.URL.Path[:12] == "/operations/" {
		s.queries++
		if s.receipt == nil {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(s.receipt)
		return
	}
	s.reads++
	value := s.value
	if s.mismatch {
		value = map[string]string{"status": "wrong"}
	}
	id := ""
	if s.receipt != nil {
		id = s.receipt.OperationID
	}
	json.NewEncoder(w).Encode(external.State{OperationID: id, Value: value})
}
func externalRequest(t *testing.T, server *recordServer) (Request, *FileStore, *producingDriver) {
	t.Helper()
	req, store := generalRequest(t)
	endpoint := httptest.NewServer(server)
	t.Cleanup(endpoint.Close)
	token := filepath.Join(req.Task.Home, "record-token")
	if err := os.WriteFile(token, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Pipeline.Spec.Outputs = nil
	req.Pipeline.Spec.Operations = []external.Call{{ID: "update", GrantRef: "records", Target: "r1", Value: map[string]string{"status": "done"}}}
	req.Pipeline.Spec.Criteria = []completion.Criterion{{ID: "business", Kind: "external_state", Ref: "business"}}
	req.Pipeline.Policy.Tools = map[string]external.Grant{"records": {Connector: "record_http", Endpoint: endpoint.URL, CredentialFile: token, Targets: []string{"r1"}, Fields: []string{"status"}, Actions: []string{"write", "lookup", "read"}, ExpiresAt: time.Now().Add(time.Hour), TimeoutSeconds: 2, MaxResponseBytes: 1024}}
	req.Pipeline.Policy.Criteria = map[string]completion.Rule{"business": {Kind: "external_state", Version: "1", Operation: "update", Expected: map[string]string{"status": "done"}}}
	code := 0
	driver := &producingDriver{body: "{}", testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	return req, store, driver
}
func TestExternalTaskNoFilesAndBusinessEvidence(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "pass", true: "business_mismatch"}[mismatch], func(t *testing.T) {
			server := &recordServer{mismatch: mismatch}
			req, store, d := externalRequest(t, server)
			result, err := NewService(d).WithStore(store).Run(context.Background(), req)
			if (err != nil) != mismatch || result.ExecutionState != Finished || result.ArtifactState != ArtifactNotProduced {
				t.Fatalf("%+v %v", result, err)
			}
			want := "passed"
			if mismatch {
				want = "failed"
			}
			if result.ValidationState != want {
				t.Fatalf("validation %+v", result)
			}
			record, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			e := record.Attempts[0].Pipeline.Validation.Evidence[0]
			if e.OperationID == "" || e.ReceiptDigest == "" || e.StateDigest == "" {
				t.Fatal("missing business evidence")
			}
			if len(record.Operations) != 1 || record.Operations[0].State != "completed" {
				t.Fatal("missing operation journal")
			}
			if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "verify", ""); (err != nil) != mismatch {
				t.Fatal(err)
			}
			server.Lock()
			defer server.Unlock()
			if server.writes != 1 || d.creates != 1 {
				t.Fatal("verification repeated write or agent")
			}
		})
	}
}
func TestExternalLostResponseRecoveryNeverReplaysAgentOrWrite(t *testing.T) {
	server := &recordServer{drop: true}
	req, store, d := externalRequest(t, server)
	result, err := NewService(d).WithStore(store).Run(context.Background(), req)
	var classified *ExecutionError
	if !errors.As(err, &classified) || classified.Kind != ProductOutcomeUnknown || result.State != OutcomeUnknown {
		t.Fatalf("%+v %v", result, err)
	}
	record, _ := store.Load()
	if record.Recovery() != "reconcile_operations" {
		t.Fatal(record.Recovery())
	}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err == nil {
		t.Fatal("unknown run was replayed")
	}
	// Write permission can be removed while explicitly retaining lookup/read.
	g := req.Pipeline.Policy.Tools["records"]
	g.Actions = []string{"lookup", "read"}
	req.Pipeline.Policy.Tools["records"] = g
	result, err = NewService(d).WithStore(store).Continue(context.Background(), req, "reconcile", "")
	if err != nil || result.ValidationState != "passed" {
		t.Fatalf("%+v %v", result, err)
	}
	record, _ = store.Load()
	if len(record.Attempts) != 1 || record.Operations[0].State != "completed" {
		t.Fatal("recovery changed attempt")
	}
	server.Lock()
	defer server.Unlock()
	if server.writes != 1 || d.creates != 1 {
		t.Fatal("recovery replayed execution")
	}
}
func TestExternalAcceptedIsUnknownAndCanBeReconciledLater(t *testing.T) {
	server := &recordServer{accepted: true}
	req, store, d := externalRequest(t, server)
	_, err := NewService(d).WithStore(store).Run(context.Background(), req)
	if err == nil {
		t.Fatal("accepted passed")
	}
	result, err := NewService(d).WithStore(store).Continue(context.Background(), req, "verify", "")
	if err == nil || result.ValidationState != "unknown" {
		t.Fatalf("%+v %v", result, err)
	}
	server.Lock()
	if server.reads != 0 {
		t.Fatal("accepted treated as completed")
	}
	server.receipt.State = "applied"
	server.Unlock()
	result, err = NewService(d).WithStore(store).Continue(context.Background(), req, "reconcile", "")
	if err != nil || result.ValidationState != "passed" {
		t.Fatalf("%+v %v", result, err)
	}
	if d.creates != 1 {
		t.Fatal("agent replay")
	}
}
func TestExternalRevokedRecoveryPreservesUnknown(t *testing.T) {
	server := &recordServer{drop: true}
	req, store, d := externalRequest(t, server)
	NewService(d).WithStore(store).Run(context.Background(), req)
	g := req.Pipeline.Policy.Tools["records"]
	g.Revoked = true
	req.Pipeline.Policy.Tools["records"] = g
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "reconcile", ""); err == nil {
		t.Fatal("revoked read allowed")
	}
	record, _ := store.Load()
	if record.Operations[0].State != "unknown" {
		t.Fatal("revocation erased uncertainty")
	}
	server.Lock()
	defer server.Unlock()
	if server.queries != 0 || server.writes != 1 {
		t.Fatal("revoked recovery contacted service")
	}
}
func TestExternalManualGrantThenApplyWithoutAgent(t *testing.T) {
	server := &recordServer{}
	req, store, d := externalRequest(t, server)
	g := req.Pipeline.Policy.Tools["records"]
	g.Actions = []string{"lookup", "read"}
	req.Pipeline.Policy.Tools["records"] = g
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err == nil {
		t.Fatal("write without authorization")
	}
	g.Actions = append(g.Actions, "write")
	req.Pipeline.Policy.Tools["records"] = g
	result, err := NewService(d).WithStore(store).Continue(context.Background(), req, "apply", "")
	if err != nil || result.ValidationState != "passed" {
		t.Fatalf("%+v %v", result, err)
	}
	server.Lock()
	defer server.Unlock()
	if server.writes != 1 || d.creates != 1 {
		t.Fatal("manual grant replayed agent")
	}
}
func TestExternalCannotChangeOperationsDuringContinuation(t *testing.T) {
	server := &recordServer{drop: true}
	req, store, d := externalRequest(t, server)
	NewService(d).WithStore(store).Run(context.Background(), req)
	req.Pipeline.Spec.Operations[0].Value["status"] = "different"
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "reconcile", ""); err == nil {
		t.Fatal("operation rebound")
	}
	server.Lock()
	defer server.Unlock()
	if server.queries != 0 {
		t.Fatal("rebound operation queried service")
	}
}

func TestExternalCredentialCannotBeInAgentMount(t *testing.T) {
	server := &recordServer{}
	req, _, _ := externalRequest(t, server)
	for _, name := range []string{filepath.Join(req.Task.Worktree(), "token"), filepath.Join(req.Task.StateDir(), "token")} {
		g := req.Pipeline.Policy.Tools["records"]
		g.CredentialFile = name
		req.Pipeline.Policy.Tools["records"] = g
		if err := pipelineBoundary(req); err == nil {
			t.Fatal("credential in agent mount accepted")
		}
	}
}
func TestExternalUnresolvedOtherOperationCannotPassOrDeliver(t *testing.T) {
	server := &recordServer{}
	req, store, d := externalRequest(t, server)
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	record, _ := store.Load()
	record.Operations[0].State = "unknown"
	record.Operations[0].External.State = "unknown"
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	unlock()
	// With no external criterion, a pending operation still prevents false success.
	svc := NewService(d).WithStore(store)
	svc.record = record
	result := completion.Result{State: "passed"}
	svc.guardExternalCompletion(req, &result)
	if result.State != "unknown" {
		t.Fatal("unresolved write passed completion")
	}
}

func TestExternalOperationsComposeWithArtifactDelivery(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "recover-before-collection"}[drop], func(t *testing.T) {
			server := &recordServer{drop: drop}
			req, store, d := externalRequest(t, server)
			req.Pipeline.Spec.Outputs = &OutputSelection{Include: []string{"report.json"}}
			req.Pipeline.Spec.Criteria = append(req.Pipeline.Spec.Criteria, completion.Criterion{ID: "files", Kind: "files", Ref: "report"})
			req.Pipeline.Policy.Criteria["report"] = completion.Rule{Kind: "files", Version: "1", Required: []string{"report.json"}, Format: map[string]string{"report.json": "json"}}
			target := withDirectorySink(t, &req)
			result, err := NewService(d).WithStore(store).Run(context.Background(), req)
			if drop {
				if err == nil {
					t.Fatal("lost response passed")
				}
				result, err = NewService(d).WithStore(store).Continue(context.Background(), req, "reconcile", "")
			}
			if err != nil || result.ProductDeliveryState != "delivered" || result.ValidationState != "passed" {
				t.Fatalf("%+v %v", result, err)
			}
			if _, err = os.Stat(filepath.Join(target, "report.json")); err != nil {
				t.Fatal(err)
			}
			// A target-service change invalidates cached business evidence for delivery.
			g := req.Pipeline.Policy.Tools["records"]
			g.Endpoint = "http://127.0.0.1:1"
			req.Pipeline.Policy.Tools["records"] = g
			if _, err = NewService(d).WithStore(store).Continue(context.Background(), req, "deliver", ""); err == nil {
				t.Fatal("service rebind used old business proof")
			}
			server.Lock()
			defer server.Unlock()
			if server.writes != 1 || d.creates != 1 {
				t.Fatal("mixed recovery replayed write or agent")
			}
		})
	}
}
