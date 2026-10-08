// Package cli 实现 sbx 的命令行（cobra）。
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/proxy"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

// App 持有一次命令执行的公共依赖。
type App struct {
	Verbose bool
	Out     io.Writer
	Err     io.Writer
	Docker  *docker.Client

	Home string
	Cfg  config.Config
	WS   workspace.Workspace
}

// Execute 是 main 的入口。
func Execute() int {
	app := &App{Out: os.Stdout, Err: os.Stderr}
	root := &cobra.Command{
		Use:           "sbx",
		Short:         "在容器沙箱里运行 Claude Code / Codex 的 Task 运行时",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			app.Docker = docker.New(app.Verbose)
			app.Docker.Log = app.Err
			if app.Verbose {
				workspace.Trace = app.Err
			}
		},
	}
	root.PersistentFlags().BoolVarP(&app.Verbose, "verbose", "v", false, "打印执行的 docker 和 git 命令")
	root.AddCommand(app.runCmd(), app.attachCmd(), app.shellCmd(), app.stopCmd(), app.lsCmd(), app.pathCmd(), app.doneCmd(), app.memoryCmd(), app.upgradeCmd(), app.netCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(app.Err, "sbx:", err)
		return 1
	}
	return 0
}

// load 解析 Workspace 和配置。
func (a *App) load() error {
	if err := a.loadConfig(); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	a.WS, err = workspace.Resolve(cwd)
	return err
}

// loadConfig 只读全局配置，不要求在 git 仓库里。
func (a *App) loadConfig() error {
	home, err := task.Home()
	if err != nil {
		return err
	}
	a.Home = home
	cfg, warnings, err := config.Load(filepath.Join(home, "config.toml"))
	for _, w := range warnings {
		fmt.Fprintln(a.Err, "警告:", w)
	}
	if err != nil {
		return err
	}
	a.Cfg = cfg
	return nil
}

func (a *App) task(name string) (task.Task, error) {
	if name == "" {
		name = "main"
	}
	return task.New(a.Home, a.WS, name)
}

func (a *App) proxy() proxy.Shared {
	host, port, _ := a.Cfg.Upstream()
	return proxy.Shared{Docker: a.Docker, Dir: filepath.Join(a.Home, "proxy"), UpstreamHost: host, UpstreamPort: port}
}

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(a.Err, format+"\n", args...)
}
