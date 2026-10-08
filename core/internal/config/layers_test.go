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
