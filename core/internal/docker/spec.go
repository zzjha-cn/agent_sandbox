package docker

import (
	"sort"
	"strconv"
)

// Mount 是一条挂载。Volume 为 true 时 Source 是 volume 名，否则是宿主机路径。
type Mount struct {
	Source   string
	Target   string
	Volume   bool
	ReadOnly bool
}

// Resources 是容器的资源上限。
type Resources struct {
	CPUs   float64
	Memory string
	Pids   int
}

// RunSpec 描述一个 docker run -d。
type RunSpec struct {
	Name       string
	Image      string
	Labels     map[string]string
	Network    string
	Aliases    []string
	Mounts     []Mount
	Env        []string // KEY=VALUE，保持调用方给定的顺序
	User       string
	Workdir    string
	Resources  Resources
	AddHosts   []string
	LogMaxSize string // 非空时限制 json-file 日志大小（保留 2 个文件）
	Entrypoint string
	Cmd        []string
}

// Args 把 RunSpec 转换成 docker 参数。纯函数，挂载顺序保持不变
// （依赖 volume 必须排在 worktree 之后才能遮盖，ADR 0008）。
func (s RunSpec) Args() []string {
	args := []string{"run", "-d"}
	if s.Name != "" {
		args = append(args, "--name", s.Name)
	}
	args = append(args, labelArgs(s.Labels)...)
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	for _, a := range s.Aliases {
		args = append(args, "--network-alias", a)
	}
	for _, m := range s.Mounts {
		args = append(args, "--mount", m.flag())
	}
	for _, e := range s.Env {
		args = append(args, "-e", e)
	}
	if s.User != "" {
		args = append(args, "-u", s.User)
	}
	if s.Workdir != "" {
		args = append(args, "-w", s.Workdir)
	}
	if s.Resources.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(s.Resources.CPUs, 'f', -1, 64))
	}
	if s.Resources.Memory != "" {
		args = append(args, "--memory", s.Resources.Memory)
	}
	if s.Resources.Pids > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(s.Resources.Pids))
	}
	for _, h := range s.AddHosts {
		args = append(args, "--add-host", h)
	}
	if s.LogMaxSize != "" {
		args = append(args, "--log-opt", "max-size="+s.LogMaxSize, "--log-opt", "max-file=2")
	}
	if s.Entrypoint != "" {
		args = append(args, "--entrypoint", s.Entrypoint)
	}
	args = append(args, s.Image)
	return append(args, s.Cmd...)
}

func (m Mount) flag() string {
	typ := "bind"
	if m.Volume {
		typ = "volume"
	}
	f := "type=" + typ + ",source=" + m.Source + ",target=" + m.Target
	if m.ReadOnly {
		f += ",readonly"
	}
	return f
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
