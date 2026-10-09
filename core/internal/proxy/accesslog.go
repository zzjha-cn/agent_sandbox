package proxy

import (
	"bufio"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"sandx/internal/docker"
)

// logPath 是 squid 容器里的 access.log；轮转后的上一份是 access.log.0。
const logPath = "/var/log/squid/access.log"

// Entry 是 access.log 的一行。对应 squid.conf.tmpl 里的
// logformat sbx %ts.%03tu %un %>a %Ss/%>Hs %rm %ru %Sh/%<a
type Entry struct {
	TS     time.Time
	TaskID string // 代理用户名 <ws>.<task>；未通过认证时是 "-"
	Result string // 例如 TCP_DENIED
	Status int    // 例如 403
	Method string
	Host   string // 去掉端口的主机名
}

// Denied 报告这一条是不是被拒的请求。
func (e Entry) Denied() bool { return e.Status == 403 || e.Status == 407 }

// ParseAccessLog 解析日志，跳过格式不对的行（squid 自己也会写一些别的行）。
func ParseAccessLog(r io.Reader) []Entry {
	var out []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if e, ok := parseLine(sc.Text()); ok {
			out = append(out, e)
		}
	}
	return out
}

func parseLine(line string) (Entry, bool) {
	f := strings.Fields(line)
	if len(f) < 6 {
		return Entry{}, false
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return Entry{}, false
	}
	code, status, ok := strings.Cut(f[3], "/")
	if !ok {
		return Entry{}, false
	}
	st, err := strconv.Atoi(status)
	if err != nil {
		return Entry{}, false
	}
	return Entry{
		TS:     time.Unix(int64(secs), int64(secs*1e9)%1e9),
		TaskID: f[1],
		Result: code,
		Status: st,
		Method: f[4],
		Host:   hostOf(f[5]),
	}, true
}

// hostOf 从 CONNECT 的 "host:443" 或 GET 的 URL 里取出主机名。
func hostOf(target string) string {
	s := target
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if h, _, ok := strings.Cut(s, ":"); ok {
		s = h
	}
	return strings.ToLower(s)
}

// Kind 是被拒请求的分类（design §6.5）。
type Kind string

const (
	// KindNotAllowed 是真正需要你决定放不放行的那一类，默认只展示它。
	KindNotAllowed Kind = "不在白名单"
	// KindPolicy 是策略拦截层和已知遥测：被拒是预期行为。
	KindPolicy Kind = "策略拦截/遥测"
	// KindAuth 是代理认证失败。少量正常（有的客户端先不带凭据试一次），量大才是问题。
	KindAuth Kind = "认证失败"
)

// DeniedHost 是按域名聚合后的一行。
type DeniedHost struct {
	Host  string
	Kind  Kind
	Count int
	Last  time.Time
	Tasks []string
}

// matches 实现 squid dstdomain 的语义：以 . 开头的条目匹配该域及其子域。
func matches(host, pattern string) bool {
	if strings.HasPrefix(pattern, ".") {
		return host == pattern[1:] || strings.HasSuffix(host, pattern)
	}
	return host == pattern
}

// Classify 判断一条被拒请求属于哪一类。policy 是策略拦截层加已知遥测。
func Classify(e Entry, policy []string) Kind {
	if e.Status == 407 {
		return KindAuth
	}
	for _, p := range policy {
		if matches(e.Host, p) {
			return KindPolicy
		}
	}
	return KindNotAllowed
}

// SummarizeDenied 按域名聚合被拒请求，按次数降序。
// taskID 非空时只统计这个 Task；since 非零时只统计这之后的。
func SummarizeDenied(entries []Entry, taskID string, since time.Time, policy []string) []DeniedHost {
	agg := map[string]*DeniedHost{}
	for _, e := range entries {
		if !e.Denied() {
			continue
		}
		if taskID != "" && e.TaskID != taskID {
			continue
		}
		if !since.IsZero() && e.TS.Before(since) {
			continue
		}
		k := Classify(e, policy)
		key := string(k) + "\x00" + e.Host
		d := agg[key]
		if d == nil {
			d = &DeniedHost{Host: e.Host, Kind: k}
			agg[key] = d
		}
		d.Count++
		if e.TS.After(d.Last) {
			d.Last = e.TS
		}
		if e.TaskID != "" && e.TaskID != "-" && !contains(d.Tasks, e.TaskID) {
			d.Tasks = append(d.Tasks, e.TaskID)
		}
	}
	out := make([]DeniedHost, 0, len(agg))
	for _, d := range agg {
		sort.Strings(d.Tasks)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Host < out[j].Host
	})
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// PolicyList 是分类用的"预期会被拒"名单：策略拦截层加已知遥测。
func PolicyList() []string {
	return append(BuiltinList("policy-block"), BuiltinList("telemetry")...)
}

// AccessLog 读出 sbx-proxy 里的 access.log，连同轮转出来的上一份。
func (s Shared) AccessLog() ([]Entry, error) {
	out, err := s.Docker.Exec(s.name(), docker.ExecOpts{},
		"sh", "-c", "cat "+logPath+".0 2>/dev/null; cat "+logPath+" 2>/dev/null")
	if err != nil {
		return nil, err
	}
	return ParseAccessLog(strings.NewReader(out)), nil
}

// DeniedCounts 按 TaskID 统计「不在白名单」的被拒次数（sbx ls 的 DENIED 列）。
// 只数这一类：策略拦截（云端 MCP、遥测）和认证失败都是预期行为，放进这一列只是噪音。
// since 非零时只数这之后的——shared 模式下日志是全局的，不切窗口会把同名旧 Task 的历史算进来。
func DeniedCounts(entries []Entry, since time.Time, policy []string) map[string]int {
	out := map[string]int{}
	for _, e := range entries {
		if !e.Denied() || e.TaskID == "" || e.TaskID == "-" {
			continue
		}
		if !since.IsZero() && e.TS.Before(since) {
			continue
		}
		if Classify(e, policy) == KindNotAllowed {
			out[e.TaskID]++
		}
	}
	return out
}
