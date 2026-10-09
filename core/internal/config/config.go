// Package config 加载 sbx 配置。四层：内置默认值 → ~/.sbx/config.toml →
// <repo>/.sbx/sandbox.toml → ~/.sbx/workspaces/<ws>.toml（design §9.1、ADR 0009）。
// 标量后者覆盖前者，列表取并集；每个字段的来源记在 Loaded.Sources 里（sbx config show）。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type Network struct {
	Upstream string   `toml:"upstream"`
	Proxy    string   `toml:"proxy"`
	Mode     string   `toml:"mode"`
	CloudMCP bool     `toml:"cloud_mcp"`
	Allow    []string `toml:"allow"`
}

type Resources struct {
	CPUs   float64 `toml:"cpus"`
	Memory string  `toml:"memory"`
	Pids   int     `toml:"pids"`
}

type Deps struct {
	Mask []string `toml:"mask"`
}

// MemoryBytes 把 resources.memory（3g / 512m / 1024）换成字节数。
// 格式由 Validate 保证，这里解析不出来就返回 0，调用方按"算不出来"处理。
func (r Resources) MemoryBytes() int64 {
	m := strings.TrimSpace(r.Memory)
	if m == "" {
		return 0
	}
	unit := int64(1)
	switch last := m[len(m)-1]; last {
	case 'b', 'B':
		m = m[:len(m)-1]
	case 'k', 'K':
		unit, m = 1<<10, m[:len(m)-1]
	case 'm', 'M':
		unit, m = 1<<20, m[:len(m)-1]
	case 'g', 'G':
		unit, m = 1<<30, m[:len(m)-1]
	}
	v, err := strconv.ParseFloat(m, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return int64(v * float64(unit))
}

type Agent struct {
	Version string `toml:"version"`
	// APIKeyEnv / APIKeyFile 二选一，配了就优先于订阅登录（design §7.1）。
	// 这两个键只能写在全局层或工作区层——项目层的红线会拦住它们（M2-2）。
	APIKeyEnv  string `toml:"api_key_env"`
	APIKeyFile string `toml:"api_key_file"`
}

type Config struct {
	DefaultAgent string `toml:"default_agent"`
	Profile      string `toml:"profile"`
	// Image 直接指定一个现成镜像当底，覆盖 profile（M3-2、design §5.2）。
	// design 原来写的是 profile.image，但 profile 是标量，TOML 里没法再当表用，
	// 所以单开一个顶层键。
	Image      string `toml:"image"`
	MaxRunning int    `toml:"max_running"`
	// OnIdle / OnExit 是在容器里执行的通知命令（M3-10、design §7.2）。
	// 它们只能写在全局层或工作区层——项目层的红线会拦住（跟着仓库走的配置
	// 不该决定沙箱里执行什么命令）。
	OnIdle         string           `toml:"on_idle"`
	OnExit         string           `toml:"on_exit"`
	NotifyThrottle int              `toml:"notify_throttle"` // 秒；两次同类通知的最小间隔
	Network        Network          `toml:"network"`
	Resources      Resources        `toml:"resources"`
	Deps           Deps             `toml:"deps"`
	Agents         map[string]Agent `toml:"agents"`
}

// profileMasks 是各 Profile 默认要遮盖的依赖目录（M3-5、ADR 0008）。
// 这些目录在容器里各挂一个独立 volume 盖住，装依赖不会落到宿主机的 worktree 里。
var profileMasks = map[string][]string{
	"web-go":  {"node_modules", ".next"},
	"py-rust": {".venv", "target"},
}

// ProfileMasks 返回某个 Profile 的默认遮盖项；不认识的 Profile 退回通用的一条。
func ProfileMasks(profile string) []string {
	if m, ok := profileMasks[profile]; ok {
		return append([]string(nil), m...)
	}
	return []string{"node_modules"}
}

// Default 返回内置默认值（ADR 0012 修订后的 B 方案）。
func Default() Config {
	return Config{
		DefaultAgent: "claude",
		Profile:      "web-go",
		MaxRunning:   3,
		// Notification 是"等人处理"时反复触发的事件，不节流会刷屏
		NotifyThrottle: 600,
		// 默认不拦截出网（2026-10-08 决定，ADR 0005 修订）：白名单改成按需开启，
		// 写 network.mode = "allowlist" 或 sbx run --net allowlist 才生效。
		Network:   Network{Proxy: "shared", Mode: "open"},
		Resources: Resources{CPUs: 2, Memory: "3g", Pids: 1024},
		Deps:      Deps{Mask: ProfileMasks("web-go")},
		Agents:    map[string]Agent{"claude": {Version: "latest"}},
	}
}

// Load 读取 path 并覆盖到默认值上。文件不存在时返回默认值。
// 未知字段以 warnings 返回，不报错。
func Load(path string) (cfg Config, warnings []string, err error) {
	cfg = Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil, nil
	}
	if err != nil {
		return cfg, nil, err
	}
	return parse(string(data), path, cfg)
}

