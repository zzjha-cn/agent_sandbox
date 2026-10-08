package cli

import (
	"path/filepath"
	"testing"

	"sandx/internal/config"
	"sandx/internal/proxy"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

func egressApp(t *testing.T, mode string) (*App, task.Task) {
	t.Helper()
	repo := t.TempDir()
	cfg := config.Default()
	cfg.Network.Proxy = mode
	a := &App{Home: t.TempDir(), Cfg: cfg, WS: workspace.Workspace{Root: repo, ID: "demo-abc123"}}
	tk, err := task.New(a.Home, a.WS, "t1")
	if err != nil {
		t.Fatal(err)
	}
	return a, tk
}

func TestEgressPicksByConfig(t *testing.T) {
	a, tk := egressApp(t, "dedicated")
	if _, ok := a.egress(tk).(proxy.Dedicated); !ok {
		t.Fatal("network.proxy = dedicated 应该拿到独占实例")
	}
	a, tk = egressApp(t, "shared")
	if _, ok := a.egress(tk).(proxy.Shared); !ok {
		t.Fatal("默认应该拿到共享实例")
	}
}

// 已经建过的 Task 以 meta 为准：容器和网络是按那个模式建的，
// 改了配置也不能把运行中的 Task 换到另一种代理上。
func TestEgressFollowsMetaForExistingTask(t *testing.T) {
	a, tk := egressApp(t, "shared")
	if err := tk.WriteMeta(task.Meta{Task: tk.Name, WS: a.WS.ID, Proxy: "dedicated", TaskID: tk.ID()}); err != nil {
		t.Fatal(err)
	}
	if got := a.egressMode(tk); got != "dedicated" {
		t.Fatalf("meta 里是 dedicated，配置是 shared，应该听 meta 的，得到 %q", got)
	}
	if _, ok := a.egress(tk).(proxy.Dedicated); !ok {
		t.Fatal("egress 没有跟着 meta 走")
	}
	// 新建 Task 时反过来：这时 meta 要么没有，要么是上一轮留下的，不该左右选择
	if _, ok := a.egressOf(tk, a.Cfg.Network.Proxy).(proxy.Shared); !ok {
		t.Fatal("新建 Task 应该用生效配置里的模式")
	}
}

func TestDedicatedNaming(t *testing.T) {
	a, tk := egressApp(t, "dedicated")
	a.Cfg.Network.Upstream = "http://host.docker.internal:7890"
	d := a.dedicated(tk)
	if d.Name != "sbx-demo-abc123-t1-proxy" {
		t.Errorf("sidecar 容器名：%s", d.Name)
	}
	if want := filepath.Join(a.Home, "state", a.WS.ID, "t1", "proxy"); d.Dir != want {
		t.Errorf("配置目录：%s，想要 %s", d.Dir, want)
	}
	if d.Agent != "sbx-demo-abc123-t1" || d.TaskID != "demo-abc123.t1" {
		t.Errorf("agent=%s taskID=%s", d.Agent, d.TaskID)
	}
	// 上游代理对两种模式都生效（M2-8）
	if d.UpstreamHost != "host.docker.internal" || d.UpstreamPort != "7890" {
		t.Errorf("上游没有传给独占实例：%s:%s", d.UpstreamHost, d.UpstreamPort)
	}
	if name := a.proxyName(tk); name != d.Name {
		t.Errorf("提示里的代理名：%s", name)
	}
}
