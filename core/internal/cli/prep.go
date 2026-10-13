package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/config"
	"sandx/internal/docker"
	"sandx/internal/prep"
	"sandx/internal/task"
)

func (a *App) prepCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prep",
		Short: "Freeze private dependencies into an image that new tasks start from",
		Long: "prep runs a docker build on the host, where you still have intranet access and credentials,\n" +
			"and seeds the result into each new task's dependency volumes. The task container itself\n" +
			"needs no private network configuration, and never sees the credentials.",
	}
	cmd.AddCommand(a.prepInitCmd(), a.prepBuildCmd(), a.prepShowCmd(), a.prepReseedCmd(), a.prepDropCmd())
	return cmd
}

// prepSpec 把配置转成 prep.Spec，并检查这个仓库到底配没配 prep。
func (a *App) prepSpec() (prep.Spec, error) {
	s, err := prep.FromConfig(a.Cfg, a.WS.ID, a.WS.Root, a.homeDir())
	if err != nil {
		return s, err
	}
	if !s.Enabled() {
		return s, fmt.Errorf("no %s in this repo; run sbx prep init to create one", a.Cfg.Prep.File)
	}
	return s, nil
}

func (a *App) builder() prep.Builder { return prep.Builder{Docker: a.Docker, Out: a.Out} }

func (a *App) prepInitCmd() *cobra.Command {
	var force bool
	var lang string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented " + "prep.Dockerfile" + " template",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			p := filepath.Join(a.WS.Root, filepath.FromSlash(a.Cfg.Prep.File))
			if err := writeTemplate(p, "prep/prep.Dockerfile.%s.tmpl", lang, force); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "wrote %s\n", p)
			fmt.Fprintln(a.Out, "the contract is in the comments; edit it (or let the agent fill it in), then:")
			fmt.Fprintln(a.Out, "  sbx prep build --check   # report what would be seeded, without touching any volume")
			fmt.Fprintln(a.Out, "  sbx prep build")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	langFlag(cmd, &lang)
	return cmd
}

func (a *App) prepBuildCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the prep image (use --check to report without seeding)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			s, err := a.prepSpec()
			if err != nil {
				return err
			}
			tag, err := a.builder().Build(s)
			if err != nil {
				return err
			}
			plan, err := a.builder().Inspect(s, tag)
			if err != nil {
				return err
			}
			fmt.Fprint(a.Out, plan.Describe())
			// 声明越权要在构建这一步就说，别等到下一次 sbx run 才炸——
			// 这条命令是 prep 的反馈回路，问题越早出现越便宜
			if m := plan.Manifest; m != nil {
				if err := config.CheckPrepManifest(m.Allow, m.Env); err != nil {
					return err
				}
			}
			if check {
				fmt.Fprintln(a.Out, "(--check: nothing was seeded)")
				return nil
			}
			fmt.Fprintf(a.Out, "built %s\n", tag)
			// .sbx/ 变了就要重新确认一次，这是信任模型的入口，不能靠人记得
			if !a.trusted() {
				fmt.Fprintln(a.Out, "note: .sbx/ changed; run sbx trust before the next sbx run")
			}
			fmt.Fprintln(a.Out, "new tasks will seed their dependency volumes from it; existing tasks are untouched")
			fmt.Fprintln(a.Out, "to reseed one anyway: sbx prep reseed <task>")
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "build and report, but do not seed anything")
	return cmd
}

