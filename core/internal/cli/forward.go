package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

// forwardCmd 现在只有 init 一个子命令。单独立一个顶层命令而不是塞进 config init，
// 是因为落点不同：forward 只能写在你自己机器上的工作区层，项目层整棵子树被红线
// 挡着。命令分开，这件事就不用靠读文档才知道。
func (a *App) forwardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "forward",
		Short: "Single-target TCP holes from a task to one address you name",
		Long: "A forward lets a task reach exactly one address and port -- a database on your machine,\n" +
			"or one on an internal network. The task still has no default route and still reaches the\n" +
			"internet only through the proxy. Because a forward names a real address, it can only be\n" +
			"written on your own machine: the project layer rejects it.",
	}
	cmd.AddCommand(a.forwardInitCmd())
	return cmd
}

func (a *App) forwardInitCmd() *cobra.Command {
	var force, print bool
	var lang string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented [[forward]] template into the workspace layer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if print {
				b, err := templateAsset(forwardTemplate, lang)
				if err != nil {
					return err
				}
				_, err = a.Out.Write(b)
				return err
			}
			if err := a.locate(); err != nil {
				return err
			}
			p := filepath.Join(a.Home, "workspaces", a.WS.ID+".toml")
			if err := writeTemplate(p, forwardTemplate, lang, force); err != nil {
				// 这个文件多半已经有别的设置了，覆盖是会丢东西的操作——
				// 与其让人加 --force 赌一把，不如指一条读了再抄的路。
				return fmt.Errorf("%w\n       or read it first with: sbx forward init --print", err)
			}
			fmt.Fprintf(a.Out, "wrote %s\n", p)
			fmt.Fprintln(a.Out, "this is the workspace layer: it applies to this workspace, on this machine only.")
			fmt.Fprintln(a.Out, "every entry is commented out; read the three risk levels before uncommenting one, then:")
			fmt.Fprintln(a.Out, "  sbx config show          # check it merged the way you expected")
			fmt.Fprintln(a.Out, "  sbx run <task>           # forwards come up with the task")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	cmd.Flags().BoolVar(&print, "print", false, "print the template instead of writing it")
	langFlag(cmd, &lang)
	return cmd
}
