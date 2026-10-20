package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这是 M4-19 的核心断言：代理开着、透明拦截开着、Bypass 是空的——
// 这个组合下每一个容器出站连接都会被按域名交给代理，内网目标必然断。
// 它必须在 sbx prep build 失败**之前**就被说出来。
func TestDDProxyWarnsWhenTransparentProxyHasNoBypass(t *testing.T) {
	d := ddProxy{Found: true, Path: "/x/settings-store.json", Mode: "system", Transparent: true}
	c := ddProxyVerdict(d, "127.0.0.1:7890")
	if c.Level != lvWarn {
		t.Fatalf("level = %v, want warn", c.Level)
	}
	if !strings.Contains(c.Detail, "7890") || !strings.Contains(c.Detail, "SNI") {
		t.Fatalf("detail should name the proxy and what it does: %s", c.Detail)
	}
	for _, r := range []string{"10.0.0.1", "172.16.0.1", "192.168.0.1"} {
		if !strings.Contains(c.Detail, r) {
			t.Fatalf("detail should list the uncovered range %s: %s", r, c.Detail)
		}
	}
	// 提示必须说清"代理是按名字被问的",否则用户会以为加了 IP 段就够了——
	// 这正是这次故障里实际发生的事。
	if !strings.Contains(c.Fix, "by domain name") || !strings.Contains(c.Fix, "Bypass") {
		t.Fatalf("fix must explain the name-vs-address trap: %s", c.Fix)
	}
}

func TestDDProxyQuietWhenBypassCoversPrivateRanges(t *testing.T) {
	d := ddProxy{Found: true, Mode: "manual", HTTPS: "http://127.0.0.1:7890", Transparent: true,
		Exclude: splitExclude("*.corp.example,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16")}
	if c := ddProxyVerdict(d, ""); c.Level != lvOK {
		t.Fatalf("level = %v (%s), want ok", c.Level, c.Detail)
	}
}

// system 模式但系统里根本没配代理时这一层是空转。报警会变成噪音,
// 而 doctor 里的噪音会让人连真的警告一起忽略。
func TestDDProxyQuietWhenSystemHasNoProxy(t *testing.T) {
	d := ddProxy{Found: true, Mode: "system", Transparent: true}
	if c := ddProxyVerdict(d, ""); c.Level != lvOK {
		t.Fatalf("level = %v (%s), want ok", c.Level, c.Detail)
	}
}

// 非透明模式下 Docker 只注入环境变量,而 sbx prep build 会清掉它们
// (boundary.md §2.8 坑一)。那一层我们已经处理过了。
func TestDDProxyQuietWhenInterceptionIsOff(t *testing.T) {
	d := ddProxy{Found: true, Mode: "manual", HTTP: "http://127.0.0.1:7890"}
	c := ddProxyVerdict(d, "")
	if c.Level != lvOK {
		t.Fatalf("level = %v, want ok", c.Level)
	}
	if !strings.Contains(c.Detail, "env vars") {
		t.Fatalf("detail should say why this is harmless: %s", c.Detail)
	}
}

// 不是 Docker Desktop 的环境(纯 Docker Engine)根本没有这一层。
func TestDDProxySkipsWithoutASettingsFile(t *testing.T) {
	if c := ddProxyVerdict(ddProxy{}, "127.0.0.1:7890"); c.Level != lvSkip {
		t.Fatalf("level = %v, want skip", c.Level)
	}
}

