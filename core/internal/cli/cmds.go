package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sandx/internal/agent"
	"sandx/internal/docker"
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
		return fmt.Errorf("Task %s 没有在运行；用 sbx run %s 启动", t.Name, t.Name)
	}
	return nil
}

func (a *App) attach(t task.Task) error {
	if err := a.requireRunning(t); err != nil {
		return err
	}
	rt := agent.Runtime{Docker: a.Docker, Container: t.Container()}
	if !rt.HasSession() {
		return fmt.Errorf("Task %s 的 Agent 会话不存在；用 sbx run %s 重新拉起", t.Name, t.Name)
	}
	if agentExited(t) {
		fmt.Fprintf(a.Out, "提示：claude 已经退出，会话里现在是一个 shell。用 sbx run %s 重新拉起（默认接上这个 Task 的上次对话）。\n", t.Name)
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
				return fmt.Errorf("Task %s 不存在", t.Name)
			}
			if st.Running {
				if err := a.Docker.Stop(t.Container()); err != nil {
					return err
				}
			}
			fmt.Fprintf(a.Out, "已停止 %s\n", t.Name)
			if stopped, err := a.proxy().StopIfIdle(); err != nil {
				return err
			} else if stopped {
				fmt.Fprintln(a.Out, "没有运行中的 Task 了，已停止 sbx-proxy")
			}
			return nil
		},
	}
}

// taskNames 汇总当前 ws 下的 Task：有容器的，加上有 state 目录的。
func (a *App) taskNames() ([]string, error) {
	set := map[string]bool{}
	items, err := a.Docker.ListByLabel("container", "sbx.ws="+a.WS.ID, "sbx.role=agent")
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		for _, kv := range strings.Split(it.Str("Labels"), ",") {
			if v, ok := strings.CutPrefix(kv, "sbx.task="); ok {
				set[v] = true
			}
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

// TaskRow 是 sbx ls 的一行。
type TaskRow struct {
	Task, Status, Branch, Ahead, Diff, LastActive, Path string
}

// tildePath 把 home 前缀缩写成 ~。
func tildePath(p, home string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}

// padCJK 把 s 右侧补空格到 n 个显示宽度：CJK 字符占两格，ASCII 占一格。
// tabwriter 按字节算宽度，中英混排的列会错位，需要对齐的地方用这个。
func padCJK(s string, n int) string {
	w := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
			(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x20000 && r <= 0x3fffd)) {
			w += 2
		} else {
			w++
		}
	}
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func (a *App) row(t task.Task) TaskRow {
	home, _ := os.UserHomeDir()
	r := TaskRow{Task: t.Name, Branch: t.Branch(), Ahead: "-", Diff: "-", LastActive: "-", Path: tildePath(t.Worktree(), home)}
	st, exists, err := a.Docker.Inspect(t.Container())
	s, _ := t.ReadStatus()
	if err != nil {
		r.Status = "error"
	} else {
		r.Status = task.Derive(st, exists, s)
	}
	if s != nil && s.TS > 0 {
		r.LastActive = humanAgo(time.Since(time.Unix(s.TS, 0)))
	}
	if r.Branch == "" {
		r.Branch = "(repo root)"
		return r
	}
	meta, ok, _ := t.ReadMeta()
	if ok && meta.Base != "" && t.WS.BranchExists(t.Branch()) {
		if n, err := workspace.Git(t.WS.Root, "rev-list", "--count", meta.Base+".."+t.Branch()); err == nil {
			r.Ahead = n
		}
		if d, err := workspace.Git(t.WS.Root, "diff", "--shortstat", meta.Base+"..."+t.Branch()); err == nil {
			r.Diff = compactStat(d)
		}
	}
	return r
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

func (a *App) lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the tasks in this workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			names, err := a.taskNames()
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "workspace %s (%s)\n", a.WS.ID, a.WS.Root)
			w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TASK\tSTATUS\tBRANCH\tAHEAD\tDIFF\tLAST-ACTIVE\tPATH")
			for _, n := range names {
				t, err := a.task(n)
				if err != nil {
					continue
				}
				r := a.row(t)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Task, r.Status, r.Branch, r.Ahead, r.Diff, r.LastActive, r.Path)
			}
			return w.Flush()
		},
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
				return fmt.Errorf("Task %s 的工作目录不存在（%s）；用 sbx run %s 创建", t.Name, t.Worktree(), t.Name)
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
					err = a.done(t, force)
				}
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", n, err))
				}
			}
			if stopped, err := a.proxy().StopIfIdle(); err != nil {
				errs = append(errs, err)
			} else if stopped {
				fmt.Fprintln(a.Out, "没有运行中的 Task 了，已停止 sbx-proxy")
			}
			return errors.Join(errs...)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove the task even with uncommitted changes in its worktree")
	return cmd
}

func (a *App) done(t task.Task, force bool) error {
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
			return fmt.Errorf("worktree 有未提交的改动，先在 sbx shell %s 里提交，或加 --force 丢弃：\n%s", t.Name, dirty)
		}
	}
	meta, _, _ := t.ReadMeta()
	taskID := meta.TaskID
	if taskID == "" {
		taskID = t.ID()
	}
	if err := a.Docker.Rm(t.Container()); err != nil && !docker.IsNotFound(err) {
		return err
	}
	if err := a.proxy().DetachTask(taskID, t.Network()); err != nil {
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
	if t.IsMain() {
		fmt.Fprintf(a.Out, "已结束 %s（仓库根保持不变）\n", t.Name)
	} else {
		fmt.Fprintf(a.Out, "已结束 %s，分支 %s 保留。合并：git merge %s；删除分支：git branch -D %s\n",
			t.Name, t.Branch(), t.Branch(), t.Branch())
	}
	return nil
}
