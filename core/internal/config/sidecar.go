package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Service 是跟着 Task 一起起来的伴生容器（M4-10、boundary.md §3.2）。
//
// 字段只有这四个是刻意的：没有地址、没有宿主机路径、没有能力声明，所以它**可以写在
// 项目层**、可以进仓库——"本项目要一个 postgres" 是事实陈述，不是对你这台机器的决定。
// 想挂宿主机目录或者提权的人会发现这里没有那个字段，不是被拒，是根本没有。
type Service struct {
	Name  string            `toml:"name"`
	Image string            `toml:"image"`
	Env   map[string]string `toml:"env"`
	Port  int               `toml:"port"`
}

// Forward 是一条单目标 TCP 洞（M4-12、boundary.md §3.3）。
// 和 Service 正好相反：它指向一个真实地址，所以**只能写在全局层或工作区层**，
// 项目层整棵 forward 子树都被红线挡住。
type Forward struct {
	Name string `toml:"name"`
	Port int    `toml:"port"` // Task 侧的端口
	To   Target `toml:"to"`
	// Host 非空时给 Task 容器加一条 --add-host，让容器里仍用真实域名访问，
	// 保住 SNI 和证书校验（socat 是纯 TCP 中继，原样透传）。
	Host string `toml:"host"`
	// AllowPublic 解除"目标必须是私有地址"的限制。少数公司在内部用公网 IP 段，
	// 不给这个口子他们完全没法用；给了就逐条醒目警告。
	AllowPublic bool `toml:"allow_public"`
}

// Target 是 forward 的目标。TOML 里可以写成裸端口号（= 宿主机）或 "host:port"，
// 所以它既要吃整数也要吃字符串。
type Target struct{ S string }

func (t *Target) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case int64:
		t.S = strconv.FormatInt(x, 10)
	case string:
		t.S = strings.TrimSpace(x)
	default:
		return fmt.Errorf("to must be a port number or \"host:port\", not %T", v)
	}
	return nil
}

func (t Target) MarshalTOML() ([]byte, error) { return []byte(strconv.Quote(t.S)), nil }
func (t Target) String() string               { return t.S }

// Split 把目标拆成主机和端口。主机为空表示"宿主机"（裸端口号的简写）。
func (t Target) Split() (host string, port int, err error) {
	s := t.S
	if s == "" {
		return "", 0, fmt.Errorf("to is empty")
	}
	if n, e := strconv.Atoi(s); e == nil {
		if !validPort(n) {
			return "", 0, fmt.Errorf("to = %d is not a port number between 1 and 65535", n)
		}
		return "", n, nil
	}
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("to = %q must be a port number or \"host:port\"", s)
	}
	host, ps := strings.Trim(s[:i], "[]"), s[i+1:]
	n, e := strconv.Atoi(ps)
	if host == "" || e != nil || !validPort(n) {
		return "", 0, fmt.Errorf("to = %q must be a port number or \"host:port\"", s)
	}
	return host, n, nil
}

func validPort(n int) bool { return n >= 1 && n <= 65535 }

// sidecarName 是 services / forward 的名字规则。名字会变成 Docker 的网络别名和
// 容器名的一段，所以限制得比配置里别的字段严。
var sidecarName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,29}$`)

// validateSidecars 校验 services 和 forward，并保证两边的名字不撞车——
// 它们共用 Task 网络上的别名空间。
func (c Config) validateSidecars() error {
	seen := map[string]string{}
	for i, s := range c.Services {
		key := fmt.Sprintf("services[%d]", i)
		if !sidecarName.MatchString(s.Name) {
			return fieldf("services", "%s: name %q must be lowercase letters, digits or -", key, s.Name)
		}
		if strings.TrimSpace(s.Image) == "" {
			return fieldf("services", "%s (%s): image is required", key, s.Name)
		}
		if !validPort(s.Port) {
			return fieldf("services", "%s (%s): port must be between 1 and 65535", key, s.Name)
		}
		if where, dup := seen[s.Name]; dup {
			return fieldf("services", "%s: name %q is already used by %s", key, s.Name, where)
		}
		seen[s.Name] = key
	}
	for i, f := range c.Forward {
		key := fmt.Sprintf("forward[%d]", i)
		if !sidecarName.MatchString(f.Name) {
			return fieldf("forward", "%s: name %q must be lowercase letters, digits or -", key, f.Name)
		}
		if !validPort(f.Port) {
			return fieldf("forward", "%s (%s): port must be between 1 and 65535", key, f.Name)
		}
		if _, _, err := f.To.Split(); err != nil {
			return fieldf("forward", "%s (%s): %v", key, f.Name, err)
		}
		if where, dup := seen[f.Name]; dup {
			return fieldf("forward", "%s: name %q is already used by %s", key, f.Name, where)
		}
		seen[f.Name] = key
	}
	return nil
}
