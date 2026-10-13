// Package prep 把「取」变成「存」（M4-6…M4-9、boundary.md §2）。
//
// 一次宿主机上的 docker build 产出一个镜像，sbx 再从这个镜像里把内容播种进 Task 的
// volume。构建跑在宿主机的 Docker 上，走默认 bridge，有完整的内网可达性和凭据；
// Task 容器则一行私有网络配置都不需要，凭据只在 build 期存在，agent 永远碰不到。
//
//	.sbx/prep.Dockerfile ──build（有网有凭据）──> sbx/prep-<ws>:<hash> ──cp──> Task volume
package prep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"sandx/internal/config"
	"sandx/internal/docker"
)

// CacheRoot 是镜像里给共享 sbx-cache 准备内容的位置。
// 和 agent 层的 GOMODCACHE / npm_config_cache 指向同一处，所以 prep 的 Dockerfile
// 什么都不用改，照常 go mod download / npm ci 就行。
const CacheRoot = "/sbx/cache"

// ManifestPath 是镜像里那份可选的声明（M4-15）。
const ManifestPath = "/sbx/prep.json"

// Spec 是一次 prep 的全部输入。
type Spec struct {
	WSID  string // Workspace ID，进 tag
	Root  string // 仓库根，既是 build context 也是 watch 的基准
	File  string // 相对 Root 的 Dockerfile 路径
	Watch []string
	// Secrets 是 id -> 宿主机路径，已经展开过 ~。
	Secrets map[string]string
	Masks   []string // deps.mask，决定哪些目录要播种
}

// FromConfig 按配置组装 Spec。secrets 里的路径在这里展开 ~ 并校验存在，
// 免得构建跑到一半才因为少一个文件失败。
func FromConfig(cfg config.Config, wsID, root, home string) (Spec, error) {
	s := Spec{WSID: wsID, Root: root, File: cfg.Prep.File, Watch: cfg.Prep.Watch, Masks: cfg.Deps.Mask}
	if len(cfg.Prep.Secrets) > 0 {
		s.Secrets = map[string]string{}
	}
	for _, id := range cfg.Prep.Secrets {
		p := expandHome(cfg.Prep.Secret[id], home)
		if _, err := os.Stat(p); err != nil {
			return Spec{}, fmt.Errorf("prep secret %q points at %s, which cannot be read: %w", id, p, err)
		}
		s.Secrets[id] = p
	}
	return s, nil
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// Enabled 报告这个仓库有没有配 prep。判据是 Dockerfile 在不在——
// 配置里 prep.file 永远有默认值，拿它判断会让所有仓库都"配了 prep"。
func (s Spec) Enabled() bool {
	st, err := os.Stat(s.Dockerfile())
	return err == nil && st.Mode().IsRegular()
}

// Dockerfile 返回 Dockerfile 的绝对路径。
func (s Spec) Dockerfile() string { return filepath.Join(s.Root, filepath.FromSlash(s.File)) }

// Tag 计算镜像 tag：hash(Dockerfile 内容 + 每个 watch 文件的内容)。
//
// 为什么要 watch：解析 Dockerfile 猜不出它 COPY 了什么，改了 go.sum 而 Dockerfile
// 没变的话 hash 不动，种子就陈旧了。让项目层明说是哪些文件（相对路径，是事实陈述，
// 红线放行）。watch 里列了但不存在的文件按空内容计入——这样"文件被删掉"也是一次漂移。
func (s Spec) Tag() (string, error) {
	df, err := os.ReadFile(s.Dockerfile())
	if err != nil {
		return "", err
	}
	h := sha256.New()
	write := func(b []byte) { fmt.Fprintf(h, "%d:", len(b)); h.Write(b) }
	write(df)
	// 排序后再算，免得 watch 的书写顺序影响 tag
	watch := append([]string(nil), s.Watch...)
	sort.Strings(watch)
	for _, rel := range watch {
		write([]byte(rel))
		data, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(rel)))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		write(data)
	}
	return "sbx/prep-" + s.WSID + ":" + hex.EncodeToString(h.Sum(nil))[:12], nil
}

