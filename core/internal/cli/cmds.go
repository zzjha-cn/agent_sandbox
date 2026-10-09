package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sandx/internal/agent"
	"sandx/internal/docker"
	"sandx/internal/proxy"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

func (a *App) taskArg(args []string) (task.Task, error) {
	if err := a.load(); err != nil {
		return task.Task{}, err
	}
	return a.task(args[0])
}

func (a *App) attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <task>",
		Short: "Enter the task's agent session (tmux; Ctrl-b, release, then d leaves it running)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.taskArg(args)
			if err != nil {
				return err
			}
			return a.attach(t)
		},
	}
}

func (a *App) requireRunning(t task.Task) error {
	st, exists, err := a.Docker.Inspect(t.Container())
	if err != nil {
		return err
	}
	if !exists || !st.Running {
		return fmt.Errorf("task %s is not running; start it with sbx run %s", t.Name, t.Name)
	}
	return nil
}

func (a *App) attach(t task.Task) error {
	if err := a.requireRunning(t); err != nil {
		return err
	}
	rt := agent.Runtime{Docker: a.Docker, Container: t.Container()}
	if !rt.HasSession() {
		return fmt.Errorf("task %s has no agent session; bring it back with sbx run %s", t.Name, t.Name)
	}
	if agentExited(t) {
		fmt.Fprintf(a.Out, "note: claude has exited and the session is now a plain shell. Bring it back with sbx run %s (resumes this task's last conversation by default).\n", t.Name)
	}
	return a.Docker.ExecInteractive(t.Container(), docker.ExecOpts{User: "agent"}, "tmux", "attach", "-t", agent.Session)
}

func (a *App) shellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell <task>",
		Short: "Open a bash shell in the task's container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.taskArg(args)
			if err != nil {
				return err
			}
			if err := a.requireRunning(t); err != nil {
				return err
			}
			return a.Docker.ExecInteractive(t.Container(), docker.ExecOpts{User: "agent", Workdir: t.Worktree()}, "bash")
		},
	}
}

func (a *App) stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <task>",
		Short: "Stop the task's container, keeping everything; stops sbx-proxy with the last task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.taskArg(args)
			if err != nil {
				return err
			}
			st, exists, err := a.Docker.Inspect(t.Container())
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("task %s does not exist", t.Name)
			}
			if st.Running {
				if err := a.Docker.Stop(t.Container()); err != nil {
					return err
				}
			}
			fmt.Fprintf(a.Out, "stopped %s\n", t.Name)
			// dedicated 下停的是这个 Task 自己的 sidecar，shared 下只有最后一个 Task 停了才停
			if stopped, err := a.egress(t).StopIfIdle(); err != nil {
				return err
			} else if stopped {
				fmt.Fprintf(a.Out, "stopped %s\n", a.proxyName(t))
			}
			return nil
		},
	}
}

// tildePath 把 home 前缀缩写成 ~。
func tildePath(p, home string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}

// labels 把 docker ps 那一列逗号分隔的 label 拆成 map。
func labels(it docker.Item) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(it.Str("Labels"), ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// taskNames 汇总当前 ws 下的 Task：有容器的，加上有 state 目录的。
func (a *App) taskNames() ([]string, error) {
	set := map[string]bool{}
	items, err := a.Docker.ListByLabel("container", "sbx.ws="+a.WS.ID, "sbx.role=agent")
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if n := labels(it)["sbx.task"]; n != "" {
			set[n] = true
		}
	}
	ents, err := os.ReadDir(filepath.Join(a.Home, "state", a.WS.ID))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range ents {
		if e.IsDir() {
			set[e.Name()] = true
		}
	}
	var names []string
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// compactStat 把 "3 files changed, 10 insertions(+), 2 deletions(-)" 压成 "3f +10 -2"。
func compactStat(s string) string {
	if s == "" {
		return "0"
	}
	var f, ins, del int
	for _, part := range strings.Split(s, ",") {
		var n int
		part = strings.TrimSpace(part)
		fmt.Sscanf(part, "%d", &n)
		switch {
		case strings.Contains(part, "file"):
			f = n
		case strings.Contains(part, "insertion"):
			ins = n
		case strings.Contains(part, "deletion"):
			del = n
		}
	}
	return fmt.Sprintf("%df +%d -%d", f, ins, del)
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (a *App) pathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path [task]",
		Short: "Print a task's working directory (omit task for main, the repo root): cd $(sbx path <task>)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			t, err := a.task(name)
			if err != nil {
				return err
			}
			if _, err := os.Stat(t.Worktree()); err != nil {
				return fmt.Errorf("the working directory of task %s does not exist (%s); create it with sbx run %s", t.Name, t.Worktree(), t.Name)
			}
			fmt.Fprintln(a.Out, t.Worktree())
			return nil
		},
	}
}

