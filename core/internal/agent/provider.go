package agent

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"sandx/internal/docker"
)

// Provider describes only agent-specific execution and authentication. Task
// admission, resource ownership, cancellation and delivery belong to execution.
type Provider interface {
	Name() string
	Command(Launch) (string, error)
	LoggedIn(Runtime) (bool, error)
}

type Launch struct {
	MemoryMode    string
	MemoryContext bool
	Headless      bool
	Continue      bool
	Notify        bool
	SessionID     string
	Tools         bool
	NoRepository  bool
}

func ResolveProvider(name string) (Provider, error) {
	switch name {
	case "", "claude":
		return claudeProvider{}, nil
	case "codex":
		return codexProvider{}, nil
	default:
		return nil, fmt.Errorf("unknown agent %q (supported: claude, codex)", name)
	}
}

type claudeProvider struct{}

func (claudeProvider) Name() string { return "claude" }
func (claudeProvider) Command(o Launch) (string, error) {
	if err := validateLaunchMemory(o); err != nil {
		return "", err
	}
	prefix := ""
	if o.MemoryMode == "managed" || o.MemoryMode == "off" {
		prefix = "export CLAUDE_CODE_DISABLE_AUTO_MEMORY=1\n"
	}
	if o.Headless {
		return prefix + HeadlessCmd(HeadlessOpts{Continue: o.Continue, Notify: o.Notify, Tools: o.Tools, MemoryContext: o.MemoryContext}), nil
	}
	cmd := ClaudeCmd(o.Continue)
	if o.MemoryContext {
		cmd = strings.Replace(cmd, " --dangerously-skip-permissions", ` "$(cat /sbx/gen/memory-context.md)" --dangerously-skip-permissions`, 1)
	}
	return prefix + cmd, nil
}
func validateLaunchMemory(o Launch) error {
	switch o.MemoryMode {
	case "", "native", "managed", "off":
	default:
		return errors.New("invalid memory mode")
	}
	if o.MemoryContext && o.MemoryMode != "managed" {
		return errors.New("memory context requires managed mode")
	}
	return nil
}
func (claudeProvider) LoggedIn(r Runtime) (bool, error) { return r.LoggedIn() }

// Codex prints login status to stderr in some releases; a successful status
// command is authoritative. Recognize only the explicit unauthenticated reply;
// Docker, network and executable failures must remain errors.
func ParseCodexStatus(out string, err error) (Status, error) {
	if err == nil {
		return Status{LoggedIn: true, AuthMethod: "Codex"}, nil
	}
	var de *docker.Error
	if errors.As(err, &de) {
		out += "\n" + de.Stderr
	}
	if strings.Contains(strings.ToLower(out), "not logged in") {
		return Status{}, nil
	}
	return Status{}, err
}

type codexProvider struct{}

func (codexProvider) Name() string { return "codex" }
func (codexProvider) LoggedIn(r Runtime) (bool, error) {
	out, err := r.exec("codex", "login", "status")
	st, err := ParseCodexStatus(out, err)
	return st.LoggedIn, err
}

var sessionIDRE = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func (codexProvider) Command(o Launch) (string, error) {
	if err := validateLaunchMemory(o); err != nil {
		return "", err
	}
	if o.SessionID != "" && !sessionIDRE.MatchString(o.SessionID) {
		return "", errors.New("invalid Codex session ID")
	}
	if o.Continue && o.SessionID == "" {
		return "", errors.New("Codex resume requires the task's session ID")
	}
	// Authentication and Codex transcripts remain in the shared identity volume.
	// Task ownership is enforced by an exact recorded session ID and worktree;
	// no auth file is copied into the task's host-visible state directory.
	setup := `mkdir -p /home/agent/.codex
export CODEX_HOME=/home/agent/.codex
`
	cmd := "codex"
	if o.Tools {
		setup += `export SBX_TOOL_TOKEN="$(cat /sbx/gen/tool-token)"
export NO_PROXY="${NO_PROXY},proxy"
export no_proxy="${no_proxy},proxy"
`
		cmd += ` -c "mcp_servers=$(cat /sbx/gen/tools-codex.conf)"`
	}
	if o.Headless {
		cmd += " exec"
		if o.Continue {
			cmd += " resume " + o.SessionID
		}
		if o.NoRepository {
			cmd += " --skip-git-repo-check"
		}
		cmd += codexMemoryFlags(o)
		prompt := PromptFile
		if o.MemoryContext {
			prompt = "/sbx/gen/memory-prompt.txt"
		}
		cmd += " --json --dangerously-bypass-approvals-and-sandbox - < " + prompt
		// jq runs synchronously in the pipeline, so the session is persisted
		// before run.exit becomes visible even on a very short turn.
		capture := `while IFS= read -r line; do
printf '%s\n' "$line"
sid=$(printf '%s\n' "$line" | jq -r 'select(.type == "thread.started") | .thread_id // empty' 2>/dev/null)
if [[ "$sid" =~ ^[a-fA-F0-9-]{36}$ ]]; then printf '%s' "$sid" > /sbx/state/codex-session.tmp; mv /sbx/state/codex-session.tmp /sbx/state/codex-session; printf '{"state":"running","event":"CodexStart"}' > /sbx/state/status.json; fi
done`
		body := setup + "set -o pipefail\n" + cmd + " 2>&1 | (" + capture + ") | tee -a " + RunLog + "\ncode=${PIPESTATUS[0]}\n"
		body += "printf '%s' \"$code\" > " + RunExit + ".tmp; mv " + RunExit + ".tmp " + RunExit + "\n"
		body += "printf '{\"state\":\"exited\",\"event\":\"CodexExit\"}' > /sbx/state/status.json\n"
		if o.Notify {
			body += "SBX_EXIT_CODE=\"$code\" " + NotifySh + " exit --sync\n"
		}
		return body + "kill 1\n", nil
	}
	if o.Continue {
		cmd += " resume " + o.SessionID
	}
	cmd += codexMemoryFlags(o) + " --dangerously-bypass-approvals-and-sandbox"
	if o.MemoryContext {
		cmd += ` "$(cat /sbx/gen/memory-context.md)"`
	}
	// Interactive stdout is a terminal UI. Capture rollout metadata for this
	// exact worktree only; reject ambiguous matches rather than guessing latest.
	capture := `find /home/agent/.codex/sessions -name '*.jsonl' -newer /sbx/state/codex-launch -type f -exec head -n 1 {} \; 2>/dev/null | jq -sr --arg cwd "$PWD" '[.[] | select(.type == "session_meta" and .payload.cwd == $cwd) | .payload.id] | unique | if length == 1 then .[0] else empty end' > /sbx/state/codex-session.tmp
if test -s /sbx/state/codex-session.tmp; then mv /sbx/state/codex-session.tmp /sbx/state/codex-session; fi
`

	return setup + "touch /sbx/state/codex-launch\nprintf '{\"state\":\"idle\",\"event\":\"CodexStart\"}' > /sbx/state/status.json\n" + cmd + "\n" + capture + "printf '{\"state\":\"exited\",\"event\":\"CodexExit\"}' > /sbx/state/status.json\nprintf '\\n[sbx] codex exited\\n'; exec bash -l", nil
}

func codexMemoryFlags(o Launch) string {
	if o.MemoryMode == "" {
		return ""
	}
	if o.MemoryMode == "native" {
		return " -c features.memories=true -c memories.generate_memories=true -c memories.use_memories=true"
	}
	return " -c features.memories=false -c memories.generate_memories=false -c memories.use_memories=false"
}
