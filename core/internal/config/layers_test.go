package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write 建一个层文件，返回 Layer。
func write(t *testing.T, dir, name, body string, project bool) Layer {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Layer{Name: name, Path: p, Project: project}
}

func TestMergeFourLayers(t *testing.T) {
	d := t.TempDir()
	global := write(t, d, "global.toml", `
max_running = 4
[network]
allow = ["global.example.com"]
[resources]
memory = "3g"
`, false)
	project := write(t, d, "project.toml", `
profile = "web-go"
[network]
mode = "allowlist"
allow = ["api.internal", "global.example.com"]
[resources]
memory = "4g"
[deps]
mask = [".next"]
`, true)
	ws := write(t, d, "ws.toml", "[resources]\nmemory = \"6g\"\n", false)

	ld, err := LoadLayers([]Layer{global, project, ws})
	if err != nil {
		t.Fatal(err)
	}
	// 标量：后者覆盖前者。
	if ld.Config.Resources.Memory != "6g" {
		t.Fatalf("memory = %q", ld.Config.Resources.Memory)
	}
	if ld.Config.MaxRunning != 4 || ld.Config.Network.Mode != "allowlist" {
		t.Fatalf("%+v", ld.Config)
	}
	// 没人覆盖的保持默认值。
	if ld.Config.Resources.CPUs != 2 {
		t.Fatalf("cpus = %v", ld.Config.Resources.CPUs)
	}
	// 列表：并集，顺序按层，重复项只留一个。
	want := []string{"global.example.com", "api.internal"}
	if strings.Join(ld.Config.Network.Allow, ",") != strings.Join(want, ",") {
		t.Fatalf("allow = %v", ld.Config.Network.Allow)
	}
	// 默认值里的 node_modules 不会被项目层挤掉。
	if strings.Join(ld.Config.Deps.Mask, ",") != "node_modules,.next" {
		t.Fatalf("mask = %v", ld.Config.Deps.Mask)
	}
	// 来源：标量记最后一层，列表记所有贡献者。
	for key, want := range map[string]string{
		"resources.memory": "ws.toml",
		"max_running":      "global.toml",
		"resources.cpus":   LayerDefault,
		"network.allow":    "global.toml + project.toml",
	} {
		if got := ld.Sources[key].String(); got != want {
			t.Errorf("%s 来自 %q，期望 %q", key, got, want)
		}
	}
}

func TestMissingLayerIsSkipped(t *testing.T) {
	d := t.TempDir()
	ld, err := LoadLayers([]Layer{{Name: "x", Path: filepath.Join(d, "nope.toml")}})
	if err != nil || ld.Config.MaxRunning != 3 {
		t.Fatal(err, ld.Config)
	}
	if ld.Layers[0].Found {
		t.Fatal("不存在的文件不该标成 Found")
	}
}

func TestInvalidValueNamesItsLayer(t *testing.T) {
	d := t.TempDir()
	l := write(t, d, "bad.toml", "[network]\nmode = \"wide\"\n", false)
	_, err := LoadLayers([]Layer{l})
	if err == nil || !strings.Contains(err.Error(), "bad.toml") {
		t.Fatalf("报错里应当指出是哪一层：%v", err)
	}
}

// 校验只跑在合并结果上：被后面的层盖掉的非法值不该报错。
func TestOverriddenInvalidValueIsFine(t *testing.T) {
	d := t.TempDir()
	bad := write(t, d, "a.toml", "[resources]\nmemory = \"lots\"\n", false)
	good := write(t, d, "b.toml", "[resources]\nmemory = \"4g\"\n", false)
	ld, err := LoadLayers([]Layer{bad, good})
	if err != nil || ld.Config.Resources.Memory != "4g" {
		t.Fatal(err, ld.Config.Resources.Memory)
	}
}

func TestProjectLayerRejects(t *testing.T) {
	cases := map[string]string{
		"密钥字段":      "[agents.claude]\napi_key_file = \"x\"\n",
		"token 字段":  "my_token = \"abc\"\n",
		"绝对路径":      "[deps]\nmask = [\"/abs\"]\n",
		"home 路径":   "profile = \"~/x\"\n",
		"sbx 不认识的键": "secret_thing = 1\n",
	}
	d := t.TempDir()
	for name, body := range cases {
		l := write(t, d, "p-"+name+".toml", body, true)
		if _, err := LoadLayers([]Layer{l}); err == nil {
			t.Errorf("%s 应当被项目层校验拦住", name)
		}
	}
	// 正常的项目层配置不该被误伤。
	ok := write(t, d, "ok.toml", "profile = \"web-go\"\n[network]\nallow = [\"api.internal\"]\n", true)
	if _, err := LoadLayers([]Layer{ok}); err != nil {
		t.Fatal(err)
	}
}

