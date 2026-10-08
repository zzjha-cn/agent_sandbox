// Package config 加载 sbx 配置。M1 只有两层：内置默认值 + ~/.sbx/config.toml（ADR 0009）。
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

func (c Config) Validate() error {
	if !memRe.MatchString(c.Resources.Memory) {
		return fmt.Errorf("resources.memory 非法：%q（示例：3g、512m）", c.Resources.Memory)
	}
	if c.Resources.CPUs <= 0 {
		return fmt.Errorf("resources.cpus 必须大于 0")
	}
	if c.Resources.Pids <= 0 {
		return fmt.Errorf("resources.pids 必须大于 0")
	}
	if c.MaxRunning <= 0 {
		return fmt.Errorf("max_running 必须大于 0")
	}
	switch c.Network.Mode {
	case "allowlist", "open":
	default:
		return fmt.Errorf("network.mode 只能是 allowlist 或 open：%q", c.Network.Mode)
	}
	switch c.Network.Proxy {
	case "shared", "dedicated":
	default:
		return fmt.Errorf("network.proxy 只能是 shared 或 dedicated：%q", c.Network.Proxy)
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
		return "", "", fmt.Errorf("network.upstream 非法：%q（示例：http://host.docker.internal:7890）", c.Network.Upstream)
	}
	if u.Scheme != "http" {
		return "", "", fmt.Errorf("network.upstream 只支持 http 上游：%q", c.Network.Upstream)
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
