package proxy

import (
	"reflect"
	"testing"
)

func TestHostsIn(t *testing.T) {
	cases := []struct {
		name, cmd      string
		hosts, skipped []string
	}{
		{"飞书 webhook", `curl -s -X POST https://open.feishu.cn/open-apis/bot/v2/hook/xxx -d '{"a":1}'`,
			[]string{"open.feishu.cn"}, nil},
		{"一条命令里两个 URL", `curl http://a.example.com/x | curl -d @- https://b.example.com:8443/y`,
			[]string{"a.example.com", "b.example.com"}, nil},
		{"带用户名密码", `curl https://user:pw@hooks.example.com/p`, []string{"hooks.example.com"}, nil},
		{"主机名是变量", `curl "$WEBHOOK_HOST/p"; curl https://$HOST/p`, nil, []string{"$host"}},
		{"裸 IP 放进去不起作用", `curl http://10.0.0.5:9000/notify`, nil, []string{"10.0.0.5"}},
		{"没有 URL", `echo done >> /sbx/state/probe.log`, nil, nil},
		{"大小写和末尾点", `curl HTTPS://Open.Feishu.CN./p`, []string{"open.feishu.cn"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hosts, skipped := HostsIn(c.cmd)
			if !reflect.DeepEqual(hosts, c.hosts) {
				t.Errorf("hosts = %v，期望 %v", hosts, c.hosts)
			}
			if !reflect.DeepEqual(skipped, c.skipped) {
				t.Errorf("skipped = %v，期望 %v", skipped, c.skipped)
			}
		})
	}
	// 多段命令一起扫，去重
	if h, _ := HostsIn("curl https://a.io/x", "curl https://a.io/y", ""); !reflect.DeepEqual(h, []string{"a.io"}) {
		t.Fatalf("%v", h)
	}
}

// 通知域名不能越过策略拦截层：RenderAllow 会把它减掉。
func TestNotifyHostCannotBypassPolicyBlock(t *testing.T) {
	notify, _ := HostsIn("curl https://mcp-proxy.anthropic.com/x")
	got := RenderAllow(BuiltinList("policy-block"), []string{"github.com"}, notify)
	if contains(got, "mcp-proxy.anthropic.com") {
		t.Fatalf("策略拦截的域名被通知命令放行了：%v", got)
	}
}