func parse(data, src string, base Config) (Config, []string, error) {
	cfg := base
	md, err := toml.Decode(data, &cfg)
	if err != nil {
		return base, nil, fmt.Errorf("%s: %w", src, err)
	}
	var warnings []string
	for _, k := range md.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("%s: 未知字段 %s（已忽略）", src, k.String()))
	}
	if err := cfg.Validate(); err != nil {
		return base, warnings, fmt.Errorf("%s: %w", src, err)
	}
	return cfg, warnings, nil
}

var memRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[bkmgBKMG]?$`)

// FieldError 带上出错字段的点号 key，这样合并配置时能回答"这个值是哪一层给的"。
type FieldError struct {
	Key string
	Msg string
}

func (e *FieldError) Error() string { return e.Msg }

func fieldf(key, format string, args ...any) error {
	return &FieldError{Key: key, Msg: fmt.Sprintf(format, args...)}
}

func (c Config) Validate() error {
	if !memRe.MatchString(c.Resources.Memory) {
		return fieldf("resources.memory", "resources.memory 非法：%q（示例：3g、512m）", c.Resources.Memory)
	}
	if c.Resources.CPUs <= 0 {
		return fieldf("resources.cpus", "resources.cpus 必须大于 0")
	}
	if c.Resources.Pids <= 0 {
		return fieldf("resources.pids", "resources.pids 必须大于 0")
	}
	if c.MaxRunning <= 0 {
		return fieldf("max_running", "max_running 必须大于 0")
	}
	if c.NotifyThrottle < 0 {
		return fieldf("notify_throttle", "notify_throttle 不能是负数")
	}
	for name, ag := range c.Agents {
		if ag.APIKeyEnv != "" && ag.APIKeyFile != "" {
			return fieldf("agents."+name+".api_key_env",
				"agents.%s 同时配了 api_key_env 和 api_key_file，只能二选一", name)
		}
	}
	switch c.Network.Mode {
	case "allowlist", "open":
	default:
		return fieldf("network.mode", "network.mode 只能是 allowlist 或 open：%q", c.Network.Mode)
	}
	switch c.Network.Proxy {
	case "shared", "dedicated":
	default:
		return fieldf("network.proxy", "network.proxy 只能是 shared 或 dedicated：%q", c.Network.Proxy)
	}
	if _, _, err := c.Upstream(); err != nil {
		return err
	}
	return nil
}

// Upstream 解析 network.upstream，返回 host 和 port。未配置时 host 为空。
func (c Config) Upstream() (host string, port string, err error) {
	s := strings.TrimSpace(c.Network.Upstream)
	if s == "" {
		return "", "", nil
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return "", "", fieldf("network.upstream", "network.upstream 非法：%q（示例：http://host.docker.internal:7890）", c.Network.Upstream)
	}
	if u.Scheme != "http" {
		return "", "", fieldf("network.upstream", "network.upstream 只支持 http 上游：%q", c.Network.Upstream)
	}
	return u.Hostname(), u.Port(), nil
}

// ClaudeVersion 返回 agents.claude.version，默认 latest。
func (c Config) ClaudeVersion() string {
	if a, ok := c.Agents["claude"]; ok && a.Version != "" {
		return a.Version
	}
	return "latest"
}

// String 以 TOML 形式输出生效配置（--verbose 用）。
func (c Config) String() string {
	var b strings.Builder
	toml.NewEncoder(&b).Encode(c)
	return b.String()
}
