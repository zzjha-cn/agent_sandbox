package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// 四层配置的名字（design §9.1、ADR 0009）。合并顺序就是这个顺序：
// 标量后者覆盖前者，列表取并集。
const (
	LayerDefault   = "default"
	LayerGlobal    = "global"
	LayerProject   = "project"
	LayerWorkspace = "workspace"
	// LayerPrep 是 prep 镜像里的 /sbx/prep.json（M4-15）。它不是文件层，不参与
	// LoadLayers 的循环，而是在 sbx run 里合并进来的——但权限和项目层完全相同。
	LayerPrep = "prep"
)

// Layer 是一层配置文件。Project 为 true 时要额外过 M2-2 的校验。
type Layer struct {
	Name    string
	Path    string
	Project bool
	Found   bool // 加载后填：文件是否存在
}

// Source 记录一个字段的值是谁给的。标量看最后一个，列表是所有贡献者。
type Source struct {
	Layers []string
	List   bool
}

// Last 返回决定最终值的那一层。
func (s Source) Last() string {
	if len(s.Layers) == 0 {
		return LayerDefault
	}
	return s.Layers[len(s.Layers)-1]
}

// String 是 sbx config show 里「来源」那一列。
func (s Source) String() string {
	if len(s.Layers) == 0 {
		return LayerDefault
	}
	if s.List {
		return strings.Join(s.Layers, " + ")
	}
	return s.Last()
}

// Loaded 是合并后的结果。
type Loaded struct {
	Config   Config
	Layers   []Layer
	Sources  map[string]Source
	Warnings []string
}

// Paths 按 design §9.1 给出四层里的三个文件。wsRoot 为空时只有全局层
// （sbx login / upgrade / config show 不要求在 git 仓库里）。
func Paths(home, wsRoot, wsID string) []Layer {
	ls := []Layer{{Name: LayerGlobal, Path: filepath.Join(home, "config.toml")}}
	if wsRoot == "" {
		return ls
	}
	return append(ls,
		Layer{Name: LayerProject, Path: filepath.Join(wsRoot, ".sbx", "sandbox.toml"), Project: true},
		Layer{Name: LayerWorkspace, Path: filepath.Join(home, "workspaces", wsID+".toml")},
	)
}

// LoadLayers 从内置默认值开始，按顺序合并各层。文件不存在就跳过。
// 校验放在全部合并完之后：单独一层往往是不完整的，分开校验会误报。
func LoadLayers(layers []Layer) (Loaded, error) {
	ld := Loaded{Config: Default(), Sources: map[string]Source{}, Layers: layers}
	// 默认遮盖项跟 profile 走，而 profile 要合并完才知道，所以先清空、最后再补（M3-5）
	ld.Config.Deps.Mask = nil
	for i := range ld.Layers {
		l := &ld.Layers[i]
		data, err := os.ReadFile(l.Path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return ld, err
		}
		l.Found = true
		var src Config
		md, err := toml.Decode(string(data), &src)
		if err != nil {
			return ld, fmt.Errorf("%s: %w", l.Path, err)
		}
		if l.Project {
			if err := checkProject(string(data), l.Path); err != nil {
				return ld, err
			}
		}
		for _, k := range md.Undecoded() {
			ld.Warnings = append(ld.Warnings, fmt.Sprintf("%s: unknown field %s (ignored)", l.Path, k.String()))
		}
		mergeLayer(&ld.Config, src, md, l.Name, ld.Sources)
	}
	// 补上这个 profile 的默认遮盖项（M3-5）。放在合并之后：profile 可能被后面的层
	// 改掉，而列表是取并集的——先放 web-go 的默认值再并上 py-rust 的，会得到一份
	// 四不像（多出来的遮盖项会在 worktree 里凭空建出目录来）。
	ld.Config.Deps.Mask = union(ProfileMasks(ld.Config.Profile), ld.Config.Deps.Mask)
	if err := ld.Config.Validate(); err != nil {
		return ld, ld.annotate(err)
	}
	return ld, nil
}

// annotate 给校验错误补上"这个值是哪一层给的"。
func (ld Loaded) annotate(err error) error {
	var fe *FieldError
	if !errors.As(err, &fe) {
		return err
	}
	src, ok := ld.Sources[fe.Key]
	if !ok {
		return err
	}
	for _, l := range ld.Layers {
		if l.Name == src.Last() {
			return fmt.Errorf("%s (the value came from the %s layer: %s)", err, l.Name, l.Path)
		}
	}
	return err
}

