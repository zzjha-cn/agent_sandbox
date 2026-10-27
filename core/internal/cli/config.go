package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"sandx/internal/config"
	"sandx/internal/workspace"
)

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect the effective configuration, or write a template for it"}
	cmd.AddCommand(a.configInitCmd())
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
				fmt.Fprintln(a.Err, "note: not inside a git repository; showing only the defaults and the global layer")
			} else {
				a.WS = ws
				layers = config.Paths(a.Home, ws.Root, ws.ID)
			}
			if err := a.applyLayers(layers); err != nil {
				return err
			}
			// prep 镜像里的声明也是一层（M4-15）。读不到就说一句，别让 config show
			// 因为一个可选的镜像而失败——但也不能静悄悄地少显示一层。
			if wsErr == nil {
				if err := a.checkIsolation(cmd.Context(), false); err != nil {
					fmt.Fprintln(a.Err, "note: prep declarations omitted because daemon isolation has not passed:", err)
				} else if m, err := a.prepDeclared(); err != nil {
					fmt.Fprintln(a.Err, "note: cannot read the prep image's declarations:", err)
				} else if m != nil {
					if err := a.Loaded.MergePrep(m.Allow, m.Env); err != nil {
						return err
					}
					a.Cfg = a.Loaded.Config
				}
			}
			// 路径里可能有 CJK，tabwriter 按字节算宽度会错位，这张表自己对齐。
			fmt.Fprintf(a.Out, "%s  (built-in)\n", padCJK(config.LayerDefault, 9))
			for _, l := range a.Loaded.Layers {
				note := ""
				if !l.Found {
					note = "  <- no such file"
				}
				fmt.Fprintf(a.Out, "%s  %s%s\n", padCJK(l.Name, 9), tildePath(l.Path, a.homeDir()), note)
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

// configInitCmd 写一份全是注释的项目层模板。全是注释是刻意的：写一个模板文件不该
// 改变任何行为，每一条都得是你自己打开的。
func (a *App) configInitCmd() *cobra.Command {
	var force, print bool
	var lang string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented <repo>/.sbx/sandbox.toml template (the project layer)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if print {
				b, err := templateAsset(projectTemplate, lang)
				if err != nil {
					return err
				}
				_, err = a.Out.Write(b)
				return err
			}
			if err := a.locate(); err != nil {
				return err
			}
			p := filepath.Join(a.WS.Root, ".sbx", "sandbox.toml")
			// 信任记录要在改之前问：这次改动是用户让 sbx 做的，不该反过来拦住他
			// 下一次 run——但只在本来就已信任时才顺手更新（见 retrust）。
			trustedBefore := a.trusted()
			if err := writeTemplate(p, projectTemplate, lang, force); err != nil {
				return err
			}
			a.retrust(trustedBefore)
			fmt.Fprintf(a.Out, "wrote %s\n", p)
			fmt.Fprintln(a.Out, "every line is commented out; uncomment only what this repo needs, then:")
			fmt.Fprintln(a.Out, "  sbx config show          # which layer each effective value came from")
			fmt.Fprintln(a.Out, "this is project-layer config; commit it and your teammates get it when they clone.")
			fmt.Fprintln(a.Out, "[[forward]] cannot live here -- run sbx forward init for that one.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	cmd.Flags().BoolVar(&print, "print", false, "print the template instead of writing it")
	langFlag(cmd, &lang)
	return cmd
}
