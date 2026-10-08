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

	Home   string
	Cfg    config.Config
	Loaded config.Loaded // 分层结果：每个值来自哪一层（sbx config show）
	WS     workspace.Workspace
}

// Execute 是 main 的入口。
func Execute() int {
	app := &App{Out: os.Stdout, Err: os.Stderr}
	root := &cobra.Command{
		Use:           "sbx",
		Short:         "Run Claude Code / Codex tasks in container sandboxes",
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
	root.PersistentFlags().BoolVarP(&app.Verbose, "verbose", "v", false, "print every docker and git command sbx runs")
	root.AddCommand(app.runCmd(), app.attachCmd(), app.shellCmd(), app.stopCmd(), app.lsCmd(), app.pathCmd(), app.doneCmd(), app.memoryCmd(), app.loginCmd(), app.configCmd(), app.trustCmd(), app.upgradeCmd(), app.netCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(app.Err, "sbx:", err)
		return 1
	}
	return 0
}

// load 解析 Workspace，然后按四层合并配置（design §9.1）。
func (a *App) load() error {
	if err := a.setHome(); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	a.WS, err = workspace.Resolve(cwd)
	if err != nil {
		return err
	}
	return a.applyLayers(config.Paths(a.Home, a.WS.Root, a.WS.ID))
}

// loadConfig 只合并默认值和全局层，不要求在 git 仓库里（login、upgrade 用）。
func (a *App) loadConfig() error {
	if err := a.setHome(); err != nil {
		return err
	}
	return a.applyLayers(config.Paths(a.Home, "", ""))
}

func (a *App) setHome() error {
	home, err := task.Home()
	if err != nil {
		return err
	}
	a.Home = home
	return nil
}

func (a *App) applyLayers(layers []config.Layer) error {
	ld, err := config.LoadLayers(layers)
	for _, w := range ld.Warnings {
		fmt.Fprintln(a.Err, "警告:", w)
	}
	if err != nil {
		return err
	}
	a.Cfg, a.Loaded = ld.Config, ld
	return nil
}

// homeDir 是宿主机的 ~，只用于把路径缩写成 ~/… 打印。
func (a *App) homeDir() string {
	h, _ := os.UserHomeDir()
	return h
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
