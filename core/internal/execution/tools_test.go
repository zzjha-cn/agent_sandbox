package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sandx/internal/config"
	"sandx/internal/external"
	"sandx/internal/toolgate"
)

type toolDriver struct {
	producingDriver
	call            bool
	endpoint, token string
}

func (d *toolDriver) Create(ctx context.Context, req Request) error {
	if err := d.producingDriver.Create(ctx, req); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(req.Task.GenDir(), "tools-claude.json"))
	if err != nil {
		return err
	}
	var file struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err = json.Unmarshal(body, &file); err != nil {
		return err
	}
	d.endpoint = req.ToolEndpoint
	d.token = file.Servers["sbx"].Headers["Authorization"]
	if !d.call {
		return nil
	}
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"records","arguments":{"target":"r1"}}}`
	r, _ := http.NewRequestWithContext(ctx, "POST", d.endpoint, strings.NewReader(payload))
	r.Header.Set("Authorization", d.token)
	r.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result struct {
		Result struct {
			IsError bool `json:"isError"`
		}
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if result.Result.IsError {
		return fmt.Errorf("tool failed")
	}
	return nil
}
func (d *toolDriver) Resume(ctx context.Context, req Request, _ bool) error {
	return d.Create(ctx, req)
}
func toolRequest(t *testing.T, call bool) (Request, *FileStore, *toolDriver) {
	t.Helper()
	req, store := generalRequest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-service-secret" {
			w.WriteHeader(403)
			return
		}
		json.NewEncoder(w).Encode(external.State{OperationID: "seed", Value: map[string]string{"status": "done", "private": "not-retained"}})
	}))
	t.Cleanup(backend.Close)
	credential := filepath.Join(req.Task.Home, "token")
	if err := os.WriteFile(credential, []byte("synthetic-service-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Pipeline.Spec.Tools = []toolgate.Selection{{ID: "records", Kind: "records.get", GrantRef: "read", Targets: []string{"r1"}, Fields: []string{"status"}}}
	req.Pipeline.Policy.Tools = map[string]external.Grant{"read": {Connector: "record_http", Endpoint: backend.URL, CredentialFile: credential, Targets: []string{"r1"}, ReadFields: []string{"status"}, Actions: []string{"read"}, ExpiresAt: time.Now().Add(time.Hour), TimeoutSeconds: 1, MaxResponseBytes: 4096}}
	rule := req.Pipeline.Policy.Criteria["report"]
	rule.RequiredTools = []string{"records"}
	req.Pipeline.Policy.Criteria["report"] = rule
	req.Pipeline.Policy.ToolGateway = toolgate.Policy{Listen: "127.0.0.1:0", Host: "127.0.0.1"}
	req.Config.Network.Proxy = "dedicated"
	req.Config.Agents["codex"] = config.Agent{Version: "0.164.0"}
	code := 0
	d := &toolDriver{call: call, producingDriver: producingDriver{body: `{"status":"done"}`, testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}}
	return req, store, d
}
func TestDynamicToolEvidenceAndLifecycle(t *testing.T) {
	for _, call := range []bool{false, true} {
		t.Run(fmt.Sprint(call), func(t *testing.T) {
			req, store, d := toolRequest(t, call)
			target := withDirectorySink(t, &req)
			result, err := NewService(d).WithStore(store).Run(context.Background(), req)
			if (err == nil) != call {
				t.Fatalf("%+v %v", result, err)
			}
			record, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			entries, err := ReadToolCalls(req.Task, record)
			if err != nil {
				t.Fatal(err)
			}
			if call {
				if len(entries) != 2 || entries[1].State != "completed" || result.ProductDeliveryState != "delivered" {
					t.Fatalf("%+v %+v", entries, result)
				}
				if record.Attempts[0].Pipeline.Validation.Evidence[0].ToolDigest == "" {
					t.Fatal("missing bound tool evidence")
				}
				if _, err = os.Stat(filepath.Join(target, "report.json")); err != nil {
					t.Fatal(err)
				}
			} else {
				if result.ValidationState != "failed" {
					t.Fatal(result)
				}
				if _, err = os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("no-call report delivered")
				}
			}
			if _, err = os.Stat(filepath.Join(req.Task.GenDir(), "tool-token")); !os.IsNotExist(err) {
				t.Fatal("lease retained after run")
			}
			if _, err = http.Post(d.endpoint, "application/json", strings.NewReader(`{}`)); err == nil {
				t.Fatal("gateway still active")
			}
			if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "verify", ""); (err == nil) != call {
				t.Fatal(err)
			}
			if d.creates != 1 {
				t.Fatal("verification replayed agent")
			}
		})
	}
}
func TestDynamicToolsRejectUnsupportedProviderBeforePreparation(t *testing.T) {
	req, store, d := toolRequest(t, true)
	req.Config.Agents["codex"] = config.Agent{Version: "0.1.0"}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	if _, err := os.Stat(req.Task.Worktree()); !os.IsNotExist(err) || d.creates != 0 {
		t.Fatal("unsupported tools created runtime state")
	}
}
func TestDynamicToolContinuationCannotAcquireTools(t *testing.T) {
	req, store := generalRequest(t)
	code := 0
	d := &producingDriver{body: `{}`, testDriver: testDriver{outcome: AgentOutcome{ExitCode: &code, ContainerState: ContainerStopped}}}
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Pipeline.Spec.Tools = []toolgate.Selection{{ID: "new", Kind: "records.get", GrantRef: "missing", Targets: []string{"r1"}, Fields: []string{"status"}}}
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "verify", ""); err == nil {
		t.Fatal("old attempt acquired tools")
	}
}

func TestDynamicToolServiceRebindingInvalidatesDelivery(t *testing.T) {
	req, store, d := toolRequest(t, true)
	withDirectorySink(t, &req)
	if _, err := NewService(d).WithStore(store).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	grant := req.Pipeline.Policy.Tools["read"]
	grant.Endpoint = "http://127.0.0.1:1"
	req.Pipeline.Policy.Tools["read"] = grant
	if _, err := NewService(d).WithStore(store).Continue(context.Background(), req, "deliver", ""); err == nil {
		t.Fatal("old tool evidence was rebound to new service")
	}
	if d.creates != 1 {
		t.Fatal("delivery replayed agent")
	}
}
