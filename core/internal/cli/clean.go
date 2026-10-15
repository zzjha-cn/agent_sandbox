package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/task"
)

// M4-5 只做了报告：sbx 的 volume 和镜像没有配额，而 sbx done 只回收 Task 自己的
// 依赖 volume，sbx-cache / 旧镜像是只涨不消的。这里补上回收那一半。
//
// 一条命令两档，分档的判据不是"哪一层"，是**回来要不要网**——用户不必认识
// go-mod 和 go-build 的区别，只需要知道自己现在等不等得起重下：
//
//	sbx clean          旧镜像 + 派生缓存。重编译就能回来，不问、不确认
//	sbx clean --deep   连包缓存和 BuildKit 的构建缓存一起。要重下，所以要确认，
//	                   还要过种子那道闸
//
// 没有配置键，没有后台清理。回收是人按的，工具只负责在你看得见的地方把数字摆出来。
var (
	// derivedCacheDirs 是纯派生的：删了下次编译自己重建，不需要网络。
	// Go 的构建缓存本来就会自动 trim（超过 5 天没用到的条目），但它不会因为你
	// 磁盘满了就提前动手——clean 的价值是按你的时机立刻回收。
	derivedCacheDirs = []string{"go-build"}
	// packageCacheDirs 删了要重下。prep 播种的私有依赖也混在 go-mod 里，
	// 所以清它们之前必须过 seedGate。
	packageCacheDirs = []string{"go-mod", "npm", "pip", "uv", "cargo"}
)

func (a *App) cleanCmd() *cobra.Command {
	var deep, yes bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Reclaim disk: old images and derived caches (--deep also clears package and build caches)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 用 setHome 而不是 load：clean 是全局的，不属于任何 Workspace，
			// 而且配置坏掉的时候它照样该能用。
			if err := a.setHome(); err != nil {
				return err
			}
			return a.clean(deep, yes)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "also clear package caches and the BuildKit build cache (they have to be re-fetched)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation (for scripts)")
	return cmd
}

func (a *App) clean(deep, yes bool) error {
	refs, err := a.sbxImages()
	if err != nil {
		return err
	}
	inUse, err := a.imagesInUse()
	if err != nil {
		return err
	}
	remove := planImages(refs, inUse)
	sizes, buildCache := a.dockerDF()

	// 两份列表：measure 是要量的，dirs 是要清的。默认档也得把包缓存量出来——
	// 最后那句"还占着多少、--deep 能清掉"才有数可报，否则它永远显示 0。
	measure := append(append([]string{}, derivedCacheDirs...), packageCacheDirs...)
	dirs := derivedCacheDirs
	if deep {
		dirs = measure
	}
	runner := ""
	if len(refs) > 0 {
		runner = refs[0] // 任何一个 sbx 镜像都带 sh，拿最新的那个当工具人
	}

	var busy []string
	if items, err := a.Docker.Running("label=sbx.role=agent"); err == nil {
		for _, it := range items {
			if n := labels(it)["sbx.task"]; n != "" {
				busy = append(busy, n)
			}
		}
	}

	var used map[string]int64
	var seeds map[string]string
	if runner == "" {
		a.logf("no sbx image on this machine yet; skipping the caches")
		dirs = nil
	} else if len(busy) > 0 {
		// 在跑的 Task 正拿着这个 volume 编译，清到一半的缓存会让它以看不懂的方式失败。
		sort.Strings(busy)
		fmt.Fprintf(a.Out, "caches left alone: %s still running\n", strings.Join(busy, ", "))
		dirs = nil
	} else {
		if used, err = a.cacheSizes(runner, measure); err != nil {
			return err
		}
		if seeds, err = a.cacheSeeds(runner); err != nil {
			return err
		}
	}

	if deep && len(dirs) > 0 {
		if err := a.seedGate(seeds); err != nil {
			return err
		}
	}

	if len(remove) == 0 && len(dirs) == 0 && !(deep && buildCache > 0) {
		fmt.Fprintln(a.Out, "nothing to reclaim")
		return nil
	}

	if deep && !yes {
		if err := a.confirmClean(dirs, used, buildCache, seeds); err != nil {
			return err
		}
	}

	var freed int64
	for _, ref := range remove {
		if _, err := a.Docker.Run("rmi", ref); err != nil {
			// docker 自己会拦住还被容器引用的镜像——拦住就对了，这里只是别中断
			fmt.Fprintf(a.Err, "warning: kept %s: %v\n", ref, err)
			continue
		}
		fmt.Fprintf(a.Out, "removed %s  %s\n", ref, humanBytes(sizes[ref]))
		freed += sizes[ref]
	}
	if len(dirs) > 0 {
		if err := a.clearCacheDirs(runner, dirs, deep); err != nil {
			return err
		}
		for _, d := range dirs {
			if used[d] == 0 {
				continue // 空目录照样重建了，但报一行 0 只是噪音
			}
			fmt.Fprintf(a.Out, "cleared cache/%s  %s\n", d, humanBytes(used[d]))
			freed += used[d]
		}
	}
	if deep {
		// prep build 每跑一次 BuildKit 就攒一层。它和包缓存同一个性质：
		// 清了下次构建要重新拉基础层，所以归 --deep，不归默认档。
		// prune 只动没被引用的那部分，正在用的记录它自己会跳过。
		if _, err := a.Docker.Run("builder", "prune", "-f"); err != nil {
			fmt.Fprintf(a.Err, "warning: build cache kept: %v\n", err)
		} else if buildCache > 0 {
			fmt.Fprintf(a.Out, "cleared build cache  %s\n", humanBytes(buildCache))
			freed += buildCache
		}
	}
	fmt.Fprintf(a.Out, "reclaimed %s\n", humanBytes(freed))

	if !deep && len(dirs) > 0 {
		held := buildCache
		for _, d := range packageCacheDirs {
			held += used[d]
		}
		if held > 0 {
			fmt.Fprintf(a.Out, "\npackage + build caches still held: %s (re-downloadable) — sbx clean --deep\n", humanBytes(held))
		}
	}
	return nil
}