func (a *App) prepShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the current prep image, whether it drifted, and which tasks were seeded from it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			s, err := a.prepSpec()
			if err != nil {
				return err
			}
			tag, err := s.Tag()
			if err != nil {
				return err
			}
			exists, err := a.Docker.ImageExists(tag)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "dockerfile: %s\n", s.File)
			if len(s.Watch) > 0 {
				fmt.Fprintf(a.Out, "watching:   %s\n", strings.Join(s.Watch, ", "))
			}
			fmt.Fprintf(a.Out, "tag:        %s", tag)
			if !exists {
				fmt.Fprint(a.Out, "  <- not built yet (sbx prep build)")
			}
			fmt.Fprintln(a.Out)

			names, err := a.taskNames()
			if err != nil {
				return err
			}
			var rows [][]string
			for _, name := range names {
				t, err := a.task(name)
				if err != nil {
					continue
				}
				m, ok, err := t.ReadMeta()
				if err != nil || !ok {
					continue
				}
				state := "seeded"
				switch {
				case m.PrepTag == "":
					state = "no seed"
				case m.PrepTag != tag:
					state = "stale"
				}
				rows = append(rows, []string{name, state, shortTag(m.PrepTag)})
			}
			if len(rows) == 0 {
				return nil
			}
			fmt.Fprintln(a.Out)
			return writeTable(a.Out, []string{"TASK", "SEED", "FROM"}, rows)
		},
	}
}

func shortTag(tag string) string {
	if tag == "" {
		return "-"
	}
	if _, h, ok := strings.Cut(tag, ":"); ok {
		return h
	}
	return tag
}

func (a *App) prepReseedCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reseed <task>",
		Short: "Re-seed a task's dependency volumes from the current prep image (destructive)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			t, err := a.task(args[0])
			if err != nil {
				return err
			}
			s, err := a.prepSpec()
			if err != nil {
				return err
			}
			tag, err := s.Tag()
			if err != nil {
				return err
			}
			if ok, err := a.Docker.ImageExists(tag); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("%s is not built yet; run sbx prep build first", tag)
			}
			// 播种是覆盖式的：Task 自己装的东西会被这一次抹掉。
			// 正常路径只在 volume 新建时播种，这条命令是显式的例外，所以必须确认。
			if !yes {
				fmt.Fprintf(a.Out, "this overwrites %s's dependency volumes (%s) with the contents of %s.\n",
					t.Name, strings.Join(a.Cfg.Deps.Mask, ", "), tag)
				fmt.Fprintln(a.Out, "anything the agent installed in them is lost.")
				fmt.Fprintf(a.Out, "Type the task name to confirm (%s): ", t.Name)
				line, _ := bufio.NewReader(a.In()).ReadString('\n')
				if strings.TrimSpace(line) != t.Name {
					return fmt.Errorf("that is not %q; cancelled", t.Name)
				}
			}
			st, exists, err := a.Docker.Inspect(t.Container())
			if err != nil {
				return err
			}
			if exists && st.Running {
				return fmt.Errorf("%s is running; stop it first (sbx stop %s) so nothing writes while we replace the volumes", t.Name, t.Name)
			}
			plan, err := a.builder().Inspect(s, tag)
			if err != nil {
				return err
			}
			if err := a.seedTask(t, tag, plan, nil); err != nil {
				return err
			}
			m, ok, err := t.ReadMeta()
			if err == nil && ok {
				m.PrepTag = tag
				t.WriteMeta(m)
			}
			fmt.Fprintf(a.Out, "reseeded %s from %s\n", t.Name, tag)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation (for scripts)")
	return cmd
}

func (a *App) prepDropCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "drop",
		Short: "Remove prep images for this workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.load(); err != nil {
				return err
			}
			imgs, err := a.Docker.Run("images", "-q", "--filter", "label=sbx.kind=prep",
				"--filter", "label=sbx.ws="+a.WS.ID, "--format", "{{.Repository}}:{{.Tag}}")
			if err != nil {
				return err
			}
			var keep string
			if !all {
				if s, err := a.prepSpec(); err == nil {
					keep, _ = s.Tag()
				}
			}
			n := 0
			for _, ref := range strings.Fields(imgs) {
				if ref == keep {
					continue
				}
				if _, err := a.Docker.Run("rmi", ref); err != nil {
					fmt.Fprintf(a.Err, "warning: cannot remove %s: %v\n", ref, err)
					continue
				}
				fmt.Fprintf(a.Out, "removed %s\n", ref)
				n++
			}
			if n == 0 {
				fmt.Fprintln(a.Out, "nothing to remove")
			}
			if keep != "" {
				fmt.Fprintf(a.Out, "kept the current one (%s); use --all to remove it too\n", keep)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also remove the image the current config points at")
	return cmd
}

