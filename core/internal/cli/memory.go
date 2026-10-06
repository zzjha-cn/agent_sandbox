package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/image"
	"sandx/internal/memory"
)

// hostClaudeDir 是宿主机的 ~/.claude；测试时可用 SBX_HOST_CLAUDE 覆盖。
func hostClaudeDir() string {
	if d := os.Getenv("SBX_HOST_CLAUDE"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func (a *App) memoryPaths() (key, hostDir, basePath string) {
	key = memory.Key(a.WS.Root)
	return key, memory.HostDir(hostClaudeDir(), key), filepath.Join(a.Home, "memory", key+".json")
}

func summarize(acts []memory.Action) string {
	count := map[string]int{}
	for _, x := range acts {
		count[x.Kind]++
	}
	var parts []string
	for _, k := range []string{"add", "update", "merge", "keep-dst", "conflict", "skip-deleted"} {
		if count[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, count[k]))
		}
	}
	return strings.Join(parts, "，")
}

// importMemory 把宿主机的项目记忆导入沙箱（ADR 0016），在拉起 claude 之前调用。
func (a *App) importMemory(container string) error {
	key, hostDir, basePath := a.memoryPaths()
	src, err := memory.ReadDir(hostDir)
	if err != nil || len(src) == 0 {
		return err
	}
	sb := memory.ExecIn(a.Docker, container)
	dst, err := sb.Read(memory.SandboxDir(key))
	if err != nil {
		return err
	}
	base, err := memory.LoadBase(basePath)
	if err != nil {
		return err
	}
	acts, next := memory.Plan(src, dst, base)
	if err := sb.Write(memory.SandboxDir(key), memory.Changes(acts)); err != nil {
		return err
	}
	for _, x := range acts {
		if x.Kind == "conflict" {
			a.logf("记忆 %s 在宿主机和沙箱里都改过，保留沙箱的版本；用 sbx memory pull 查看", x.File)
		}
	}
	if s := summarize(acts); s != "" {
		a.logf("导入项目记忆：%s", s)
	}
	return memory.SaveBase(basePath, next)
}

// sandboxForPull 优先用本 ws 里运行中的 Task 容器，否则起一个一次性容器挂 sbx-home。
func (a *App) sandboxForPull() (memory.Sandbox, error) {
	items, err := a.Docker.ListByLabel("container", "sbx.ws="+a.WS.ID, "sbx.role=agent")
	if err != nil {
		return memory.Sandbox{}, err
	}
	for _, it := range items {
		if it.Str("State") == "running" {
			return memory.ExecIn(a.Docker, it.Str("Names")), nil
		}
	}
	in, err := image.BuiltinInputs(a.Cfg.Profile, a.Cfg.ClaudeVersion(), os.Getuid(), os.Getgid())
	if err != nil {
		return memory.Sandbox{}, err
	}
	if ok, err := a.Docker.ImageExists(in.Tag()); err != nil || !ok {
		return memory.Sandbox{}, fmt.Errorf("沙箱镜像还不存在（还没有 sbx run 过）：%v", err)
	}
	return memory.OneShot(a.Docker, in.Tag()), nil
}

func showDiff(name string, old, new []byte, out *os.File) {
	dir, err := os.MkdirTemp("", "sbx-mem-*")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	o, n := filepath.Join(dir, "host", name), filepath.Join(dir, "sandbox", name)
	os.MkdirAll(filepath.Dir(o), 0o755)
	os.MkdirAll(filepath.Dir(n), 0o755)
	os.WriteFile(o, old, 0o644)
	os.WriteFile(n, new, 0o644)
	cmd := exec.Command("diff", "-u", "--label", "宿主机/"+name, "--label", "沙箱/"+name, o, n)
	cmd.Stdout = out
	cmd.Run()
}

func (a *App) memoryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "memory", Short: "宿主机和沙箱之间的 Claude 项目记忆（ADR 0016）"}
	var yes bool
	pull := &cobra.Command{
		Use:   "pull",
		Short: "把沙箱里新记的项目记忆导回宿主机（先展示差异，确认后写入）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			key, hostDir, basePath := a.memoryPaths()
			sb, err := a.sandboxForPull()
			if err != nil {
				return err
			}
			src, err := sb.Read(memory.SandboxDir(key))
			if err != nil {
				return err
			}
			dst, err := memory.ReadDir(hostDir)
			if err != nil {
				return err
			}
			base, err := memory.LoadBase(basePath)
			if err != nil {
				return err
			}
			acts, next := memory.Plan(src, dst, base)
			changes := memory.Changes(acts)
			fmt.Fprintf(a.Out, "项目记忆：沙箱 → 宿主机 %s\n", hostDir)
			for _, x := range acts {
				switch x.Kind {
				case "add", "update", "merge":
					fmt.Fprintf(a.Out, "\n[%s] %s\n", x.Kind, x.File)
					showDiff(x.File, dst[x.File], x.Data, os.Stdout)
				case "conflict":
					fmt.Fprintf(a.Out, "\n[conflict] %s：两边都改过，不导回；差异如下，请手动处理\n", x.File)
					showDiff(x.File, dst[x.File], src[x.File], os.Stdout)
				case "keep-dst":
					fmt.Fprintf(a.Out, "[keep] %s：宿主机更新，下次 sbx run 时导入沙箱\n", x.File)
				}
			}
			if len(changes) == 0 {
				fmt.Fprintln(a.Out, "\n没有需要导回的内容。")
				return memory.SaveBase(basePath, next)
			}
			if !yes {
				fmt.Fprintf(a.Out, "\n写入以上 %d 个文件？[y/N] ", len(changes))
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(line)) != "y" {
					fmt.Fprintln(a.Out, "已取消。")
					return nil
				}
			}
			if err := memory.WriteDir(hostDir, changes); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "已写入 %d 个文件。\n", len(changes))
			return memory.SaveBase(basePath, next)
		},
	}
	pull.Flags().BoolVarP(&yes, "yes", "y", false, "不询问，直接写入")
	cmd.AddCommand(pull)
	return cmd
}
