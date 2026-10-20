package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Docker Desktop 的透明代理（M4-19）。
//
// 这一项是被一次真实故障逼出来的：prep 构建拉私有 Go module，报
// "certificate signed by unknown authority"，修好 CA 之后变成 EOF。真正的原因
// 在两层之外——Docker Desktop 开着 VpnKitTransparentProxy，它会**嗅探容器出站
// 连接的 SNI**，然后向宿主机代理发 `CONNECT <嗅到的域名>:443`。于是：
//
//	带 SNI   → CONNECT gitlab.corp.example:443 → 代理按域名判，内网域名没规则 → 发给节点 → 断
//	不带 SNI → CONNECT 10.10.16.50:443         → 代理按私有 IP 判，直连 → 通
//
// 宿主机自己走的是第二条路（直接连 IP），所以"宿主机能连、容器不能连"，而症状
// 落在 TLS 层，看起来像对端的 SNI 策略。
//
// 关键在于 **boundary.md §2.8 坑一 不够**：那条说的是 `sbx prep build` 要清掉
// HTTP_PROXY / HTTPS_PROXY。确实清了，但透明代理根本不在环境变量里，清不掉。
//
// 这个检查只读一个 JSON 文件，不需要网络也不需要 docker，所以它在 --quick 里
// 也跑——它能在构建失败**之前**就说出问题。

// ddProxy 是 Docker Desktop 设置里和代理相关的那几个字段。
type ddProxy struct {
	Found       bool
	Path        string
	Mode        string // "system" | "manual" | "off"
	HTTP        string // manual 模式下的上游
	HTTPS       string
	Transparent bool     // VpnKitTransparentProxy：按 SNI 改写目的地的那个开关
	Exclude     []string // Bypass 列表，已拆成条目
}

// ddSettings 只取我们要的键，其余忽略——这个文件有上百个字段，而且会变。
type ddSettings struct {
	ProxyHTTPMode          *string `json:"ProxyHTTPMode"`
	OverrideProxyHTTP      *string `json:"OverrideProxyHTTP"`
	OverrideProxyHTTPS     *string `json:"OverrideProxyHTTPS"`
	OverrideProxyExclude   *string `json:"OverrideProxyExclude"`
	VpnKitTransparentProxy *bool   `json:"VpnKitTransparentProxy"`
}

// ddSettingsPaths 按新旧顺序给出候选路径。Docker Desktop 在某个版本把
// settings.json 改成了 settings-store.json，两个都可能存在。
func ddSettingsPaths(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		d := filepath.Join(home, "Library", "Group Containers", "group.com.docker")
		return []string{filepath.Join(d, "settings-store.json"), filepath.Join(d, "settings.json")}
	case "windows":
		if ad := os.Getenv("APPDATA"); ad != "" {
			d := filepath.Join(ad, "Docker")
			return []string{filepath.Join(d, "settings-store.json"), filepath.Join(d, "settings.json")}
		}
		return nil
	default:
		d := filepath.Join(home, ".docker", "desktop")
		return []string{filepath.Join(d, "settings-store.json"), filepath.Join(d, "settings.json")}
	}
}

// readDDProxy 读 Docker Desktop 的代理设置。读不到就是 Found=false——
// Docker Engine（非 Desktop）没有这个文件，那种环境下根本没有这一层。
func readDDProxy(home string) ddProxy {
	for _, p := range ddSettingsPaths(home) {
		b, err := os.ReadFile(p) //nolint:gosec // 路径由 ddSettingsPaths 构造
		if err != nil {
			continue
		}
		var s ddSettings
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		d := ddProxy{Found: true, Path: p}
		if s.ProxyHTTPMode != nil {
			d.Mode = *s.ProxyHTTPMode
		}
		if s.OverrideProxyHTTP != nil {
			d.HTTP = *s.OverrideProxyHTTP
		}
		if s.OverrideProxyHTTPS != nil {
			d.HTTPS = *s.OverrideProxyHTTPS
		}
		if s.VpnKitTransparentProxy != nil {
			d.Transparent = *s.VpnKitTransparentProxy
		}
		if s.OverrideProxyExclude != nil {
			d.Exclude = splitExclude(*s.OverrideProxyExclude)
		}
		return d
	}
	return ddProxy{}
}

