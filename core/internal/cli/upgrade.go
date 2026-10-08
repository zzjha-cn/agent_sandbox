package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/fsutil"
	"sandx/internal/image"
)

// claudeVersionFile 记录 sbx upgrade 解析出的 claude 版本；配置为 latest 时用它固定镜像。
func (a *App) claudeVersionFile() string {
	return filepath.Join(a.Home, "claude-version")
}

// claudeVersion 是构建镜像用的 claude 版本：配置里写死的版本优先，
// 否则用 sbx upgrade 记录的版本，都没有时是 latest。
func (a *App) claudeVersion() string {
	if v := a.Cfg.ClaudeVersion(); v != "latest" {
		return v
	}
	if b, err := os.ReadFile(a.claudeVersionFile()); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return "latest"
}

func (a *App) imageInputs() (image.Inputs, error) {
	return image.BuiltinInputs(a.Cfg.Profile, a.claudeVersion(), os.Getuid(), os.Getgid())
}

var semver = regexp.MustCompile(`^\d+\.\d+\.\d+\S*$`)

// latestClaude 在 Profile 镜像里用 npm 查询 claude-code 的最新版本（走上游代理）。
func (a *App) latestClaude(b image.Builder, in image.Inputs) (string, error) {
	ptag, err := b.EnsureProfile(in)
	if err != nil {
		return "", err
	}
	args := []string{"run", "--rm"}
	if up := a.Cfg.Network.Upstream; up != "" {
		args = append(args, "-e", "HTTPS_PROXY="+up, "-e", "HTTP_PROXY="+up)
	}
	args = append(args, ptag, "npm", "view", "@anthropic-ai/claude-code", "version")
	out, err := a.Docker.Run(args...)
	if err != nil {
		return "", fmt.Errorf("查询 claude-code 最新版本失败：%w", err)
	}
	v := strings.TrimSpace(out)
	if !semver.MatchString(v) {
		return "", fmt.Errorf("npm 返回了无法识别的版本：%q", v)
	}
	return v, nil
}

func (a *App) upgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade claude in the sandbox to the latest version (rebuilds the agent layer; new tasks pick it up)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.loadConfig(); err != nil {
				return err
			}
			if v := a.Cfg.ClaudeVersion(); v != "latest" {
				return fmt.Errorf("配置里固定了 agents.claude.version = %q；要升级请改配置", v)
			}
			in, err := a.imageInputs()
			if err != nil {
				return err
			}
			b := image.Builder{Docker: a.Docker, Upstream: a.Cfg.Network.Upstream, Out: a.Err}
			cur := a.claudeVersion()
			latest, err := a.latestClaude(b, in)
			if err != nil {
				return err
			}
			in.ClaudeVersion = latest
			if latest == cur {
				if ok, err := a.Docker.ImageExists(in.Tag()); err == nil && ok {
					fmt.Fprintf(a.Out, "claude 已是最新版 %s（镜像 %s）\n", latest, in.Tag())
					return nil
				}
			}
			tag, err := b.Ensure(in)
			if err != nil {
				return err
			}
			if err := fsutil.AtomicWrite(a.claudeVersionFile(), []byte(latest+"\n"), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "claude %s → %s，镜像 %s\n", cur, latest, tag)
			fmt.Fprintln(a.Out, "之后新建的 Task 使用新镜像；已有 Task 仍是旧镜像，sbx done 后重新 run 即可换上。")
			return nil
		},
	}
}
