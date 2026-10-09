package cli

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/task"
	"sandx/internal/workspace"
)

func (a *App) dropCmd() *cobra.Command {
	var force, yes bool
	cmd := &cobra.Command{
		Use:   "drop <task>",
		Short: "Finish a task and delete its branch too (asks you to type the task name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.taskArg(args)
			if err != nil {
				return err
			}
			if t.IsMain() {
				return fmt.Errorf("main Task 没有专属分支，用 sbx done 就行")
			}
			if err := a.confirmDrop(t, yes); err != nil {
				return err
			}
			a.remindPull()
			if err := a.done(t, force, true); err != nil {
				return err
			}
			if !t.WS.BranchExists(t.Branch()) {
				fmt.Fprintf(a.Out, "已结束 %s（分支 %s 本来就不存在）\n", t.Name, t.Branch())
				return nil
			}
			if _, err := workspace.Git(t.WS.Root, "branch", "-D", t.Branch()); err != nil {
				return fmt.Errorf("分支 %s 没删掉（Task 已经结束了）：%w", t.Branch(), err)
			}
			fmt.Fprintf(a.Out, "已结束 %s，并删除了分支 %s\n", t.Name, t.Branch())
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "drop it even with uncommitted changes in its worktree")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation (for scripts)")
	return cmd
}

// confirmDrop 要求输入 Task 名确认。drop 会删掉分支，那是这套工具里唯一
// 真正不可逆的操作——worktree 和容器都能重建，commit 删了就没了。
func (a *App) confirmDrop(t task.Task, yes bool) error {
	if yes {
		return nil
	}
	ahead := "?"
	if meta, ok, _ := t.ReadMeta(); ok && meta.Base != "" && t.WS.BranchExists(t.Branch()) {
		if n, err := workspace.Git(t.WS.Root, "rev-list", "--count", meta.Base+".."+t.Branch()); err == nil {
			ahead = n
		}
	}
	fmt.Fprintf(a.Out, "即将删除 Task %s 和它的分支 %s（%s 个提交会一起没掉）。\n", t.Name, t.Branch(), ahead)
	fmt.Fprintf(a.Out, "确认请输入 Task 名（%s）：", t.Name)
	line, err := bufio.NewReader(a.In()).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return fmt.Errorf("没有读到确认，已取消")
	}
	if strings.TrimSpace(line) != t.Name {
		return fmt.Errorf("输入的不是 %q，已取消", t.Name)
	}
	return nil
}