// splitExclude 拆 Bypass 列表。Docker Desktop 存成一个字符串，用户可能用逗号、
// 分号、空格或换行分隔，全都认。
func splitExclude(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
	var out []string
	for _, e := range f {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// active 报告容器流量是不是真的会被交给某个代理。
//
// sysProxy 是宿主机系统代理的地址（没有就是空串）：`system` 模式下 Docker
// Desktop 跟随系统设置，所以系统里没配代理时这一层其实是空转，不该报警。
func (d ddProxy) active(sysProxy string) bool {
	switch d.Mode {
	case "manual":
		return d.HTTP != "" || d.HTTPS != ""
	case "system":
		return sysProxy != ""
	default: // "off" / 空 / 未知
		return false
	}
}

// excludes 报告某个主机名或地址是否在 Bypass 列表里。
//
// 条目有四种形态：CIDR、裸 IP、`*.foo.com`/`.foo.com` 后缀、以及裸域名。
// 裸域名按"自身或其子域"匹配，和浏览器、curl 的 no_proxy 一致。
func (d ddProxy) excludes(host string, ip net.IP) bool {
	for _, e := range d.Exclude {
		if e == "*" {
			return true
		}
		if _, cidr, err := net.ParseCIDR(e); err == nil {
			if ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if parsed := net.ParseIP(e); parsed != nil {
			if ip != nil && parsed.Equal(ip) {
				return true
			}
			continue
		}
		suffix := strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
		if host != "" && (strings.EqualFold(host, suffix) ||
			strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(suffix))) {
			return true
		}
	}
	return false
}

// privateProbes 是用来验 Bypass 列表覆盖面的代表地址，一个网段一个。
var privateProbes = []string{"10.0.0.1", "172.16.0.1", "192.168.0.1"}

// coversPrivate 报告 Bypass 列表是否覆盖了三个 RFC 1918 网段。
// 缺哪个就返回哪个——提示里要说得出"少了什么"才有用。
func (d ddProxy) coversPrivate() (missing []string) {
	for _, p := range privateProbes {
		if !d.excludes("", net.ParseIP(p)) {
			missing = append(missing, p)
		}
	}
	return missing
}

// systemProxy 返回宿主机当前的系统代理地址，没有就是空串。
//
// macOS 上问 scutil：环境变量里的 http_proxy 是 shell 的事，和 Docker Desktop
// 跟随的那个"系统代理"不是一回事。其他平台上就看环境变量。
func systemProxy() string {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("scutil", "--proxy").Output()
		if err != nil {
			return ""
		}
		return parseSCUtilProxy(string(out))
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// parseSCUtilProxy 从 `scutil --proxy` 的输出里取出启用着的 HTTP(S) 代理。
// 只有 Enable 为 1 才算数：这些键在关掉代理之后仍然留着旧的端口值。
func parseSCUtilProxy(out string) string {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	for _, p := range []struct{ enable, host, port string }{
		{"HTTPSEnable", "HTTPSProxy", "HTTPSPort"},
		{"HTTPEnable", "HTTPProxy", "HTTPPort"},
	} {
		if kv[p.enable] != "1" {
			continue
		}
		host, port := kv[p.host], kv[p.port]
		if host == "" {
			host = "127.0.0.1"
		}
		if port == "" {
			return host
		}
		return net.JoinHostPort(host, port)
	}
	return ""
}

// ddProxyVerdict 是纯逻辑部分，便于测试：读文件和问系统都在外面做完。
func ddProxyVerdict(d ddProxy, sysProxy string) Check {
	const name = "dd-proxy"
	if !d.Found {
		return skip(name, "no Docker Desktop settings file; this layer does not exist here")
	}
	if !d.active(sysProxy) {
		return ok(name, fmt.Sprintf("container traffic is not sent through a proxy (mode %q)", modeOrUnset(d.Mode)))
	}
	via := sysProxy
	if d.Mode == "manual" {
		via = firstNonEmpty(d.HTTPS, d.HTTP)
	}
	if !d.Transparent {
		// 非透明模式下 Docker 只注入环境变量，而 sbx prep build 会清掉它们，
		// Task 容器也只认 squid。这一层碰不到我们。
		return ok(name, fmt.Sprintf("proxy %s is injected as env vars only, which sbx clears", via))
	}
	missing := d.coversPrivate()
	if len(missing) == 0 {
		return ok(name, fmt.Sprintf("transparent proxy to %s, with the private ranges bypassed", via))
	}
	return warn(name,
		fmt.Sprintf("Docker Desktop sends every container connection to %s and sniffs its SNI; the bypass list does not cover %s",
			via, strings.Join(missing, ", ")),
		"that proxy decides by domain name, so an internal host is handed to a proxy node that cannot reach it — the failure surfaces as a TLS error, not as a routing one. "+
			"Add your private ranges and internal domains under Docker Desktop → Settings → Resources → Proxies → Bypass, for example: "+
			"*.corp.example,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16")
}

func modeOrUnset(m string) string {
	if m == "" {
		return "unset"
	}
	return m
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// checkDDProxy 是 doctor 里的那一项。只读文件 + 问一次系统代理，不碰网络，
// 所以 --quick 也跑：它是唯一能在 sbx prep build 失败之前说出这件事的检查。
func checkDDProxy(_ *App, _ docEnv) Check {
	d, err := currentDDProxy()
	if err != nil {
		return skip("dd-proxy", "cannot locate your home directory: "+err.Error())
	}
	return ddProxyVerdict(d, systemProxy())
}

// currentDDProxy 读当前用户的 Docker Desktop 设置。注意用的是**操作系统的** home，
// 不是 a.Home（那是 ~/.sbx，测试里会被 SBX_HOME 换掉）。
func currentDDProxy() (ddProxy, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ddProxy{}, err
	}
	return readDDProxy(home), nil
}
