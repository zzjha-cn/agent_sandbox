// Package cli 实现 sbx 的命令行（cobra）。
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/execution"
	"sandx/internal/protocol"
	"sandx/internal/proxy"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

// Version 是 sbx 自己的版本，由 main 在启动时注入（make release 打进去）。
var Version = "dev"

// App 持有一次命令执行的公共依赖。
type App struct {
	Verbose bool
	Out     io.Writer
	Err     io.Writer
	Docker  *docker.Client

	In_    io.Reader // 测试里替换；为空时用 os.Stdin
	Home   string
	Cfg    config.Config
	Loaded config.Loaded // 分层结果：每个值来自哪一层（sbx config show）
	WS     workspace.Workspace
}

// In 是确认提示读输入的地方。
func (a *App) In() io.Reader {
	if a.In_ != nil {
		return a.In_
	}
	return os.Stdin
}

// Execute 是 main 的入口。
func Execute() int {
	app := &App{Out: os.Stdout, Err: os.Stderr}
	root := &cobra.Command{
		Use:           "sbx",
		Short:         "Run Claude Code / Codex tasks in container sandboxes",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			app.Docker = docker.New(app.Verbose)
			app.Docker.Log = app.Err
			if app.Verbose {
				workspace.Trace = app.Err
			}
			top := cmd
			for top.Parent() != nil && top.Parent() != rootCommand(cmd) {
				top = top.Parent()
			}
			switch top.Name() {
			case "run", "admit", "version", "execution", "task", "config", "trust", "isolation", "doctor", "completion", "help", "sbx":
				// Run/admit validate the envelope first and perform the same
				// ownership check in execution admission; inspection stays read-only.
				return nil
			}
			if err := app.loadConfig(); err != nil {
				return err
			}
			readOnly := top.Name() == "ls" || top.Name() == "path" || top.Name() == "logs"
			return app.checkIsolation(cmd.Context(), !readOnly)
		},
	}
	root.PersistentFlags().BoolVarP(&app.Verbose, "verbose", "v", false, "print every docker and git command sbx runs")
	root.AddCommand(app.runCmd(), app.admitCmd(), app.versionCmd(), app.executionCmd(), app.taskCmd(), app.isolationCmd(), app.attachCmd(), app.shellCmd(), app.stopCmd(), app.lsCmd(), app.pathCmd(), app.doneCmd(), app.dropCmd(), app.portCmd(), app.logsCmd(), app.memoryCmd(), app.loginCmd(), app.configCmd(), app.trustCmd(), app.upgradeCmd(), app.netCmd(), app.prepCmd(), app.forwardCmd(), app.cleanCmd(), app.doctorCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(app.Err, "sbx:", err)
		return exitCode(err)
	}
	return 0
}

func rootCommand(cmd *cobra.Command) *cobra.Command {
	for cmd.Parent() != nil {
		cmd = cmd.Parent()
	}
	return cmd
}

func exitCode(err error) int {
	var coded *protocol.CodedError
	if errors.As(err, &coded) {
		if coded.Code == "runner_error" {
			return 4
		}
		return 2
	}
	var runErr *execution.ExecutionError
	if errors.As(err, &runErr) {
		switch runErr.Kind {
		case execution.AdmissionRejected:
			return 3
		case execution.AgentRunFailed:
			return 5
		case execution.DeliveryFailedKind:
			return 6
		case execution.CancelledKind:
			return 130
		case execution.TimedOutKind:
			return 124
		case execution.AgentStartFailed, execution.EnvironmentFailed:
			return 4
		case execution.InternalFailed:
			return 7
		case execution.VerificationFailed:
			return 8
		case execution.ReviewRequired:
			return 9
		case execution.ProductDeliveryFailed:
			return 10
		case execution.ProductOutcomeUnknown:
			return 11
		}
	}
	return 1
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

// locate 只找出 ~/.sbx 和当前 Workspace，不读也不校验任何配置文件。
// 写模板的命令用它：配置坏掉的时候，最该还能用的恰恰是「再给我一份模板」——
// 用 load() 的话，一个手滑写坏的 sandbox.toml 会把修它的工具一起锁在门外。
func (a *App) locate() error {
	if err := a.setHome(); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	a.WS, err = workspace.Resolve(cwd)
	return err
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
		fmt.Fprintln(a.Err, "warning:", w)
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
	t, err := task.New(a.Home, a.WS, name)
	if err != nil {
		return t, err
	}
	if a.Docker != nil && len(a.Docker.ResourceLabels) > 0 {
		meta, ok, err := t.ReadMeta()
		if err != nil {
			return t, err
		}
		if ok && ((meta.Namespace != "" && meta.Namespace != a.Docker.ResourceLabels["sbx.namespace"]) || (meta.Owner != "" && meta.Owner != a.Docker.ResourceLabels["sbx.owner"])) {
			return t, &execution.ExecutionError{Kind: execution.AdmissionRejected, Stage: "task isolation", Cause: fmt.Errorf("task ownership does not match this daemon/operator")}
		}
	}
	return t, nil
}

func (a *App) proxy() proxy.Shared {
	host, port, _ := a.Cfg.Upstream()
	return proxy.Shared{Docker: a.Docker, Dir: filepath.Join(a.Home, "proxy"), UpstreamHost: host, UpstreamPort: port}
}

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(a.Err, format+"\n", args...)
}
