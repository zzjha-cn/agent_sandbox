package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"sandx/internal/completion"
	"sandx/internal/external"
	"sandx/internal/fsutil"
	"sandx/internal/task"
	"sandx/internal/toolgate"
	"sandx/internal/workspace"
)

var toolVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

func validateToolProvider(req Request) error {
	if req.Config.Network.Proxy != "dedicated" {
		return errors.New("dynamic tools require the dedicated task proxy")
	}
	if req.Mode != Headless || req.Detach {
		return errors.New("dynamic tools require synchronous headless execution")
	}
	version := req.Config.Agents[req.Pipeline.Spec.Agent.Provider].Version
	m := toolVersion.FindStringSubmatch(version)
	if m == nil {
		return errors.New("tool provider requires a pinned supported release")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	switch req.Pipeline.Spec.Agent.Provider {
	case "codex":
		if major == 0 && minor >= 164 {
			return nil
		}
	case "claude":
		if major == 2 && minor >= 1 {
			return nil
		}
	}
	return errors.New("provider release does not support the admitted MCP HTTP contract")
}
func toolJournalPath(t task.Task, attempt string) string {
	return filepath.Join(t.Home, "tool-calls", t.WS.ID, t.Name, attempt+".jsonl")
}
func (s *Service) startTools(ctx context.Context, req *Request, result *Result) (func() error, error) {
	if err := validateToolProvider(*req); err != nil {
		return nil, err
	}
	policy := req.Pipeline.Policy.ToolGateway.Defaults()
	if !toolgate.ValidateHost(policy.Host) {
		return nil, errors.New("invalid gateway host")
	}
	for _, dir := range []string{filepath.Dir(toolJournalPath(req.Task, result.AttemptID)), req.Task.GenDir()} {
		if err := controlledDirectories(req.Task.Home, dir); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	journal, err := toolgate.NewJournal(toolJournalPath(req.Task, result.AttemptID))
	if err != nil {
		return nil, err
	}
	base := *req
	bindings := map[string]string{}
	for _, selection := range req.Pipeline.Spec.Tools {
		bindings[selection.ID] = req.Pipeline.Policy.Tools[selection.GrantRef].Binding()
	}
	auth := func(selection toolgate.Selection, target string) (external.Grant, error) {
		current := base.Pipeline.Policy
		if base.Pipeline.Reauthorize != nil {
			var err error
			current, err = base.Pipeline.Reauthorize()
			if err != nil {
				return external.Grant{}, external.ErrDenied
			}
		}
		grant, ok := current.Tools[selection.GrantRef]
		if !ok || grant.Binding() != bindings[selection.ID] {
			return grant, external.ErrDenied
		}
		plan := *base.Pipeline
		plan.Policy = current
		copy := base
		copy.Pipeline = &plan
		if pipelineBoundary(copy) != nil {
			return grant, external.ErrDenied
		}
		return grant, selection.Authorize(grant, target)
	}
	gateway, endpoint, err := toolgate.Start(ctx, req.Task.Name, result.AttemptID, req.Pipeline.Spec.Tools, policy, auth, external.HTTP{}, journal.Append)
	if err != nil {
		journal.Close()
		return nil, err
	}
	files := []string{"tool-token", "tools-codex.conf", "tools-claude.json", "tool-host"}
	var once sync.Once
	var closeErr error
	closeTools := func() error {
		once.Do(func() {
			closeErr = errors.Join(gateway.Close(), journal.Close())
			for _, file := range files {
				if err := os.Remove(filepath.Join(req.Task.GenDir(), file)); err != nil && !errors.Is(err, os.ErrNotExist) {
					closeErr = errors.Join(closeErr, err)
				}
			}
		})
		return closeErr
	}
	codex := fmt.Sprintf(`{sbx={url=%s,bearer_token_env_var="SBX_TOOL_TOKEN",required=true,startup_timeout_sec=15,tool_timeout_sec=305}}`, strconv.Quote("http://proxy:3129/mcp"))
	claude, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"sbx": map[string]any{"type": "http", "url": "http://proxy:3129/mcp", "headers": map[string]string{"Authorization": "Bearer " + gateway.Token()}}}})
	contents := []string{gateway.Token(), codex, string(claude), policy.Host}
	for i, file := range files {
		if err := fsutil.AtomicWrite(filepath.Join(req.Task.GenDir(), file), []byte(contents[i]), 0600); err != nil {
			closeTools()
			return nil, err
		}
	}
	s.facts.ToolJournal = result.AttemptID
	req.ToolGateway = true
	req.ToolEndpoint = endpoint
	s.closeTools = closeTools
	if err := s.checkpoint(ctx, result, PreparePhase, ProgressRunning); err != nil {
		closeTools()
		return nil, err
	}
	return closeTools, nil
}

// ReadToolCalls requires no credentials, project configuration or runtime.
func ReadToolCalls(t task.Task, record *TaskRecord) ([]toolgate.Audit, error) {
	calls := []toolgate.Audit{}
	bytes := 0
	if record == nil {
		return calls, nil
	}
	for _, attempt := range record.Attempts {
		if attempt.Pipeline == nil || attempt.Pipeline.ToolJournal == "" {
			continue
		}
		id := attempt.Pipeline.ToolJournal
		if id != attempt.Result.AttemptID || workspace.ValidateTaskName(id) != nil {
			return nil, ErrRecordInvalid
		}
		path := toolJournalPath(t, id)
		if controlledDirectories(t.Home, filepath.Dir(path)) != nil {
			return nil, ErrRecordInvalid
		}
		entries, err := toolgate.ReadJournal(path)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.AttemptID != id || entry.TaskID != t.Name {
				return nil, ErrRecordInvalid
			}
		}
		b, _ := json.Marshal(entries)
		bytes += len(b)
		if bytes > MaxRecordBytes {
			return nil, errors.New("tool status exceeds retention limit")
		}
		calls = append(calls, entries...)
	}
	return calls, nil
}

func (e externalEvaluator) ToolEvidence(_ context.Context, ids []string) (string, error) {
	facts := e.service.facts
	if facts.ToolJournal != e.result.AttemptID {
		return "", errors.New("tool evidence is missing")
	}
	entries, err := ReadToolCalls(e.req.Task, e.service.record)
	if err != nil {
		return "", err
	}
	selected := []toolgate.Audit{}
	for _, id := range ids {
		var selection *toolgate.Selection
		for _, tool := range e.req.Pipeline.Spec.Tools {
			if tool.ID == id {
				copy := tool
				selection = &copy
				break
			}
		}
		if selection == nil {
			return "", errors.New("required tool not declared")
		}
		found := false
		for _, entry := range entries {
			if entry.AttemptID == e.result.AttemptID && entry.ToolRef == id && entry.State == "completed" && entry.Authorization == "allowed" && entry.ResponseDigest != "" && entry.SelectionDigest == external.Digest(*selection) && entry.GrantBinding == e.req.Pipeline.Policy.Tools[selection.GrantRef].Binding() {
				selected = append(selected, entry)
				found = true
			}
		}
		if !found {
			return "", errors.New("required successful tool call is missing")
		}
	}
	return external.Digest(selected), nil
}

var _ completion.ToolEvidenceRunner = externalEvaluator{}
