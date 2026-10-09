package proxy

import (
	"net"
	"regexp"
	"sort"
	"strings"
)

var (
	urlRe  = regexp.MustCompile(`(?i)\bhttps?://([^\s'"` + "`" + `<>|;)&]+)`)
	hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// HostsIn 从若干段 shell 命令里抽出 http(s) URL 的主机名，用来把 webhook 自动加进
// 该 Task 的白名单（design §6.3 的「通知域名」层、M3-10）。
//
// 这里**不解析 shell**，只在全文上做正则扫描：管道、引号、重定向一律无视。
// 误报的代价只是多放行一个主机，而且是用户自己写进个人配置的（项目层写不了，
// 见 config 的第三条红线）；漏报的代价是通知静默失败，查起来麻烦得多。
//
// skipped 里是扫到了但用不上的：主机名含变量（静态求不出值，不猜），
// 或者是裸 IP（squid 的 dstdomain 对 IP 不生效，放进去只会让人以为放行了）。
// 这两种都要让用户自己 sbx net allow。
func HostsIn(cmds ...string) (hosts, skipped []string) {
	seen, skip := map[string]bool{}, map[string]bool{}
	for _, cmd := range cmds {
		for _, m := range urlRe.FindAllStringSubmatch(cmd, -1) {
			h := authority(m[1])
			switch {
			case h == "":
			case strings.ContainsAny(h, "$`{}"), net.ParseIP(h) != nil, !hostRe.MatchString(h):
				skip[h] = true
			default:
				seen[h] = true
			}
		}
	}
	return keys(seen), keys(skip)
}

// authority 从 URL 的 "host:port/path?x" 里取出主机名。
func authority(s string) string {
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if strings.HasPrefix(s, "[") { // IPv6
		if i := strings.Index(s, "]"); i > 0 {
			return s[1:i]
		}
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.Trim(s, "."))
}

func keys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
