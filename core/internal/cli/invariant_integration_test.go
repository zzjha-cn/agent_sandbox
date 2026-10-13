//go:build docker

package cli

import (
	"strings"
	"testing"

	"sandx/internal/docker"
)

// TestIntegrationTaskHasNoDefaultRoute 是整个安全模型支点的回归测试（M4-17、
// boundary.md §3.3.4、§4）：**Task 容器始终没有默认路由**，通用出网只能经 squid。
//
// services 和 forward 都是往 Task 网络里加 peer 的机制，很容易在实现时顺手给 Task
// 多接一张网卡——那一刻不变量就没了，而且不会有任何报错。所以这里把两者都搭起来，
// 再验证 Task 仍然出不去：
//
//	[task] --db:9001--------> [service]        两个都在 internal 网络上
//	       --corp:9000------> [forwarder] ---> [上游]  forwarder 自己才接着外部网络
//	       --1.1.1.1:443--X   没有默认路由，到不了
func TestIntegrationTaskHasNoDefaultRoute(t *testing.T) {
	c := docker.New(testing.Verbose())
	const p = "sbx-it-inv"
	var (
		netName = p + "-net"
		egress  = p + "-egress"
		taskC   = p + "-task"
		svcC    = p + "-svc"
		fwdC    = p + "-fwd"
		upC     = p + "-up"
	)
	labels := map[string]string{"sbx.kind": "it", "sbx.it": "invariant"}
	clean := func() {
		for _, n := range []string{taskC, svcC, fwdC, upC} {
			c.Rm(n)
		}
		c.NetworkRm(netName)
		c.NetworkRm(egress)
	}
	clean()
	defer clean()

	// Task 网络是 internal 的，egress 不是——和真实拓扑一致
	if err := c.NetworkCreate(netName, true, labels); err != nil {
		t.Fatal(err)
	}
	if err := c.NetworkCreate(egress, false, labels); err != nil {
		t.Fatal(err)
	}

	serve := func(name, network, alias, port, reply string) {
		t.Helper()
		spec := docker.RunSpec{
			Name: name, Image: socatRef, Labels: labels, Network: network,
			Cmd: []string{"TCP-LISTEN:" + port + ",fork,reuseaddr", "SYSTEM:echo " + reply},
		}
		if alias != "" {
			spec.Aliases = []string{alias}
		}
		if _, err := c.RunContainer(spec); err != nil {
			t.Fatal(err)
		}
	}
	// 上游只接在 egress 上：Task 自己绝对够不着，只有 forwarder 能
	serve(upC, egress, "", "9000", "upstream-ok")
	upIP, err := c.ContainerIP(upC, egress)
	if err != nil {
		t.Fatal(err)
	}
	// 伴生服务：和 Task 同在 internal 网络（M4-11）
	serve(svcC, netName, "db", "9001", "service-ok")

	// forwarder：两边都接（M4-13）
	if _, err := c.RunContainer(docker.RunSpec{
		Name: fwdC, Image: socatRef, Labels: labels, Network: netName, Aliases: []string{"corp"},
		Cmd: []string{"TCP-LISTEN:9000,fork,reuseaddr", "TCP:" + upIP + ":9000"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.NetworkConnect(egress, fwdC, ""); err != nil {
		t.Fatal(err)
	}

	// Task 只接 internal 网络，一张网卡
	if _, err := c.RunContainer(docker.RunSpec{
		Name: taskC, Image: socatRef, Labels: labels, Network: netName,
		Entrypoint: "sleep", Cmd: []string{"300"},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. 没有默认路由。/proc/net/route 的第二列全 0 就是 default。
	out, err := c.Exec(taskC, docker.ExecOpts{}, "sh", "-c",
		`awk 'NR>1 && $2=="00000000" {print "DEFAULT", $1, $3}' /proc/net/route`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "DEFAULT") {
		t.Fatalf("the task container has a default route; the whole egress model rests on it not having one:\n%s", out)
	}

	// 2. 公网够不着。squid 之外没有第二条出去的路。
	if out, err := c.Exec(taskC, docker.ExecOpts{}, "sh", "-c",
		"socat -T3 /dev/null TCP:1.1.1.1:443 2>&1; echo rc=$?"); err != nil || !strings.Contains(out, "rc=") || strings.Contains(out, "rc=0") {
		t.Fatalf("the task reached a public address directly (out=%q err=%v)", out, err)
	}

	// 3. 声明过的那两个 peer 照常可达——不变量守住了，但功能没被守死
	for _, c2 := range []struct{ addr, want string }{
		{"db:9001", "service-ok"},
		{"corp:9000", "upstream-ok"},
	} {
		out, err := c.Exec(taskC, docker.ExecOpts{}, "sh", "-c", "socat -T5 - TCP:"+c2.addr+" </dev/null")
		if err != nil || !strings.Contains(out, c2.want) {
			t.Fatalf("%s should be reachable from the task: out=%q err=%v", c2.addr, out, err)
		}
	}
}