// planImages 挑出可以删的镜像。两条规则，都不需要用户参与：
//
//	一、任何 Task 还钉在上面的 tag 不删（meta.PrepTag / meta.Image）；
//	二、每个 repository 保留最新的一个。
//
// 第二条顺带把两种镜像都盖住了：prep 的 repository 是 sbx/prep-<ws>，
// 所以是"每个 Workspace 留最新的种子"；agent 镜像的 repository 是 sbx/<profile>，
// 所以是"每个 profile 留最新的那个"。不需要读 label，也不需要解析构建时间——
// docker images 的输出本来就按创建时间倒序，每组第一个就是最新的。
func planImages(refs []string, inUse map[string]bool) []string {
	seen := map[string]bool{}
	var remove []string
	for _, ref := range refs {
		repo := ref
		if i := strings.LastIndex(ref, ":"); i > 0 {
			repo = ref[:i]
		}
		newest := !seen[repo]
		seen[repo] = true
		if newest || inUse[ref] {
			continue
		}
		remove = append(remove, ref)
	}
	return remove
}

// seedGate 是 --deep 唯一的一道闸：包缓存里混着 prep 播种的私有依赖，
// 清掉它们只在**对应的 prep 镜像还在**时才是安全的——那时候下一次 sbx run 会自动
// 重播，而且重播不需要网络。镜像不在就等于清掉了沙箱里拿不回来的东西。
//
// 要查的是 sbx-cache 里**所有** Workspace 的标记，不只是当前这个：缓存是全局的，
// 你在 A 仓库里敲的这条命令，清掉的是 B、C 的私有依赖。
func (a *App) seedGate(seeds map[string]string) error {
	var missing []string
	for ws, tag := range seeds {
		exists, err := a.Docker.ImageExists(tag)
		if err != nil {
			return err
		}
		if !exists {
			missing = append(missing, fmt.Sprintf("  %-16s %s  MISSING", ws, tag))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("these workspaces could not get their private dependencies back:\n%s\n"+
		"       run `sbx prep build` in them first, then sbx clean --deep",
		strings.Join(missing, "\n"))
}

func (a *App) confirmClean(dirs []string, used map[string]int64, buildCache int64, seeds map[string]string) error {
	pkg := buildCache
	for _, d := range dirs {
		pkg += used[d]
	}
	fmt.Fprintf(a.Out, "this clears %s of caches shared by all workspaces.\n", humanBytes(pkg))
	ws := make([]string, 0, len(seeds))
	for w := range seeds {
		ws = append(ws, w)
	}
	sort.Strings(ws)
	for _, w := range ws {
		fmt.Fprintf(a.Out, "  %-16s prep seeds present, image ok -> re-seeds on the next sbx run\n", w)
	}
	fmt.Fprint(a.Out, "everything else, including the BuildKit cache, will be rebuilt or re-downloaded on demand.\ncontinue? [y/N]: ")
	line, _ := bufio.NewReader(a.In()).ReadString('\n')
	if s := strings.ToLower(strings.TrimSpace(line)); s != "y" && s != "yes" {
		return fmt.Errorf("cancelled")
	}
	return nil
}

// sbxImages 列出这台机器上所有 sbx 的镜像，新的在前。
func (a *App) sbxImages() ([]string, error) {
	out, err := a.Docker.Run("images", "--filter", "reference=sbx/*", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		if ref := strings.TrimSpace(line); ref != "" && !strings.HasSuffix(ref, ":<none>") {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// imagesInUse 收集所有 Workspace 的 Task 还钉着的镜像。
// 走 state 目录而不是当前 Workspace：clean 是全局的，只看眼前那个仓库会删掉别人的种子。
func (a *App) imagesInUse() (map[string]bool, error) {
	inUse := map[string]bool{}
	root := filepath.Join(a.Home, "state")
	wss, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return inUse, nil
		}
		return nil, err
	}
	for _, ws := range wss {
		if !ws.IsDir() {
			continue
		}
		tasks, err := os.ReadDir(filepath.Join(root, ws.Name()))
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			if !t.IsDir() {
				continue
			}
			var m task.Meta
			data, err := os.ReadFile(filepath.Join(root, ws.Name(), t.Name(), "meta.json"))
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			if json.Unmarshal(data, &m) != nil {
				continue // 坏掉的 meta 不该让回收整个停下来
			}
			if m.PrepTag != "" {
				inUse[m.PrepTag] = true
			}
			if m.Image != "" {
				inUse[m.Image] = true
			}
		}
	}
	return inUse, nil
}

// dockerDF 问一次 docker system df，拿两样东西：每个镜像自己占多少，
// 以及 BuildKit 的构建缓存总共多大。
//
// 镜像用 UniqueSize 而不是 Size：后者把共享的基础层也算进来，几个镜像加起来会得到
// 一个远大于实际的数（disk.go 里同一个理由）。取不到就当 0——这是个报告，不该因为
// 一行格式意外就让整条命令失败。
func (a *App) dockerDF() (sizes map[string]int64, buildCache int64) {
	sizes = map[string]int64{}
	out, err := a.Docker.Run("system", "df", "-v", "--format", "{{json .}}")
	if err != nil {
		return sizes, 0
	}
	var df dfOutput
	if json.Unmarshal([]byte(out), &df) != nil {
		return sizes, 0
	}
	for _, e := range df.Images {
		sizes[e.Repository+":"+e.Tag] = parseSize(e.UniqueSize)
	}
	// 构建缓存分不出是谁的（BuildKit 不带 label），整体计入——
	// 这台机器上的构建缓存基本都是 sbx 的 prep build 攒的。
	for _, e := range df.BuildCache {
		buildCache += parseSize(e.Size)
	}
	return sizes, buildCache
}

// cacheSizes 量一遍 sbx-cache 里这几个目录各占多少。
func (a *App) cacheSizes(runner string, dirs []string) (map[string]int64, error) {
	if len(dirs) == 0 {
		return map[string]int64{}, nil
	}
	var sb strings.Builder
	for _, d := range dirs {
		fmt.Fprintf(&sb, "du -sk %q 2>/dev/null; ", path.Join("/v", d))
	}
	out, err := a.volumeRun(runner, sb.String())
	if err != nil {
		return nil, err
	}
	return parseDU(out), nil
}

// parseDU 读 du -sk 的输出（"1234\t/v/go-mod"），按目录名给出字节数。
// 量不出来的行跳过而不是报错：这是个报告，少一行比整条命令失败好。
func parseDU(out string) map[string]int64 {
	sizes := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		sizes[path.Base(f[1])] = kb * 1024
	}
	return sizes
}

// cacheSeeds 读出 sbx-cache 里每个 Workspace 的播种标记：ws -> tag。
func (a *App) cacheSeeds(runner string) (map[string]string, error) {
	out, err := a.volumeRun(runner,
		`for f in /v/.sbx-prep/*; do [ -f "$f" ] || continue; printf '%s %s\n' "${f##*/}" "$(cat "$f")"; done`)
	if err != nil {
		return nil, err
	}
	return parseSeeds(out), nil
}

func parseSeeds(out string) map[string]string {
	seeds := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			seeds[f[0]] = f[1]
		}
	}
	return seeds
}

