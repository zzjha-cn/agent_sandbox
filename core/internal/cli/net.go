package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sandx/internal/config"
	"sandx/internal/proxy"
)

func (a *App) netCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "net", Short: "Inspect and adjust what tasks can reach on the network"}
	cmd.AddCommand(a.netDeniedCmd(), a.netAllowCmd())
	return cmd
}

func (a *App) netAllowCmd() *cobra.Command {
	var project bool
	cmd := &cobra.Command{
		Use:   "allow <host>...",
		Short: "Add hosts to the allowlist (~/.sbx/config.toml, or --project) and reload running tasks",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			path := filepath.Join(a.Home, "config.toml")
			trustedBefore := project && a.trusted()
			if project {
				path = filepath.Join(a.WS.Root, ".sbx", "sandbox.toml")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
			}
			added, err := config.AddAllow(path, args)
			if err != nil {
				return err
			}
			if len(added) == 0 {
				fmt.Fprintln(a.Out, "都已经在白名单里了，没有改动")
				return nil
			}
			fmt.Fprintf(a.Out, "已加入 %s：%s\n", path, strings.Join(added, "、"))
			if project {
				fmt.Fprintln(a.Out, "这是项目层配置，提交进仓库之后队友 clone 下来就有")
				a.retrust(trustedBefore)
			}
			// 重新读四层，下面下发的是新名单
			if err := a.applyLayers(config.Paths(a.Home, a.WS.Root, a.WS.ID)); err != nil {
				return err
			}
			n, err := a.reloadAllow()
			if err != nil {
				return err
			}
			if n > 0 {
				fmt.Fprintf(a.Out, "已对 %d 个运行中的 Task 热加载，不用重启\n", n)
			}
			if a.Cfg.Network.Mode == "open" {
				fmt.Fprintln(a.Out, "提示：当前 network.mode = \"open\"，本来就不拦截；白名单只在 allowlist 模式下起作用")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&project, "project", false, "write to <repo>/.sbx/sandbox.toml instead, so the whole team gets it")
	return cmd
}

// reloadAllow 把新的白名单下发给当前 Workspace 里所有运行中的 Task，并让 squid 热加载。
func (a *App) reloadAllow() (int, error) {
	names, err := a.taskNames()
	if err != nil {
		return 0, err
	}
	p := a.proxy()
	n := 0
	for _, name := range names {
		t, err := a.task(name)
		if err != nil {
			continue
		}
		st, exists, err := a.Docker.Inspect(t.Container())
		if err != nil || !exists || !st.Running {
			continue
		}
		meta, ok, _ := t.ReadMeta()
		taskID := t.ID()
		if ok && meta.TaskID != "" {
			taskID = meta.TaskID
		}
		if _, err := p.AttachTask(a.proxySpec(t, taskID)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (a *App) netDeniedCmd() *cobra.Command {
	var all bool
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "denied [task]",
		Short: "List the hosts the proxy denied (omit task to cover every task in this workspace)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			// 日志里的用户名是 TaskID（<ws>.<task>）。省略 task 时按 ws 前缀过滤。
			taskID, prefix := "", a.WS.ID+"."
			if len(args) == 1 {
				t, err := a.task(args[0])
				if err != nil {
					return err
				}
				taskID = t.ID()
			}
			entries, err := a.proxy().AccessLog()
			if err != nil {
				return fmt.Errorf("读不到 sbx-proxy 的日志（代理没在运行？）：%w", err)
			}
			var from time.Time
			if since > 0 {
				from = time.Now().Add(-since)
			}
			rows := proxy.SummarizeDenied(filterWS(entries, taskID, prefix), taskID, from, proxy.PolicyList())
			return a.printDenied(rows, all, since)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also show policy blocks, known telemetry and auth failures")
	cmd.Flags().DurationVar(&since, "since", 0, "only look this far back, e.g. 2h (default: everything)")
	return cmd
}

// filterWS 在没有指定 task 时，把范围限制在当前 Workspace 的 Task 上。
func filterWS(entries []proxy.Entry, taskID, prefix string) []proxy.Entry {
	if taskID != "" {
		return entries
	}
	out := entries[:0:0]
	for _, e := range entries {
		if len(e.TaskID) > len(prefix) && e.TaskID[:len(prefix)] == prefix {
			out = append(out, e)
		}
	}
	return out
}

func (a *App) printDenied(rows []proxy.DeniedHost, all bool, since time.Duration) error {
	var shown []proxy.DeniedHost
	hidden := map[proxy.Kind]int{}
	notAllowed := 0
	for _, r := range rows {
		if r.Kind == proxy.KindNotAllowed {
			notAllowed++
		}
		if all || r.Kind == proxy.KindNotAllowed {
			shown = append(shown, r)
		} else {
			hidden[r.Kind] += r.Count
		}
	}
	scope := "全部时间"
	if since > 0 {
		scope = "最近 " + since.String()
	}
	if len(shown) == 0 {
		fmt.Fprintf(a.Out, "没有被拒的请求（%s）\n", scope)
	} else {
		w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "HOST\tCOUNT\tLAST\tKIND\tTASK")
		for _, r := range shown {
			task := "-"
			if len(r.Tasks) > 0 {
				task = r.Tasks[0]
				if len(r.Tasks) > 1 {
					task = fmt.Sprintf("%s 等 %d 个", task, len(r.Tasks))
				}
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", r.Host, r.Count, humanAgo(time.Since(r.Last)), r.Kind, task)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	for _, k := range []proxy.Kind{proxy.KindPolicy, proxy.KindAuth} {
		if n := hidden[k]; n > 0 {
			fmt.Fprintf(a.Out, "另有 %s %d 次（--all 查看）\n", k, n)
		}
	}
	// 只有"不在白名单"那一类才需要你决定放不放行；策略拦截和认证失败不给这个建议。
	if notAllowed > 0 {
		fmt.Fprintln(a.Out, "要放行：sbx net allow <host>（写进 ~/.sbx/config.toml 并热加载）")
	}
	return nil
}
