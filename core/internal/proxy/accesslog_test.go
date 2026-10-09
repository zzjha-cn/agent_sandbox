package proxy

import (
	"strings"
	"testing"
	"time"
)

const sampleLog = `1791287835.493 wande.wd-ts 172.21.0.3 TCP_DENIED_ABORTED/403 CONNECT mcp-proxy.anthropic.com:443 HIER_NONE/-
1791287835.494 wande.wd-ts 172.21.0.3 TCP_DENIED/403 CONNECT mcp-proxy.anthropic.com:443 HIER_NONE/-
1791288434.935 wande.wd-ts 172.21.0.3 TCP_TUNNEL/200 CONNECT api.anthropic.com:443 FIRSTUP_PARENT/192.168.65.254
1791288435.000 wande.wd-ts 172.21.0.3 TCP_DENIED/403 CONNECT registry.npmjs.example:443 HIER_NONE/-
1791288436.000 wande.other 172.21.0.4 TCP_DENIED/403 CONNECT registry.npmjs.example:443 HIER_NONE/-
1791288437.000 - 172.21.0.3 TCP_DENIED/407 CONNECT api.anthropic.com:443 HIER_NONE/-
1791288438.000 wande.wd-ts 172.21.0.3 TCP_DENIED/403 GET http://app.datadoghq.com/v1/input HIER_NONE/-
rubbish line
`

func TestParseAccessLog(t *testing.T) {
	es := ParseAccessLog(strings.NewReader(sampleLog))
	if len(es) != 7 {
		t.Fatalf("got %d entries", len(es))
	}
	if es[0].Host != "mcp-proxy.anthropic.com" || es[0].Status != 403 || es[0].TaskID != "wande.wd-ts" {
		t.Fatalf("%+v", es[0])
	}
	if !es[0].Denied() || es[2].Denied() {
		t.Fatal("Denied 判断不对")
	}
	if es[6].Host != "app.datadoghq.com" {
		t.Fatalf("GET 的 URL 没解析出主机名：%+v", es[6])
	}
}

func TestSummarizeDenied(t *testing.T) {
	es := ParseAccessLog(strings.NewReader(sampleLog))
	rows := SummarizeDenied(es, "", time.Time{}, PolicyList())

	byHost := map[string]DeniedHost{}
	for _, r := range rows {
		byHost[r.Host] = r
	}
	if got := byHost["mcp-proxy.anthropic.com"]; got.Count != 2 || got.Kind != KindPolicy {
		t.Fatalf("云端 MCP 应该归到策略拦截：%+v", got)
	}
	if got := byHost["app.datadoghq.com"]; got.Kind != KindPolicy {
		t.Fatalf("遥测应该归到策略拦截：%+v", got)
	}
	if got := byHost["registry.npmjs.example"]; got.Count != 2 || got.Kind != KindNotAllowed || len(got.Tasks) != 2 {
		t.Fatalf("%+v", got)
	}
	// 407 和 403 命中同一个域名时要分开统计
	var auth int
	for _, r := range rows {
		if r.Kind == KindAuth {
			auth += r.Count
		}
	}
	if auth != 1 {
		t.Fatalf("认证失败 %d 次", auth)
	}
	// 次数相同的按域名排序，次数多的在前
	if rows[0].Count < rows[len(rows)-1].Count {
		t.Fatal("没有按次数降序")
	}
}

func TestSummarizeDeniedFilters(t *testing.T) {
	es := ParseAccessLog(strings.NewReader(sampleLog))
	rows := SummarizeDenied(es, "wande.other", time.Time{}, PolicyList())
	if len(rows) != 1 || rows[0].Host != "registry.npmjs.example" {
		t.Fatalf("按 Task 过滤失败：%+v", rows)
	}
	future := time.Unix(1791288437, 0)
	rows = SummarizeDenied(es, "", future, PolicyList())
	for _, r := range rows {
		if r.Last.Before(future) {
			t.Fatalf("since 过滤失败：%+v", r)
		}
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		host, pattern string
		want          bool
	}{
		{"app.datadoghq.com", ".datadoghq.com", true},
		{"datadoghq.com", ".datadoghq.com", true},
		{"notdatadoghq.com", ".datadoghq.com", false},
		{"mcp-proxy.anthropic.com", "mcp-proxy.anthropic.com", true},
		{"x.mcp-proxy.anthropic.com", "mcp-proxy.anthropic.com", false},
	}
	for _, c := range cases {
		if got := matches(c.host, c.pattern); got != c.want {
			t.Errorf("matches(%q,%q)=%v", c.host, c.pattern, got)
		}
	}
}

func TestDeniedCounts(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) time.Time { return now.Add(d) }
	entries := []Entry{
		{TS: at(-time.Hour), TaskID: "ws.a", Status: 403, Host: "old.example.com"}, // 窗口之前
		{TS: at(-time.Minute), TaskID: "ws.a", Status: 403, Host: "a.example.com"},
		{TS: at(-time.Minute), TaskID: "ws.a", Status: 403, Host: "b.example.com"},
		{TS: at(-time.Minute), TaskID: "ws.b", Status: 403, Host: "a.example.com"},
		{TS: at(-time.Minute), TaskID: "ws.a", Status: 403, Host: "mcp-proxy.anthropic.com"}, // 策略拦截不算
		{TS: at(-time.Minute), TaskID: "ws.a", Status: 407, Host: "a.example.com"},           // 认证失败不算
		{TS: at(-time.Minute), TaskID: "ws.a", Status: 200, Host: "ok.example.com"},          // 没被拒
		{TS: at(-time.Minute), TaskID: "-", Status: 403, Host: "a.example.com"},              // 认不出归属
	}
	got := DeniedCounts(entries, at(-10*time.Minute), PolicyList())
	if got["ws.a"] != 2 || got["ws.b"] != 1 || len(got) != 2 {
		t.Fatalf("%v", got)
	}
	// 不给窗口时连旧的一起数
	if all := DeniedCounts(entries, time.Time{}, PolicyList()); all["ws.a"] != 3 {
		t.Fatalf("%v", all)
	}
}
