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

type Agent struct {
	Version string `toml:"version"`
}

type Config struct {
	DefaultAgent string           `toml:"default_agent"`
	Profile      string           `toml:"profile"`
	MaxRunning   int              `toml:"max_running"`
	Network      Network          `toml:"network"`
	Resources    Resources        `toml:"resources"`
	Deps         Deps             `toml:"deps"`
	Agents       map[string]Agent `toml:"agents"`
}

// Default 返回内置默认值（ADR 0012 修订后的 B 方案）。
func Default() Config {
	return Config{
		DefaultAgent: "claude",
		Profile:      "web-go",
		MaxRunning:   3,
		// 默认不拦截出网（2026-10-08 决定，ADR 0005 修订）：白名单改成按需开启，
		// 写 network.mode = "allowlist" 或 sbx run --net allowlist 才生效。
		Network:   Network{Proxy: "shared", Mode: "open"},
		Resources: Resources{CPUs: 2, Memory: "3g", Pids: 1024},
		Deps:      Deps{Mask: []string{"node_modules"}},
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