// Builder 跑构建和播种。
type Builder struct {
	Docker *docker.Client
	Out    io.Writer
}

// Build 构建 prep 镜像并返回 tag。
func (b Builder) Build(s Spec) (string, error) {
	tag, err := s.Tag()
	if err != nil {
		return "", err
	}
	fmt.Fprintf(b.Out, "building the prep image %s ...\n", tag)
	spec := docker.BuildSpec{
		ContextDir: s.Root,
		Dockerfile: s.Dockerfile(),
		Tag:        tag,
		Labels:     map[string]string{"sbx.kind": "prep", "sbx.ws": s.WSID},
		Secrets:    s.Secrets,
		// 构建期要够得着内网，不能走宿主机那个只通外网的代理（boundary.md §2.8）
		NoProxy: true,
	}
	if err := b.Docker.Build(spec, b.Out, b.Out); err != nil {
		return "", err
	}
	return tag, nil
}

// Seedable 是镜像里一处可播种的内容。
type Seedable struct {
	Path  string // 镜像里的绝对路径
	Dest  string // 目标：mask 的相对路径，或 "cache/<name>"
	Sub   string // volume 内的子目录；mask 为空，cache 是 <name>
	Size  int64  // 字节；量不出来时为 0
	Cache bool   // true = 播种到共享的 sbx-cache
}

// Plan 是一次 --check 的全部结论。
type Plan struct {
	Tag      string
	Workdir  string
	Seeds    []Seedable
	Manifest *Manifest
	Warnings []string
}

// Manifest 是镜像里 /sbx/prep.json 的内容（M4-15）。
// 字段是白名单而不是任意配置：它和 .sbx/sandbox.toml 一样跟着仓库走、一样受信任确认
// 覆盖，不该有更高权限。
type Manifest struct {
	Allow []string          `json:"allow"`
	Env   map[string]string `json:"env"`
	CA    []string          `json:"ca"`
}

// Inspect 看一眼构建出来的镜像：哪些 mask 目录有内容、cache 下有什么、有没有 manifest。
// 不碰任何 volume——这是 --check 的全部实现，也是 run 之前判断播种内容的依据。
func (b Builder) Inspect(s Spec, tag string) (Plan, error) {
	p := Plan{Tag: tag}
	wd, err := b.Docker.Run("image", "inspect", "-f", "{{.Config.WorkingDir}}", tag)
	if err != nil {
		return p, err
	}
	p.Workdir = strings.TrimSpace(wd)
	if p.Workdir == "" {
		return p, fmt.Errorf("%s has no WORKDIR; prep needs one so sbx knows where your repo lives in the image", tag)
	}

	// 一次性把要问的都问掉：起一个容器探测比起 N 个便宜得多。
	// du -s 给的是块数，带 -k 换成 KB；量不出来的留空，由 parseProbe 当 0 处理。
	var targets []string
	for _, m := range s.Masks {
		targets = append(targets, path.Join(p.Workdir, filepath.ToSlash(m)))
	}
	script := probeScript(targets)
	out, err := b.Docker.Run("run", "--rm", "--network", "none", "-u", "0", "--entrypoint", "sh", tag, "-c", script)
	if err != nil {
		return p, fmt.Errorf("cannot inspect %s (does the image have /bin/sh?): %w", tag, err)
	}
	sizes, cache, manifest := parseProbe(out)

	for i, m := range s.Masks {
		t := targets[i]
		sz, ok := sizes[t]
		if !ok {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s is in deps.mask but %s does not exist in the image", m, t))
			continue
		}
		p.Seeds = append(p.Seeds, Seedable{Path: t, Dest: m, Size: sz})
	}
	for _, name := range cache {
		full := path.Join(CacheRoot, name)
		p.Seeds = append(p.Seeds, Seedable{Path: full, Dest: "cache/" + name, Sub: name, Size: sizes[full], Cache: true})
	}
	if manifest != "" {
		var m Manifest
		if err := json.Unmarshal([]byte(manifest), &m); err != nil {
			p.Warnings = append(p.Warnings, ManifestPath+" is not valid JSON; ignoring it: "+err.Error())
		} else {
			p.Manifest = &m
		}
	}
	if len(p.Seeds) == 0 {
		p.Warnings = append(p.Warnings, "nothing to seed: no deps.mask directory and no "+CacheRoot+"/* exists in the image")
	}
	return p, nil
}