// MergePrep 把 prep 镜像里 /sbx/prep.json 的声明并入已经合并好的配置（M4-15、
// boundary.md §2.5）。
//
// 权限等同项目层：先过 checkProject 的红线，字段是白名单而不是任意配置。理由是它和
// .sbx/sandbox.toml 一样跟着仓库走、一样受信任确认覆盖，不该因为"藏在镜像里"反而更大。
//
// 它排在最后合并，但**不覆盖任何文件层已经写过的键**：顺序上在后不等于优先级更高，
// 你在 ~/.sbx 里亲手写下的值不该被一个镜像里的 JSON 顶掉。
func (ld *Loaded) MergePrep(allow []string, env map[string]string) error {
	if err := CheckPrepManifest(allow, env); err != nil {
		return err
	}
	if len(allow) > 0 {
		ld.Config.Network.Allow = union(ld.Config.Network.Allow, allow)
		mark(ld.Sources, "network.allow", LayerPrep, true)
	}
	for k, v := range env {
		if _, taken := ld.Sources["env."+k]; taken {
			continue
		}
		if ld.Config.Env == nil {
			ld.Config.Env = map[string]string{}
		}
		ld.Config.Env[k] = v
		mark(ld.Sources, "env."+k, LayerPrep, false)
	}
	return ld.Config.Validate()
}

// binding 把一个配置字段和它的点号 key 绑在一起，合并和打印都走这张表。
type binding struct {
	Key  string
	Str  *string
	F64  *float64
	Int  *int
	Bool *bool
	List *[]string
}

// bindings 列出所有可配置字段，顺序就是 sbx config show 的输出顺序。
// agents.<name>.* 是 map，由 agentFields 单独处理。
func bindings(c *Config) []binding {
	return []binding{
		{Key: "default_agent", Str: &c.DefaultAgent},
		{Key: "profile", Str: &c.Profile},
		{Key: "image", Str: &c.Image},
		{Key: "max_running", Int: &c.MaxRunning},
		{Key: "on_idle", Str: &c.OnIdle},
		{Key: "on_exit", Str: &c.OnExit},
		{Key: "notify_throttle", Int: &c.NotifyThrottle},
		{Key: "network.upstream", Str: &c.Network.Upstream},
		{Key: "network.proxy", Str: &c.Network.Proxy},
		{Key: "network.mode", Str: &c.Network.Mode},
		{Key: "network.cloud_mcp", Bool: &c.Network.CloudMCP},
		{Key: "network.allow", List: &c.Network.Allow},
		{Key: "resources.cpus", F64: &c.Resources.CPUs},
		{Key: "resources.memory", Str: &c.Resources.Memory},
		{Key: "resources.pids", Int: &c.Resources.Pids},
		{Key: "deps.mask", List: &c.Deps.Mask},
		{Key: "git.protect", Bool: &c.Git.Protect},
		{Key: "prep.file", Str: &c.Prep.File},
		{Key: "prep.watch", List: &c.Prep.Watch},
		{Key: "prep.secrets", List: &c.Prep.Secrets},
		{Key: "prep.on_drift", Str: &c.Prep.OnDrift},
	}
}

func mergeLayer(dst *Config, src Config, md toml.MetaData, layer string, srcs map[string]Source) {
	db, sb := bindings(dst), bindings(&src)
	for i := range db {
		d, s := db[i], sb[i]
		if !md.IsDefined(strings.Split(d.Key, ".")...) {
			continue
		}
		switch {
		case d.Str != nil:
			*d.Str = *s.Str
		case d.F64 != nil:
			*d.F64 = *s.F64
		case d.Int != nil:
			*d.Int = *s.Int
		case d.Bool != nil:
			*d.Bool = *s.Bool
		case d.List != nil:
			*d.List = union(*d.List, *s.List)
		}
		mark(srcs, d.Key, layer, d.List != nil)
	}
	// env 和 prep.secret 一样逐键合并：后面的层改一个变量，不该把前面的整份顶掉。
	for k, v := range src.Env {
		if !md.IsDefined("env", k) {
			continue
		}
		if dst.Env == nil {
			dst.Env = map[string]string{}
		}
		dst.Env[k] = v
		mark(srcs, "env."+k, layer, false)
	}
	// services / forward 按 name 合并：同名的后面层整条覆盖，新名字追加在后。
	// 不走 union 是因为它们是结构体，"同一个 db 换个镜像"必须是覆盖而不是两条。
	if md.IsDefined("services") {
		dst.Services = mergeServices(dst.Services, src.Services)
		mark(srcs, "services", layer, true)
	}
	if md.IsDefined("forward") {
		dst.Forward = mergeForwards(dst.Forward, src.Forward)
		mark(srcs, "forward", layer, true)
	}
	// prep.secret 是 id -> 宿主机路径的 map，和 agents 一样逐键合并。
	for id, path := range src.Secret() {
		if !md.IsDefined("prep", "secret", id) {
			continue
		}
		if dst.Prep.Secret == nil {
			dst.Prep.Secret = map[string]string{}
		}
		dst.Prep.Secret[id] = path
		mark(srcs, "prep.secret."+id, layer, false)
	}
	for name, a := range src.Agents {
		cur, touched := dst.Agents[name], false
		df, sf := agentFields(&cur), agentFields(&a)
		for i := range df {
			if !md.IsDefined("agents", name, df[i].Key) {
				continue
			}
			*df[i].Str = *sf[i].Str
			mark(srcs, "agents."+name+"."+df[i].Key, layer, false)
			touched = true
		}
		if !touched {
			continue
		}
		if dst.Agents == nil {
			dst.Agents = map[string]Agent{}
		}
		dst.Agents[name] = cur
	}
}