// seedTask 把 plan 里的内容播种进这个 Task 的 volume 和共享的 sbx-cache。
//
// fresh 是本次新建的 volume 名集合：只播种它们，不覆盖 Task 已经装好的依赖
// （boundary.md §2.6）。fresh 为 nil 表示无条件播种，只有 sbx prep reseed 走这条路。
func (a *App) seedTask(t task.Task, tag string, plan prep.Plan, fresh map[string]bool) error {
	uid, gid := os.Getuid(), os.Getgid()
	b := a.builder()
	// 共享缓存按 tag 判断要不要重播，不跟着"volume 是不是新建的"走——
	// 见 prep.markerDir 的注释。
	seedCache := fresh == nil || fresh["sbx-cache"] || b.CacheSeededWith(tag, a.WS.ID) != tag
	cached := false
	for _, s := range plan.Seeds {
		vol := "sbx-cache"
		if !s.Cache {
			i := maskIndex(a.Cfg.Deps.Mask, s.Dest)
			if i < 0 {
				continue
			}
			vol = t.DepVolume(i)
		}
		if s.Cache {
			if !seedCache {
				continue
			}
			cached = true
		} else if fresh != nil && !fresh[vol] {
			continue
		}
		fmt.Fprintf(a.Out, "seeding %s from %s (%s) ...\n", s.Dest, tag, prep.HumanSize(s.Size))
		if err := b.Seed(tag, s.Path, vol, s.Sub, uid, gid); err != nil {
			return fmt.Errorf("seeding %s: %w", s.Dest, err)
		}
	}
	if cached {
		return b.MarkCacheSeeded(tag, a.WS.ID, tag, uid, gid)
	}
	return nil
}

func maskIndex(masks []string, dest string) int {
	for i, m := range masks {
		if m == dest {
			return i
		}
	}
	return -1
}

// prepDeclared 读出当前 prep 镜像里的声明。没配 prep、或者镜像还没构建时返回 nil——
// 这两种情况都不是错误，sbx config show 照常往下走。
func (a *App) prepDeclared() (*prep.Manifest, error) {
	s, err := prep.FromConfig(a.Cfg, a.WS.ID, a.WS.Root, a.homeDir())
	if err != nil || !s.Enabled() {
		return nil, nil
	}
	tag, err := s.Tag()
	if err != nil {
		return nil, err
	}
	if ok, err := a.Docker.ImageExists(tag); err != nil || !ok {
		return nil, err
	}
	return a.builder().Declared(tag)
}

// prepLoad 是 sbx run 里 prep 的前半段：定 tag、按 on_drift 处理漂移、读出镜像里的
// 内容清单和声明，并把声明并入配置。播种要等 volume 建好，在 prepSeed 里做。
//
// 拆成两段是因为 /sbx/prep.json 里的 allow 必须在**配置 squid 之前**生效，
// 而播种必须在建 volume 之后——两件事夹着中间好几步，合不到一处。
//
// 没配 prep 的仓库这里什么都不做——prep 是特殊场景的补丁，不是必经之路。
func (a *App) prepLoad(t task.Task) (string, *prep.Plan, error) {
	s, err := prep.FromConfig(a.Cfg, a.WS.ID, a.WS.Root, a.homeDir())
	if err != nil {
		return "", nil, err
	}
	if !s.Enabled() {
		return "", nil, nil
	}
	tag, err := s.Tag()
	if err != nil {
		return "", nil, err
	}
	exists, err := a.Docker.ImageExists(tag)
	if err != nil {
		return "", nil, err
	}
	if !exists {
		// tag 是内容哈希，所以"这个 tag 的镜像不存在"就是"依赖漂移了或者从没构建过"。
		// 默认只提醒不构建：自动构建意味着每次 sbx run 都可能悄悄连一次内网，
		// 边界就糊了（boundary.md §2.6）。
		switch a.Cfg.Prep.OnDrift {
		case "fail":
			return "", nil, fmt.Errorf("the prep image %s does not exist (%s or a watched file changed); run sbx prep build", tag, s.File)
		case "build":
			if tag, err = a.builder().Build(s); err != nil {
				return "", nil, err
			}
		default:
			a.logf("warning: the prep image %s does not exist (%s or a watched file changed); run sbx prep build to refresh the seed. This task starts with empty dependency volumes.", tag, s.File)
			return "", nil, nil
		}
	}
	plan, err := a.builder().Inspect(s, tag)
	if err != nil {
		return "", nil, err
	}
	for _, w := range plan.Warnings {
		a.logf("warning: prep: %s", w)
	}
	if err := a.applyPrepManifest(plan.Manifest); err != nil {
		return "", nil, err
	}
	return tag, &plan, nil
}