// probe 输出的三种行前缀。用前缀而不是分段输出：一条命令一次往返，
// 而且某一段为空时不会把后面的内容错位读进来。
const (
	preSize  = "S\t"
	preCache = "C\t"
	preMan   = "M\t"
)

func probeScript(targets []string) string {
	var b strings.Builder
	for _, t := range targets {
		fmt.Fprintf(&b, "[ -d %q ] && du -sk %q | sed 's|^|%s|'\n", t, t, preSize)
	}
	// cache 下的一级目录逐个量，顺带列名
	fmt.Fprintf(&b, "for d in %s/*; do [ -d \"$d\" ] || continue; echo \"%s$(basename $d)\"; du -sk \"$d\" | sed 's|^|%s|'; done\n",
		CacheRoot, preCache, preSize)
	fmt.Fprintf(&b, "[ -f %q ] && sed 's|^|%s|' %q\n", ManifestPath, preMan, ManifestPath)
	// 探测失败不该让整条命令挂掉：上面每一步都带条件，最后兜一个真值
	b.WriteString("true\n")
	return b.String()
}

// parseProbe 解析 probeScript 的输出。纯函数，便于测。
func parseProbe(out string) (sizes map[string]int64, cache []string, manifest string) {
	sizes = map[string]int64{}
	var man strings.Builder
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, preSize):
			// "<KB>\t<path>"
			kb, p, ok := strings.Cut(strings.TrimPrefix(line, preSize), "\t")
			if !ok {
				continue
			}
			n, err := strconv.ParseInt(strings.TrimSpace(kb), 10, 64)
			if err != nil {
				continue
			}
			sizes[strings.TrimSpace(p)] = n * 1024
		case strings.HasPrefix(line, preCache):
			if name := strings.TrimSpace(strings.TrimPrefix(line, preCache)); name != "" {
				cache = append(cache, name)
			}
		case strings.HasPrefix(line, preMan):
			man.WriteString(strings.TrimPrefix(line, preMan))
			man.WriteByte('\n')
		}
	}
	return sizes, cache, strings.TrimSpace(man.String())
}

// Declared 只把镜像里的 /sbx/prep.json 读出来，不做别的探测。
// sbx config show 走这条路：它只想知道"这个镜像往配置里塞了什么"，
// 不需要 Inspect 那一整套 du。镜像没有这个文件时返回 nil。
func (b Builder) Declared(tag string) (*Manifest, error) {
	out, err := b.Docker.Run("run", "--rm", "--network", "none", "-u", "0", "--entrypoint", "sh", tag,
		"-c", fmt.Sprintf("cat %q 2>/dev/null || true", ManifestPath))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var m Manifest
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", ManifestPath, err)
	}
	return &m, nil
}

// markerDir 是 sbx-cache 里记录"这个 Workspace 的缓存已经按哪个 tag 播过种"的位置。
//
// 为什么共享缓存不能照搬"只在 volume 新建时播种"：sbx-cache 是全局的，
// 装过一次 sbx 的人它早就存在了，照搬的话 go-mod / npm 这类缓存种子对老用户
// 永远不生效。缓存又和依赖目录不同——cp -a 进子目录是**合并**不是覆盖，
// 重播不会抹掉谁装的东西，所以按 tag 变化重播是安全的。
// 标记放在 volume 里而不是宿主机 state 目录：volume 被删掉时标记跟着没，不会错判。
const markerDir = CacheRoot + "/.sbx-prep"

