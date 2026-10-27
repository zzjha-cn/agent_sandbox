package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sandx/internal/agent"
	"sandx/internal/docker"
	"sandx/internal/task"
	"strings"
	"time"
)

// AgentStarter owns launch/resume policy. Adapters supply infrastructure and
// optional Claude memory sync; CLI output formatting stays with the caller.
type AgentStarter struct {
	Docker          *docker.Client
	APIKey          func() (APIKey, error)
	ImportMemory    func(string) error
	InstallRuntimes func(task.Task, agent.Runtime)
	Logf            func(string, ...any)
	RotateLog       func(string) error
}

func (s AgentStarter) Start(ctx context.Context, t task.Task, o Request) error {
	provider, err := agent.ResolveProvider(o.Config.DefaultAgent)
	if err != nil {
		return err
	}
	rt := agent.Runtime{Docker: s.Docker, Container: t.Container(), Worktree: t.Worktree()}
	hasSession := rt.HasSession()
	if hasSession && !taskAgentExited(t) {
		return nil
	}
	key, err := s.APIKey()
	if err != nil {
		return err
	}
	if provider.Name() == "codex" && o.Mode != Headless && key.Env != "" {
		return fmt.Errorf("Codex API key injection is supported in headless mode; interactive mode requires sbx login codex")
	}
	if provider.Name() == "claude" {
		if out, err := rt.Preseed(key.Value); err != nil {
			return fmt.Errorf("failed to preseed the first-run state: %w", err)
		} else if out != "" {
			s.Logf("%s", out)
		}
		if err := s.ImportMemory(t.Container()); err != nil {
			s.Logf("warning: failed to import project memory: %v", err)
		}
	}
	s.InstallRuntimes(t, rt)
	// 配了 API key 就不查订阅登录态：key 在建容器时注进了环境变量（design §7.1）
	if err := s.checkAuth(t, rt, key, provider); err != nil {
		return err
	}
	cont := !o.Fresh && t.HasPriorSession()
	os.Remove(filepath.Join(t.StateDir(), "status.json"))
	os.Remove(filepath.Join(t.StateDir(), "run.exit"))
	if o.Mode == Headless {
		if err := s.RotateLog(filepath.Join(t.StateDir(), "run.log")); err != nil {
			return err
		}
	}
	sessionID := ""
	if provider.Name() == "codex" {
		data, readErr := os.ReadFile(filepath.Join(t.StateDir(), "codex-session"))
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
		sessionID = strings.TrimSpace(string(data))
		cont = !o.Fresh && sessionID != ""
		if o.Fresh {
			sessionID = ""
		}
	}
	command, err := provider.Command(agent.Launch{Headless: o.Mode == Headless, Continue: cont, Notify: o.Config.OnExit != "", SessionID: sessionID})
	if err != nil {
		return err
	}
	if provider.Name() == "codex" && !cont {
		if err := os.Remove(filepath.Join(t.StateDir(), "codex-session")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if hasSession {
		s.Logf("agent exited; bringing it back up in the same session ...")
		if err := rt.Respawn(command); err != nil {
			return err
		}
	} else if _, err := rt.StartSession(command); err != nil {
		return err
	}
	if cont {
		s.Logf("resuming the last conversation of task %s (use --fresh to start a new one)", t.Name)
	}
	s.Logf("waiting for agent to be ready ...")
	sm := agent.Smoke{
		Timeout: 30 * time.Second,
		Ready: func() bool {
			s, err := t.ReadStatus()
			return err == nil && s != nil && (s.Event == "SessionStart" || s.Event == "CodexStart")
		},
		Exited: func() bool { return taskAgentExited(t) },
	}
	if o.Mode == Headless {
		// headless 的任务可能几秒就跑完，正常结束不算启动失败；
		// 跑完了也算"就绪"，别再等 SessionStart（那条可能已经被 SessionEnd 覆盖了）
		sm.Exited = nil
		ready := sm.Ready
		sm.Ready = func() bool { return ready() || t.RunExit() != nil }
	}
	return rt.SmokeCheckContext(ctx, sm)
}

func taskAgentExited(t task.Task) bool {
	status, err := t.ReadStatus()
	return err == nil && status != nil && status.State == "exited"
}

func (s AgentStarter) checkAuth(t task.Task, rt agent.Runtime, key APIKey, provider agent.Provider) error {
	if key.Env != "" {
		has, err := s.Docker.HasEnv(t.Container(), key.Env)
		if err != nil {
			return err
		}
		if !has {
			return fmt.Errorf("container %s does not have configured %s; recreate the task", t.Container(), key.Env)
		}
		return nil
	}
	ok, err := provider.LoggedIn(rt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s is not logged in inside the sandbox; run sbx login %s, then sbx run %s", provider.Name(), provider.Name(), t.Name)
	}
	return nil
}
