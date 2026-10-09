package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sandx/internal/config"
	"sandx/internal/proxy"
	"sandx/internal/task"
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
				fmt.Fprintln(a.Out, "all of them are already in the allowlist; nothing changed")
				return nil
			}
			fmt.Fprintf(a.Out, "added to %s: %s\n", path, strings.Join(added, ", "))
			if project {
				fmt.Fprintln(a.Out, "this is project-layer config; commit it and your teammates get it when they clone")
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
				fmt.Fprintf(a.Out, "hot-reloaded into %d running task(s); no restart needed\n", n)
			}
			if a.Cfg.Network.Mode == "open" {
				fmt.Fprintln(a.Out, "note: network.mode is currently \"open\", so nothing is blocked anyway; the allowlist only applies in allowlist mode")
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
		if _, err := a.egress(t).AttachTask(a.proxySpec(t, a.taskID(t))); err != nil {
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
			var only *task.Task
			if len(args) == 1 {
				t, err := a.task(args[0])
				if err != nil {
					return err
				}
				taskID, only = a.taskID(t), &t
			}
			entries, err := a.accessLogs(only)
			if err != nil {
				return err
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
	scope := "all time"
	if since > 0 {
		scope = "the last " + since.String()
	}
	if len(shown) == 0 {
		fmt.Fprintf(a.Out, "no denied requests (%s)\n", scope)
	} else {
		// KIND 列是中文，用按显示宽度对齐的表
		var rows [][]string
		for _, r := range shown {
			task := "-"
			if len(r.Tasks) > 0 {
				task = r.Tasks[0]
				if len(r.Tasks) > 1 {
					task = fmt.Sprintf("%s and %d more", task, len(r.Tasks))
				}
			}
			rows = append(rows, []string{r.Host, fmt.Sprint(r.Count), humanAgo(time.Since(r.Last)), string(r.Kind), task})
		}
		if err := writeTable(a.Out, []string{"HOST", "COUNT", "LAST", "KIND", "TASK"}, rows); err != nil {
			return err
		}
	}
	for _, k := range []proxy.Kind{proxy.KindPolicy, proxy.KindAuth} {
		if n := hidden[k]; n > 0 {
			fmt.Fprintf(a.Out, "%s: %d more (use --all to see them)\n", k, n)
		}
	}
	// 只有"不在白名单"那一类才需要你决定放不放行；策略拦截和认证失败不给这个建议。
	if notAllowed > 0 {
		fmt.Fprintln(a.Out, "to allow one: sbx net allow <host> (writes it to ~/.sbx/config.toml and hot-reloads)")
	}
	return nil
}