// applyPrepManifest 合并镜像里的声明，并把生效的内容打出来——镜像能往配置里塞东西
// 是 R-B1，所以它必须是可见的，不能只在 --check 里看得到。
func (a *App) applyPrepManifest(m *prep.Manifest) error {
	if m == nil {
		return nil
	}
	if err := a.Loaded.MergePrep(m.Allow, m.Env); err != nil {
		return fmt.Errorf("%s: %w", prep.ManifestPath, err)
	}
	a.Cfg = a.Loaded.Config
	var parts []string
	if len(m.Allow) > 0 {
		parts = append(parts, "allow "+strings.Join(m.Allow, ", "))
	}
	if len(m.Env) > 0 {
		keys := make([]string, 0, len(m.Env))
		for k := range m.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts = append(parts, "env "+strings.Join(keys, ", "))
	}
	if len(m.CA) > 0 {
		parts = append(parts, fmt.Sprintf("%d CA certificate(s)", len(m.CA)))
	}
	if len(parts) > 0 {
		fmt.Fprintf(a.Out, "prep declares: %s (from %s in the image; sbx config show marks them %s)\n",
			strings.Join(parts, " · "), prep.ManifestPath, config.LayerPrep)
	}
	return nil
}

// prepSeed 是后半段：把 plan 里的内容播进刚建好的 volume。
func (a *App) prepSeed(t task.Task, tag string, plan *prep.Plan, fresh map[string]bool) error {
	if plan == nil {
		return nil
	}
	return a.seedTask(t, tag, *plan, fresh)
}

// installCAs 把 prep 镜像里声明的 CA 证书装进 Task 容器的信任库（M4-15）。
//
// 走的是 tar 流直传，不落宿主机文件系统。update-ca-certificates 是**追加**语义，
// 公网的根证书一个不少——这正是 R-B2 要求的：别用 SSL_CERT_FILE 那种替换语义去做，
// 那会让容器里所有公网 HTTPS 一起挂掉。
func (a *App) installCAs(t task.Task, tag string, cas []string) error {
	if len(cas) == 0 {
		return nil
	}
	tmp := t.Container() + "-ca"
	a.Docker.Rm(tmp)
	if _, err := a.Docker.Run("create", "--name", tmp, "--entrypoint", "true", tag); err != nil {
		return err
	}
	defer a.Docker.Rm(tmp)
	for _, p := range cas {
		if !path.IsAbs(p) || strings.Contains(p, "..") {
			return fmt.Errorf("%s: ca entry %q must be an absolute path inside the image", prep.ManifestPath, p)
		}
		tarball, err := a.Docker.RunBytes(nil, "cp", tmp+":"+p, "-")
		if err != nil {
			return fmt.Errorf("ca %s: %w", p, err)
		}
		if _, err := a.Docker.RunBytes(bytes.NewReader(tarball), "cp", "-", t.Container()+":/usr/local/share/ca-certificates/"); err != nil {
			return fmt.Errorf("ca %s: %w", p, err)
		}
	}
	if out, err := a.Docker.Exec(t.Container(), docker.ExecOpts{User: "0"}, "update-ca-certificates"); err != nil {
		return fmt.Errorf("update-ca-certificates: %w\n%s", err, out)
	}
	fmt.Fprintf(a.Out, "installed %d CA certificate(s) from the prep image into the task's trust store\n", len(cas))
	return nil
}
