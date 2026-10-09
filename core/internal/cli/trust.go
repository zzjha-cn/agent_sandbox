package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/trust"
)

func (a *App) trustCmd() *cobra.Command {
	var yes, show bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Review what this repo's .sbx/ contains and record it as trusted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			cur, old, had, err := a.trustState()
			if err != nil {
				return err
			}
			path := trust.Path(a.Home, a.WS.ID)
			if cur.Empty() && !had {
				fmt.Fprintf(a.Out, "this repo has no %s/, nothing to confirm.\n", trust.Dir)
				return nil
			}
			if had && old.Hash == cur.Hash {
				fmt.Fprintf(a.Out, "%s/ is trusted (%s, confirmed %s)\n", trust.Dir, cur.Hash[:12], old.TrustedAt.Format("2006-01-02 15:04"))
				return nil
			}
			a.printTrustChanges(a.Out, cur, old, had)
			if show {
				fmt.Fprintf(a.Out, "\n(--show only looks, it does not record. Run sbx trust once it looks right)\n")
				return nil
			}
			if !yes {
				fmt.Fprint(a.Out, "\nRecord the above as trusted? [y/N] ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(line)) != "y" {
					fmt.Fprintln(a.Out, "Cancelled.")
					return nil
				}
			}
			if err := trust.Save(path, cur); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "trusted %s/ (%s), recorded in %s\n", trust.Dir, cur.Hash[:12], path)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "record it without asking")
	cmd.Flags().BoolVar(&show, "show", false, "only show what changed, do not record anything")
	return cmd
}

func (a *App) trustState() (cur, old trust.Snapshot, had bool, err error) {
	cur, err = trust.Scan(a.WS.Root)
	if err != nil {
		return
	}
	old, had, err = trust.Load(trust.Path(a.Home, a.WS.ID))
	return
}

// printTrustChanges 第一次信任时把 .sbx/ 的内容整个列出来；之后只列变更。
func (a *App) printTrustChanges(w io.Writer, cur, old trust.Snapshot, had bool) {
	if !had {
		fmt.Fprintf(w, "This repo is using sbx for the first time. Here is what %s/ contains (it affects how the sandbox runs):\n", trust.Dir)
	} else {
		fmt.Fprintf(w, "%s/ has changed since you last trusted it:\n", trust.Dir)
	}
	for _, c := range trust.Diff(old, cur) {
		fmt.Fprintf(w, "\n[%s] %s/%s", c.Kind, trust.Dir, c.Path)
		if c.Note != "" {
			fmt.Fprintf(w, "（%s）", c.Note)
		}
		fmt.Fprintln(w)
		if c.Old == nil && c.New == nil {
			continue
		}
		oldLabel, newLabel := "trusted/"+c.Path, "now/"+c.Path
		if c.Old == nil {
			oldLabel = "(did not exist)"
		}
		if c.New == nil {
			newLabel = "(deleted)"
		}
		showDiff(c.Path, oldLabel, newLabel, c.Old, c.New, w)
	}
}

// requireTrust 是 sbx run 前的闸门。.sbx/ 变了就打印变更并拒绝，
// 已经在跑的 Task 不受影响（这里只拦 run，不去动容器）。
func (a *App) requireTrust() error {
	cur, old, had, err := a.trustState()
	if err != nil {
		return err
	}
	if cur.Empty() && !had {
		return nil
	}
	if had && old.Hash == cur.Hash {
		return nil
	}
	a.printTrustChanges(a.Err, cur, old, had)
	if !had {
		return fmt.Errorf("%s/ has not been confirmed. Review the above, then run: sbx trust", trust.Dir)
	}
	return fmt.Errorf("%s/ has changed since you last trusted it. Review the changes above, then run: sbx trust", trust.Dir)
}

// retrust 在 sbx 自己改写 .sbx/ 之后（net allow --project）顺手更新信任记录：
// 这次改动是用户自己让 sbx 做的，不该反过来拦住他下一次 run。
// 只在改之前本来就是已信任状态时才这么做——否则会把别人留下的、
// 用户还没看过的改动一起悄悄放行。
func (a *App) retrust(trustedBefore bool) {
	if !trustedBefore {
		return
	}
	cur, err := trust.Scan(a.WS.Root)
	if err != nil {
		return
	}
	if err := trust.Save(trust.Path(a.Home, a.WS.ID), cur); err != nil {
		a.logf("warning: failed to update the trust record; run sbx trust once before the next sbx run: %v", err)
	}
}

// trusted 报告 .sbx/ 当前是否处于已信任状态（没有 .sbx/ 也算）。
func (a *App) trusted() bool {
	cur, old, had, err := a.trustState()
	if err != nil {
		return false
	}
	return (cur.Empty() && !had) || (had && old.Hash == cur.Hash)
}
