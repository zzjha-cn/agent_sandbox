package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

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
	in, err := a.imageInputs()
	if err != nil {
		return memory.Sandbox{}, err
	}
	if ok, err := a.Docker.ImageExists(in.Tag()); err != nil || !ok {
		return memory.Sandbox{}, fmt.Errorf("沙箱镜像还不存在（还没有 sbx run 过）：%v", err)
	}
	return memory.OneShot(a.Docker, in.Tag()), nil
}

type pullPlan struct {
	hostDir, basePath string
	src, dst          memory.Files
	acts              []memory.Action
	next              memory.Base
}

// pullPlan 计算沙箱 → 宿主机方向的记忆同步计划。
func (a *App) pullPlan() (pullPlan, error) {
	key, hostDir, basePath := a.memoryPaths()
	p := pullPlan{hostDir: hostDir, basePath: basePath}
	sb, err := a.sandboxForPull()
	if err != nil {
		return p, err
	}
	if p.src, err = sb.Read(memory.SandboxDir(key)); err != nil {
		return p, err
	}
	if p.dst, err = memory.ReadDir(hostDir); err != nil {
		return p, err
	}
	base, err := memory.LoadBase(basePath)
	if err != nil {
		return p, err
	}
	p.acts, p.next = memory.Plan(p.src, p.dst, base)
	return p, nil
}

// remindPull 在沙箱里有还没导回的记忆时提醒一句（sbx done 前调用，失败不影响 done）。
func (a *App) remindPull() {
	p, err := a.pullPlan()
	if err != nil {
		return
	}
	n, conflicts := len(memory.Changes(p.acts)), 0
	for _, x := range p.acts {
		if x.Kind == "conflict" {
			conflicts++
		}
	}
	if n+conflicts == 0 {
		return
	}
	msg := fmt.Sprintf("提示: 沙箱里有 %d 个项目记忆文件还没导回宿主机", n)
	if conflicts > 0 {
		msg += fmt.Sprintf("（另有 %d 个两边都改过）", conflicts)
	}
	a.logf("%s；记忆保存在 sbx-home 里，done 不会删除，随时可以 sbx memory pull 查看并导回", msg)
}

// showDiff 用系统的 diff -u 显示两份内容的差异；oldLabel/newLabel 是 diff 头上的两行。
func showDiff(name, oldLabel, newLabel string, old, new []byte, out io.Writer) {
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
	cmd := exec.Command("diff", "-u", "--label", oldLabel, "--label", newLabel, o, n)
	cmd.Stdout = out
	cmd.Run()
}

func (a *App) memoryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "memory", Short: "Claude project memory between your host and the sandbox (ADR 0016)"}
	var yes bool
	pull := &cobra.Command{
		Use:   "pull",
		Short: "Bring memory written in the sandbox back to the host (shows the diff, then asks)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			p, err := a.pullPlan()
			if err != nil {
				return err
			}
			hostDir, basePath, src, dst, acts, next := p.hostDir, p.basePath, p.src, p.dst, p.acts, p.next
			changes := memory.Changes(acts)
			fmt.Fprintf(a.Out, "项目记忆：沙箱 → 宿主机 %s\n", hostDir)
			for _, x := range acts {
				switch x.Kind {
				case "add", "update", "merge":
					fmt.Fprintf(a.Out, "\n[%s] %s\n", x.Kind, x.File)
					showDiff(x.File, "宿主机/"+x.File, "沙箱/"+x.File, dst[x.File], x.Data, os.Stdout)
				case "conflict":
					fmt.Fprintf(a.Out, "\n[conflict] %s：两边都改过，不导回；差异如下，请手动处理\n", x.File)
					showDiff(x.File, "宿主机/"+x.File, "沙箱/"+x.File, dst[x.File], src[x.File], os.Stdout)
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
	pull.Flags().BoolVarP(&yes, "yes", "y", false, "write without asking")
	cmd.AddCommand(pull)
	return cmd
}
