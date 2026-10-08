package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"sandx/internal/config"
	"sandx/internal/workspace"
)

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect the effective configuration"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print every effective value and which layer it came from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 仓库外也能用：那时只有默认值和全局层。注意不能拿 load() 的失败当
			// "不在仓库里"——配置本身非法也会让它失败，吞掉就成了静默降级。
			if err := a.setHome(); err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ws, wsErr := workspace.Resolve(cwd)
			layers := config.Paths(a.Home, "", "")
			if wsErr != nil {
				fmt.Fprintln(a.Err, "提示: 不在 git 仓库里，只显示默认值和全局层")
			} else {
				a.WS = ws
				layers = config.Paths(a.Home, ws.Root, ws.ID)
			}
			if err := a.applyLayers(layers); err != nil {
				return err
			}
			// 层名是中文，tabwriter 按字节算宽度会错位，这张表自己对齐。
			fmt.Fprintf(a.Out, "%s  (内置)\n", padCJK(config.LayerDefault, 8))
			for _, l := range a.Loaded.Layers {
				note := ""
				if !l.Found {
					note = "  ← 没有这个文件"
				}
				fmt.Fprintf(a.Out, "%s  %s%s\n", padCJK(l.Name, 8), tildePath(l.Path, a.homeDir()), note)
			}
			fmt.Fprintln(a.Out)

			w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "KEY\tVALUE\tFROM")
			for _, r := range a.Loaded.Show() {
				fmt.Fprintf(w, "%s\t%s\t%s\n", r[0], r[1], r[2])
			}
			return w.Flush()
		},
	})
	return cmd
}