func TestPaths(t *testing.T) {
	if got := Paths("/h", "", ""); len(got) != 1 || got[0].Name != LayerGlobal {
		t.Fatalf("仓库外只有全局层：%+v", got)
	}
	got := Paths("/h", "/r", "r-abc123")
	if len(got) != 3 || got[1].Path != "/r/.sbx/sandbox.toml" || !got[1].Project ||
		got[2].Path != "/h/workspaces/r-abc123.toml" || got[2].Project {
		t.Fatalf("%+v", got)
	}
}

// api_key_env 跟 version 一样按层合并；而项目层根本不许出现它（M2-2）。
func TestAPIKeyLayers(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.toml")
	ws := filepath.Join(dir, "ws.toml")
	os.WriteFile(global, []byte("[agents.claude]\napi_key_env = \"GLOBAL_KEY\"\nversion = \"1.2.3\"\n"), 0o644)
	os.WriteFile(ws, []byte("[agents.claude]\napi_key_env = \"WS_KEY\"\n"), 0o644)

	ld, err := LoadLayers([]Layer{
		{Name: LayerGlobal, Path: global},
		{Name: LayerWorkspace, Path: ws},
	})
	if err != nil {
		t.Fatal(err)
	}
	ag := ld.Config.Agents["claude"]
	if ag.APIKeyEnv != "WS_KEY" {
		t.Errorf("工作区层应该覆盖全局层：%q", ag.APIKeyEnv)
	}
	// 只在全局层写过的 version 不该被工作区层清空
	if ag.Version != "1.2.3" {
		t.Errorf("version 被覆盖没写的层清掉了：%q", ag.Version)
	}
	if got := ld.Sources["agents.claude.api_key_env"].Last(); got != LayerWorkspace {
		t.Errorf("来源标错了：%q", got)
	}
}

func TestProjectLayerRejectsAPIKeyFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sandbox.toml")
	os.WriteFile(p, []byte("[agents.claude]\napi_key_env = \"ANTHROPIC_API_KEY\"\n"), 0o644)
	_, err := LoadLayers([]Layer{{Name: LayerProject, Path: p, Project: true}})
	if err == nil {
		t.Fatal("项目层写 api_key_env 应该直接报错")
	}
}

// M2-15：资源上限在每一层都能覆盖，三个字段都算。
// 项目层也能写——它们不是红线字段，而且 .sbx/ 的改动要过信任确认（design §9.3）。
func TestResourceLayers(t *testing.T) {
	d := t.TempDir()
	global := write(t, d, "g.toml", "[resources]\ncpus = 4\nmemory = \"5g\"\npids = 2048\n", false)
	project := write(t, d, "p.toml", "[resources]\nmemory = \"6g\"\n", true)
	ws := write(t, d, "w.toml", "[resources]\ncpus = 8\n", false)

	ld, err := LoadLayers([]Layer{global, project, ws})
	if err != nil {
		t.Fatal(err)
	}
	r := ld.Config.Resources
	if r.CPUs != 8 || r.Memory != "6g" || r.Pids != 2048 {
		t.Fatalf("%+v", r)
	}
	for key, want := range map[string]string{
		"resources.cpus":   "w.toml",
		"resources.memory": "p.toml",
		"resources.pids":   "g.toml",
	} {
		if got := ld.Sources[key].String(); got != want {
			t.Errorf("%s 来自 %q，期望 %q", key, got, want)
		}
	}
	// 非法值照样要报错，并指出是哪一层写的
	bad := write(t, d, "bad.toml", "[resources]\nmemory = \"3 gigs\"\n", false)
	if _, err := LoadLayers([]Layer{global, bad}); err == nil || !strings.Contains(err.Error(), "bad.toml") {
		t.Fatalf("非法的 memory 要报错并指出来源，得到：%v", err)
	}
}

// M2-7 / M2-8：代理模式和上游地址也是分层的。
func TestProxyAndUpstreamLayers(t *testing.T) {
	d := t.TempDir()
	global := write(t, d, "g2.toml", "[network]\nupstream = \"http://host.docker.internal:7890\"\n", false)
	ws := write(t, d, "w2.toml", "[network]\nproxy = \"dedicated\"\n", false)
	ld, err := LoadLayers([]Layer{global, ws})
	if err != nil {
		t.Fatal(err)
	}
	if ld.Config.Network.Proxy != "dedicated" {
		t.Fatalf("proxy = %q", ld.Config.Network.Proxy)
	}
	host, port, err := ld.Config.Upstream()
	if err != nil || host != "host.docker.internal" || port != "7890" {
		t.Fatalf("upstream = %s:%s %v", host, port, err)
	}
	bad := write(t, d, "bad2.toml", "[network]\nproxy = \"sidecar\"\n", false)
	if _, err := LoadLayers([]Layer{global, bad}); err == nil || !strings.Contains(err.Error(), "bad2.toml") {
		t.Fatalf("非法的 proxy 要报错并指出来源，得到：%v", err)
	}
}