func (a *App) doneCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "done <task>...",
		Short: "Finish a task: remove its container, network, dep volumes, worktree and state; keep the branch",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			// 记忆是 Workspace 级的（存在 sbx-home），每次 done 只提醒一次；趁容器还在时读
			a.remindPull()
			var errs []error
			for _, n := range args {
				t, err := a.task(n)
				if err == nil {
					err = a.done(t, force, false)
				}
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", n, err))
				}
			}
			// dedicated 的 sidecar 在各自的 done 里已经删掉了，这里只管共享的那个
			if stopped, err := a.proxy().StopIfIdle(); err != nil {
				errs = append(errs, err)
			} else if stopped {
				fmt.Fprintln(a.Out, "no tasks are running any more; stopped "+proxy.SharedName)
			}
			return errors.Join(errs...)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove the task even with uncommitted changes in its worktree")
	return cmd
}

// done 结束一个 Task。dropping 为真时是 sbx drop 调的，分支马上就要删掉，
// 这里就别再说"分支保留"了。
func (a *App) done(t task.Task, force, dropping bool) error {
	wtExists := false
	if _, err := os.Stat(t.Worktree()); err == nil {
		wtExists = true
	}
	if !t.IsMain() && wtExists && !force {
		dirty, err := workspace.Dirty(t.Worktree())
		if err != nil {
			return err
		}
		if dirty != "" {
			return fmt.Errorf("the worktree has uncommitted changes; commit them in sbx shell %s, or pass --force to discard them:\n%s", t.Name, dirty)
		}
	}
	meta, _, _ := t.ReadMeta()
	taskID := meta.TaskID
	if taskID == "" {
		taskID = t.ID()
	}
	// 端口转发容器接在 Task 网络上，不先删掉网络就删不动（M3-11）
	if err := a.removePorts(t, 0); err != nil {
		return err
	}

	if err := a.Docker.Rm(t.Container()); err != nil && !docker.IsNotFound(err) {
		return err
	}
	if err := a.egress(t).DetachTask(taskID, t.Network()); err != nil {
		return err
	}
	if err := a.Docker.NetworkRm(t.Network()); err != nil {
		return err
	}
	vols, err := a.Docker.ListByLabel("volume", "sbx.ws="+t.WS.ID, "sbx.task="+t.Name, "sbx.kind=dep")
	if err != nil {
		return err
	}
	for _, v := range vols {
		if err := a.Docker.VolumeRm(v.Str("Name")); err != nil {
			return err
		}
	}
	if !t.IsMain() {
		if err := t.WS.RemoveWorktree(t.Worktree(), force); err != nil {
			return err
		}
		os.Remove(filepath.Dir(t.Worktree())) // 空的 worktrees/<ws> 目录
	}
	if err := os.RemoveAll(t.StateDir()); err != nil {
		return err
	}
	os.Remove(filepath.Dir(t.StateDir()))
	if dropping {
		return nil
	}
	if t.IsMain() {
		fmt.Fprintf(a.Out, "finished %s (the repo root is unchanged)\n", t.Name)
	} else {
		fmt.Fprintf(a.Out, "finished %s; branch %s is kept. Merge it with git merge %s, or delete it with git branch -D %s\n",
			t.Name, t.Branch(), t.Branch(), t.Branch())
	}
	return nil
}