// clearCacheDirs 清空这几个目录，重建并把属主改回宿主机的 uid/gid——
// 不改的话重建出来的目录是 root 的，而 Task 容器以宿主机 uid 运行，下次写缓存会 EACCES。
//
// 顺序是有讲究的：**播种标记必须先于内容删掉**。反过来会留下"标记在、内容没了"的
// 状态，sbx 认为已经播过就不再重播，私有依赖再也回不来，而且一声不吭——编译时才炸，
// 还看着像别的毛病。反着来是安全的：标记没了内容还在，顶多多播一次（cp -a 是合并）。
func (a *App) clearCacheDirs(runner string, dirs []string, deep bool) error {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	_, err := a.volumeRun(runner, clearScript(dirs, deep, uid, gid))
	return err
}

func clearScript(dirs []string, deep bool, uid, gid string) string {
	var sb strings.Builder
	sb.WriteString("set -e; ")
	if deep {
		sb.WriteString("rm -f /v/.sbx-prep/*; ")
	}
	for _, d := range dirs {
		p := path.Join("/v", d)
		fmt.Fprintf(&sb, "rm -rf %q; mkdir -p %q; chown %s:%s %q; ", p, p, uid, gid, p)
	}
	return sb.String()
}

// volumeRun 在 sbx-cache 上跑一段 sh。--network none：这里没有一件事需要网络。
func (a *App) volumeRun(runner, script string) (string, error) {
	return a.Docker.Run("run", "--rm", "-u", "0", "--network", "none",
		"--mount", "type=volume,source=sbx-cache,target=/v",
		"--entrypoint", "sh", runner, "-c", script)
}