func mergeServices(old, add []Service) []Service {
	out := append([]Service(nil), old...)
	for _, s := range add {
		if i := indexFunc(len(out), func(i int) bool { return out[i].Name == s.Name }); i >= 0 {
			out[i] = s
			continue
		}
		out = append(out, s)
	}
	return out
}

func mergeForwards(old, add []Forward) []Forward {
	out := append([]Forward(nil), old...)
	for _, f := range add {
		if i := indexFunc(len(out), func(i int) bool { return out[i].Name == f.Name }); i >= 0 {
			out[i] = f
			continue
		}
		out = append(out, f)
	}
	return out
}

func indexFunc(n int, eq func(int) bool) int {
	for i := 0; i < n; i++ {
		if eq(i) {
			return i
		}
	}
	return -1
}

// agentFields 是 agents.<name> 下可配置的字段，顺序就是 sbx config show 的输出顺序。
func agentFields(a *Agent) []binding {
	return []binding{
		{Key: "version", Str: &a.Version},
		{Key: "api_key_env", Str: &a.APIKeyEnv},
		{Key: "api_key_file", Str: &a.APIKeyFile},
	}
}

func mark(srcs map[string]Source, key, layer string, list bool) {
	s := srcs[key]
	s.List = list
	if len(s.Layers) == 0 || s.Layers[len(s.Layers)-1] != layer {
		s.Layers = append(s.Layers, layer)
	}
	srcs[key] = s
}

// union 取并集并保持先后顺序：前面各层给的排在前面，新来的追加在后。
// 代价是列表项只能加不能减——要去掉内置默认值里的某一项，目前没有办法（design §9.1）。
func union(old, add []string) []string {
	have := map[string]bool{}
	out := append([]string(nil), old...)
	for _, v := range out {
		have[v] = true
	}
	for _, v := range add {
		if !have[v] {
			have[v] = true
			out = append(out, v)
		}
	}
	return out
}

// Show 把生效配置渲染成「key / 值 / 来源」三列，给 sbx config show 用。
func (ld Loaded) Show() [][3]string {
	var rows [][3]string
	for _, b := range bindings(&ld.Config) {
		rows = append(rows, [3]string{b.Key, b.render(), ld.Sources[b.Key].String()})
	}
	ids := make([]string, 0, len(ld.Config.Prep.Secret))
	for id := range ld.Config.Prep.Secret {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		key := "prep.secret." + id
		rows = append(rows, [3]string{key, ld.Config.Prep.Secret[id], ld.Sources[key].String()})
	}
	for _, k := range sortedMapKeys(ld.Config.Env) {
		key := "env." + k
		rows = append(rows, [3]string{key, strconv.Quote(ld.Config.Env[k]), ld.Sources[key].String()})
	}
	for _, s := range ld.Config.Services {
		rows = append(rows, [3]string{"services." + s.Name, fmt.Sprintf("%s port %d", s.Image, s.Port), ld.Sources["services"].String()})
	}
	for _, f := range ld.Config.Forward {
		rows = append(rows, [3]string{"forward." + f.Name, fmt.Sprintf("%d -> %s", f.Port, f.To), ld.Sources["forward"].String()})
	}
	names := make([]string, 0, len(ld.Config.Agents))
	for n := range ld.Config.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ag := ld.Config.Agents[n]
		for _, f := range agentFields(&ag) {
			// 没配过的 api_key_* 不占一行，免得把表撑开
			if *f.Str == "" && f.Key != "version" {
				continue
			}
			key := "agents." + n + "." + f.Key
			rows = append(rows, [3]string{key, strconv.Quote(*f.Str), ld.Sources[key].String()})
		}
	}
	return rows
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (b binding) render() string {
	switch {
	case b.Str != nil:
		return strconv.Quote(*b.Str)
	case b.F64 != nil:
		return strconv.FormatFloat(*b.F64, 'g', -1, 64)
	case b.Int != nil:
		return strconv.Itoa(*b.Int)
	case b.Bool != nil:
		return strconv.FormatBool(*b.Bool)
	case b.List != nil:
		if len(*b.List) == 0 {
			return "[]"
		}
		q := make([]string, len(*b.List))
		for i, v := range *b.List {
			q[i] = strconv.Quote(v)
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	return ""
}