// CacheSeededWith 返回这个 Workspace 上次播种共享缓存用的 tag；没播过时为空。
func (b Builder) CacheSeededWith(runner, wsID string) string {
	out, err := b.Docker.Run("run", "--rm", "-u", "0", "--network", "none",
		"--mount", "type=volume,source=sbx-cache,target=/v", "--entrypoint", "sh", runner,
		"-c", fmt.Sprintf("cat %q 2>/dev/null || true", path.Join("/v/.sbx-prep", wsID)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// MarkCacheSeeded 记下这次播种用的 tag。
func (b Builder) MarkCacheSeeded(runner, wsID, tag string, uid, gid int) error {
	script := fmt.Sprintf("set -e; mkdir -p /v/.sbx-prep; printf %%s %q > %q; chown -R %d:%d /v/.sbx-prep",
		tag, path.Join("/v/.sbx-prep", wsID), uid, gid)
	_, err := b.Docker.Run("run", "--rm", "-u", "0", "--network", "none",
		"--mount", "type=volume,source=sbx-cache,target=/v", "--entrypoint", "sh", runner, "-c", script)
	return err
}

// Seed 把镜像里 src 的内容拷进 volume 的 sub 子目录（sub 为空表示 volume 根），
// 并把属主改成宿主机的 uid/gid。
//
// 拷贝跑在 prep 镜像自己的容器里（--network none），不经过宿主机文件系统：
// 几百 MB 的 node_modules 走 docker cp 来回一趟太贵，而且会丢属主和权限位。
// 属主必须改：prep 镜像里的文件是 root 的，而 Task 容器以宿主机的 uid 运行。
func (b Builder) Seed(tag, src, vol, sub string, uid, gid int) error {
	dst := "/v"
	if sub != "" {
		dst = path.Join("/v", sub)
	}
	script := fmt.Sprintf("set -e; mkdir -p %q; cp -a %q/. %q/ && chown -R %d:%d %q",
		dst, src, dst, uid, gid, dst)
	_, err := b.Docker.Run("run", "--rm", "-u", "0", "--network", "none",
		"--mount", "type=volume,source="+vol+",target=/v", "--entrypoint", "sh", tag, "-c", script)
	return err
}

// Describe 是 --check 和 sbx prep show 共用的报告正文。
func (p Plan) Describe() string {
	var b strings.Builder
	var seeded, cached []string
	for _, s := range p.Seeds {
		line := fmt.Sprintf("%s %s", s.Dest, HumanSize(s.Size))
		if s.Cache {
			cached = append(cached, line)
		} else {
			seeded = append(seeded, line)
		}
	}
	all := append(append([]string(nil), seeded...), cached...)
	fmt.Fprintf(&b, "seeded:   %s\n", orNone(strings.Join(all, " · ")))
	if m := p.Manifest; m != nil {
		var parts []string
		if n := len(m.Allow); n > 0 {
			parts = append(parts, fmt.Sprintf("allow +%d (%s)", n, strings.Join(m.Allow, ", ")))
		}
		if n := len(m.Env); n > 0 {
			keys := make([]string, 0, n)
			for k := range m.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts = append(parts, fmt.Sprintf("env +%d (%s)", n, strings.Join(keys, ", ")))
		}
		if n := len(m.CA); n > 0 {
			parts = append(parts, fmt.Sprintf("ca +%d", n))
		}
		fmt.Fprintf(&b, "declared: %s\n", orNone(strings.Join(parts, " · ")))
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "warning:  %s\n", w)
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "(nothing)"
	}
	return s
}

// HumanSize 按十进制单位打印，和 docker 的输出对得上。
func HumanSize(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fGB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0fMB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0fkB", float64(n)/1e3)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