func TestSplitExcludeAcceptsTheUsualSeparators(t *testing.T) {
	got := splitExclude("*.corp.example, 10.0.0.0/8;192.168.0.0/16\n\tlocalhost")
	want := []string{"*.corp.example", "10.0.0.0/8", "192.168.0.0/16", "localhost"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestExcludesMatchesCIDRsIPsAndDomainSuffixes(t *testing.T) {
	d := ddProxy{Exclude: splitExclude("10.0.0.0/8,192.168.1.5,*.corp.example,other.example")}
	cases := []struct {
		host string
		ip   string
		want bool
	}{
		{"", "10.42.0.50", true},                // CIDR
		{"", "192.168.1.5", true},               // 裸 IP
		{"", "192.168.1.6", false},              // 裸 IP 不该当成网段
		{"gitlab.corp.example", "", true},       // *.foo 后缀
		{"corp.example", "", true},              // 后缀条目也该匹配域本身
		{"deep.sub.corp.example", "", true},     // 多级子域
		{"sub.other.example", "", true},         // 裸域名按子域匹配
		{"other.example.evil.com", "", false},   // 不能被前缀骗过去
		{"gitlab.corp.example.cn", "", false},   // 也不能被同前缀的别的域骗过去
		{"anything.else", "203.0.113.1", false}, // 都不沾
	}
	for _, c := range cases {
		var ip net.IP
		if c.ip != "" {
			ip = net.ParseIP(c.ip)
		}
		if got := d.excludes(c.host, ip); got != c.want {
			t.Fatalf("excludes(%q, %q) = %v, want %v", c.host, c.ip, got, c.want)
		}
	}
}

func TestExcludesHonoursWildcardAll(t *testing.T) {
	d := ddProxy{Exclude: []string{"*"}}
	if !d.excludes("anything.example", net.ParseIP("203.0.113.1")) {
		t.Fatal("a bare * bypasses everything")
	}
}

// macOS 在关掉代理之后仍然把端口留在 scutil 的输出里。只看端口的话
// 会把"代理已关"读成"代理开着",于是给出一个完全无关的警告。
func TestParseSCUtilProxyRequiresTheEnableFlag(t *testing.T) {
	disabled := "  HTTPEnable : 0\n  HTTPPort : 7890\n  HTTPProxy : 127.0.0.1\n"
	if got := parseSCUtilProxy(disabled); got != "" {
		t.Fatalf("a disabled proxy must read as none, got %q", got)
	}
	enabled := "  HTTPEnable : 1\n  HTTPPort : 7890\n  HTTPProxy : 127.0.0.1\n"
	if got := parseSCUtilProxy(enabled); got != "127.0.0.1:7890" {
		t.Fatalf("got %q", got)
	}
	// HTTPS 优先：容器连的是 443,走的是这一条。
	both := "  HTTPEnable : 1\n  HTTPPort : 1\n  HTTPProxy : 1.1.1.1\n" +
		"  HTTPSEnable : 1\n  HTTPSPort : 2\n  HTTPSProxy : 2.2.2.2\n"
	if got := parseSCUtilProxy(both); got != "2.2.2.2:2" {
		t.Fatalf("got %q", got)
	}
}

func TestReadDDProxyParsesTheRealShape(t *testing.T) {
	dir := t.TempDir()
	writeDDSettings(t, dir, `{"ProxyHTTPMode":"system","VpnKitTransparentProxy":true,`+
		`"OverrideProxyExclude":"*.corp.example,10.0.0.0/8","SomethingElse":42}`)
	d := readDDProxy(dir)
	if !d.Found || d.Mode != "system" || !d.Transparent {
		t.Fatalf("parsed = %+v", d)
	}
	if len(d.Exclude) != 2 {
		t.Fatalf("exclude = %v", d.Exclude)
	}
	if d.Path == "" {
		t.Fatal("Path must be filled so the fix can cite it")
	}
}

func TestReadDDProxyToleratesGarbage(t *testing.T) {
	dir := t.TempDir()
	writeDDSettings(t, dir, "not json at all")
	if d := readDDProxy(dir); d.Found {
		t.Fatalf("unparseable settings must not be reported as found: %+v", d)
	}
	if d := readDDProxy(t.TempDir()); d.Found {
		t.Fatal("a missing file must not be reported as found")
	}
}

// writeDDSettings 把内容写到当前平台上 readDDProxy 会去找的第一个路径,
// 这样这组测试在 macOS 和 Linux 上验的是同一件事。
func writeDDSettings(t *testing.T, home, body string) {
	t.Helper()
	paths := ddSettingsPaths(home)
	if len(paths) == 0 {
		t.Skip("no Docker Desktop settings path on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(paths[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
