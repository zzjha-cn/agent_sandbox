package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sandx/internal/agent"
	"sandx/internal/projectmemory"
	"sandx/internal/toolgate"
	"strings"
	"testing"
)

func TestRuntimeMemoryRecallSeparateFromStartupAndResume(t *testing.T) {
	req, store, entry := memoryFixture(t)
	req.Config.Memory.Query = "startup-no-match"
	req.Config.Memory.Recall = true
	session, err := prepareMemory(req, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Snapshot.Entries) != 0 {
		t.Fatal("startup fixture leaked target")
	}
	tools, err := memoryTools(context.Background(), req)
	if err != nil || len(tools) != 1 {
		t.Fatal(err)
	}
	result, err := tools[0].Call(context.Background(), json.RawMessage(`{"key":"symptom1","query":"Docker"}`))
	if err != nil {
		t.Fatal(err)
	}
	out := result.(projectmemory.RecallResponse)
	if out.Status != "ok" || !strings.Contains(out.Context, entry.ID) {
		t.Fatal(out)
	}
	after, err := ReadMemorySession(req.Task.Home, req.Task.ID())
	if err != nil || after.Snapshot.Digest != session.Snapshot.Digest {
		t.Fatal(after, err)
	}
	if _, err = tools[0].Call(context.Background(), json.RawMessage(`{"key":"bad","query":"Docker","project":"other"}`)); err == nil {
		t.Fatal("model changed scope")
	}
	if err = os.WriteFile(filepath.Join(req.Task.StateDir(), "codex-session"), []byte("0199a213-81c0-7800-8aa1-bbab2a035a53"), 0600); err != nil {
		t.Fatal(err)
	}
	req.MemoryAttemptID = "attempt2"
	again, err := prepareMemory(req, false)
	if err != nil || again.RecallID != session.RecallID {
		t.Fatal(again, err)
	}
	if _, err = store.Review("demo", entry.ID, entry.Digest, "archived", "operator", "revoke"); err != nil {
		t.Fatal(err)
	}
	req.MemoryAttemptID = "attempt3"
	if _, err = prepareMemory(req, false); err == nil {
		t.Fatal("recalled-only revoked entry resumed")
	}
	req.Fresh = true
	fresh, err := prepareMemory(req, false)
	if err != nil || fresh.RecallID == session.RecallID {
		t.Fatal(fresh, err)
	}
}

func TestRuntimeRecallMissingHistoryAndProviderContracts(t *testing.T) {
	req, _, _ := memoryFixture(t)
	req.Config.Memory.Recall = true
	v, err := prepareMemory(req, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(req.Task.StateDir(), "codex-session"), []byte("0199a213-81c0-7800-8aa1-bbab2a035a53"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(MemoryTaskDir(req.Task.Home, req.Task.ID()), "recalls", v.RecallID+".json")
	if err = os.Rename(path, path+".removed"); err != nil {
		t.Fatal(err)
	}
	tools, toolErr := memoryTools(context.Background(), req)
	if toolErr != nil {
		t.Fatal(toolErr)
	}
	if _, toolErr = tools[0].Call(context.Background(), json.RawMessage(`{"key":"lost","query":"Docker"}`)); toolErr == nil {
		t.Fatal("runtime recreated missing recall ledger")
	}
	req.MemoryAttemptID = "attempt2"
	if _, err = prepareMemory(req, false); err == nil {
		t.Fatal("missing recall history accepted")
	}
	for _, name := range []string{"codex", "claude"} {
		req.Config.DefaultAgent = name
		req.Config.Network.Proxy = "dedicated"
		req.Mode = Headless
		version := "0.169.0"
		if name == "claude" {
			version = "2.1.0"
		}
		a := req.Config.Agents[name]
		a.Version = version
		req.Config.Agents[name] = a
		if err = validateToolProvider(req); err != nil {
			t.Fatal(name, err)
		}
	}
}

func TestMemoryMCPProviderAdaptersControlled(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			req, _, _ := memoryFixture(t)
			req.Config.DefaultAgent = provider
			req.Config.Memory.Recall = true
			if _, err := prepareMemory(req, false); err != nil {
				t.Fatal(err)
			}
			tools, err := memoryTools(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			gateway, endpoint, err := toolgate.StartWithTools(context.Background(), req.Task.ID(), req.MemoryAttemptID, nil, toolgate.Policy{Listen: "127.0.0.1:0", Host: "127.0.0.1"}, nil, nil, nil, tools)
			if err != nil {
				t.Fatal(err)
			}
			defer gateway.Close()
			for _, method := range []string{"tools/list", "tools/call"} {
				body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"name":"memory_search","arguments":{"key":"new-symptom","query":"Docker"}}}`
				request, _ := http.NewRequest("POST", endpoint, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+gateway.Token())
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				err = json.NewDecoder(response.Body).Decode(&payload)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(payload)
				if method == "tools/list" && !strings.Contains(string(encoded), "memory_search") {
					t.Fatal(payload)
				}
				if method == "tools/call" && !strings.Contains(string(encoded), `"status":"ok"`) {
					t.Fatal(payload)
				}
			}
			p, _ := agent.ResolveProvider(provider)
			for _, resume := range []bool{false, true} {
				command, err := p.Command(agent.Launch{Headless: true, Tools: true, MemoryMode: "managed", MemoryContext: true, Continue: resume, SessionID: func() string {
					if resume {
						return "0199a213-81c0-7800-8aa1-bbab2a035a53"
					}
					return ""
				}()})
				if err != nil || !strings.Contains(command, "tools-") {
					t.Fatal(command, err)
				}
			}
		})
	}
}
