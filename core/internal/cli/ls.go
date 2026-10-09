package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"sandx/internal/proxy"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

// deniedTimeout 是读代理日志的总预算。读不完就整列显示 -：
// sbx ls 是最常用的命令，不能因为一个慢查询卡在那里。
const deniedTimeout = 3 * time.Second

// TaskRow 是 sbx ls 的一行。
type TaskRow struct {
	Task, Status, Branch, Ahead, Diff, Denied, LastActive, Path string
}

func (a *App) row(t task.Task, denied map[string]int) TaskRow {
	home, _ := os.UserHomeDir()
	r := TaskRow{Task: t.Name, Branch: t.Branch(), Ahead: "-", Diff: "-", Denied: "-",
		LastActive: "-", Path: tildePath(t.Worktree(), home)}
	st, exists, err := a.Docker.Inspect(t.Container())
	s, _ := t.ReadStatus()
	if err != nil {
		r.Status = "error"
	} else {
		r.Status = task.Derive(st, exists, s, t.RunExit())
	}
	if s != nil && s.TS > 0 {
		r.LastActive = humanAgo(time.Since(time.Unix(s.TS, 0)))
	}
	meta, hasMeta, _ := t.ReadMeta()
	if denied != nil {
		id := t.ID()
		if hasMeta && meta.TaskID != "" {
			id = meta.TaskID
		}
		r.Denied = fmt.Sprint(denied[id])
	}
	if r.Branch == "" {
		r.Branch = "(repo root)"
		return r
	}
	// 仓库可能已经被删掉了（--all 会列出别的 Workspace），那就保持 -
	if hasMeta && meta.Base != "" && t.WS.Root != "" && t.WS.BranchExists(t.Branch()) {
		if n, err := workspace.Git(t.WS.Root, "rev-list", "--count", meta.Base+".."+t.Branch()); err == nil {
			r.Ahead = n
		}
		if d, err := workspace.Git(t.WS.Root, "diff", "--shortstat", meta.Base+"..."+t.Branch()); err == nil {
			r.Diff = compactStat(d)
		}
	}
	return r
}

// deniedCounts 汇总各 Task「不在白名单」的被拒次数。
// 读不到代理日志时返回 nil——代理没在跑是常态（最后一个 Task 停掉后它就被停了），
// 不能让 sbx ls 因此报错，整列显示 - 就好。
func (a *App) deniedCounts(refs []taskRef) map[string]int {
	type result struct{ m map[string]int }
	ch := make(chan result, 1)
	go func() {
		entries, err := a.accessLogs(nil)
		if err != nil {
			ch <- result{}
			return
		}
		// 每个 Task 只数它自己创建之后的：shared 模式下日志是全局且跨重建的，
		// 不切窗口会把同名旧 Task 的历史算进来。
		out := map[string]int{}
		policy := proxy.PolicyList()
		for _, ref := range refs {
			t, err := task.New(a.Home, ref.WS, ref.Name)
			if err != nil {
				continue
			}
			meta, ok, _ := t.ReadMeta()
			id := t.ID()
			if ok && meta.TaskID != "" {
				id = meta.TaskID
			}
			var since time.Time
			if ok {
				since = meta.CreatedAt
			}
			out[id] = proxy.DeniedCounts(entries, since, policy)[id]
		}
		ch <- result{m: out}
	}()
	select {
	case r := <-ch:
		return r.m
	case <-time.After(deniedTimeout):
		return nil
	}
}

// taskRef 是一个 Task 和它所属的 Workspace。--all 下来自多个 Workspace。
type taskRef struct {
	WS   workspace.Workspace
	Name string
}

// allTaskRefs 扫出所有 Workspace 的 Task：state 目录里的，加上还留着容器的。
func (a *App) allTaskRefs() ([]taskRef, error) {
	refs, err := a.allTaskRefsFromState()
	if err != nil {
		return nil, err
	}
	items, err := a.Docker.ListByLabel("container", "sbx.role=agent")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	roots := map[string]string{}
	for _, r := range refs {
		seen[r.WS.ID+"\x00"+r.Name] = true
		if r.WS.Root != "" {
			roots[r.WS.ID] = r.WS.Root
		}
	}
	for _, it := range items {
		l := labels(it)
		ws, name := l["sbx.ws"], l["sbx.task"]
		if ws == "" || name == "" || seen[ws+"\x00"+name] {
			continue
		}
		seen[ws+"\x00"+name] = true
		refs = append(refs, taskRef{WS: workspace.Workspace{ID: ws, Root: roots[ws]}, Name: name})
	}
	sortRefs(refs)
	return refs, nil
}

// allTaskRefsFromState 只看 ~/.sbx/state，不碰 docker。
// 仓库路径从每个 Task 的 meta.json 取；仓库已经被删掉的仍然列出，只是 AHEAD/DIFF 为 -。
func (a *App) allTaskRefsFromState() ([]taskRef, error) {
	roots := map[string]string{}
	var refs []taskRef
	wss, err := os.ReadDir(filepath.Join(a.Home, "state"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, w := range wss {
		if !w.IsDir() {
			continue
		}
		ents, _ := os.ReadDir(filepath.Join(a.Home, "state", w.Name()))
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			refs = append(refs, taskRef{WS: workspace.Workspace{ID: w.Name()}, Name: e.Name()})
			t := task.Task{Name: e.Name(), WS: workspace.Workspace{ID: w.Name()}, Home: a.Home}
			if meta, ok, _ := t.ReadMeta(); ok && meta.Root != "" {
				roots[w.Name()] = meta.Root
			}
		}
	}
	for i := range refs {
		refs[i].WS.Root = roots[refs[i].WS.ID]
	}
	sortRefs(refs)
	return refs, nil
}

func sortRefs(refs []taskRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].WS.ID != refs[j].WS.ID {
			return refs[i].WS.ID < refs[j].WS.ID
		}
		return refs[i].Name < refs[j].Name
	})
}

func (a *App) lsCmd() *cobra.Command {
	var all, noDenied bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the tasks in this workspace (--all for every workspace)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			refs, err := a.lsRefs(all)
			if err != nil {
				return err
			}
			var denied map[string]int
			if !noDenied {
				if denied = a.deniedCounts(refs); denied == nil {
					// 区分"没去数"和"数不出来"：只有后者要解释一句
					fmt.Fprintln(a.Err, "note: denied requests were not counted this time (the proxy is not running, or reading its log timed out); the DENIED column shows -")
				}
			}
			return a.printTasks(refs, denied, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list tasks in every workspace, not just this repo's")
	cmd.Flags().BoolVar(&noDenied, "no-denied", false, "skip the DENIED column (it reads the proxy's access log)")
	return cmd
}

// lsRefs 按 --all 决定列哪些 Task。--all 不要求当前在 git 仓库里：
// 它本来就是"站在任何地方看全局"的命令。
func (a *App) lsRefs(all bool) ([]taskRef, error) {
	if all {
		if err := a.loadConfig(); err != nil {
			return nil, err
		}
		return a.allTaskRefs()
	}
	if err := a.load(); err != nil {
		return nil, err
	}
	names, err := a.taskNames()
	if err != nil {
		return nil, err
	}
	refs := make([]taskRef, 0, len(names))
	for _, n := range names {
		refs = append(refs, taskRef{WS: a.WS, Name: n})
	}
	return refs, nil
}

func (a *App) printTasks(refs []taskRef, denied map[string]int, all bool) error {
	header := []string{"TASK", "STATUS", "BRANCH", "AHEAD", "DIFF", "DENIED", "LAST-ACTIVE", "PATH"}
	if all {
		header = append([]string{"WORKSPACE"}, header...)
	} else {
		fmt.Fprintf(a.Out, "workspace %s (%s)\n", a.WS.ID, a.WS.Root)
	}
	var rows [][]string
	for _, ref := range refs {
		t, err := task.New(a.Home, ref.WS, ref.Name)
		if err != nil {
			continue
		}
		r := a.row(t, denied)
		row := []string{r.Task, r.Status, r.Branch, r.Ahead, r.Diff, r.Denied, r.LastActive, r.Path}
		if all {
			row = append([]string{ref.WS.ID}, row...)
		}
		rows = append(rows, row)
	}
	return writeTable(a.Out, header, rows)
}
