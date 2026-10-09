package agent

import (
	"strings"
	"testing"
)

func TestHeadlessCmd(t *testing.T) {
	c := HeadlessCmd(HeadlessOpts{})
	for _, want := range []string{
		"set -o pipefail",                        // 没有它 tee 会把退出码吃掉
		`claude -p "$(cat /sbx/gen/prompt.txt)"`, // prompt 走文件，不进命令行
		"--dangerously-skip-permissions",
		"--settings /sbx/gen/settings.sbx.json",
		"tee -a /sbx/state/run.log",
		"code=${PIPESTATUS[0]}",
		"> /sbx/state/run.exit",
		"kill 1", // 跑完停容器（design §3.1）
	} {
		if !strings.Contains(c, want) {
			t.Errorf("缺少 %q：\n%s", want, c)
		}
	}
	if strings.Contains(c, "--continue") {
		t.Error("没要求 --continue 却加上了")
	}
	if strings.Contains(c, NotifySh) {
		t.Error("没配 on_exit 却调了通知")
	}
	if cont := HeadlessCmd(HeadlessOpts{Continue: true}); !strings.Contains(cont, "--continue") {
		t.Error("Continue 没生效")
	}
}

// on_exit 必须在 kill 1 之前同步跑完：容器一停，后台的通知进程会被一起杀掉。
func TestHeadlessNotifyRunsBeforeKill(t *testing.T) {
	c := HeadlessCmd(HeadlessOpts{Notify: true})
	n, k := strings.Index(c, NotifySh), strings.Index(c, "kill 1")
	if n < 0 || k < 0 || n > k {
		t.Fatalf("通知没有排在 kill 1 之前：\n%s", c)
	}
	if !strings.Contains(c, "--sync") {
		t.Errorf("收尾的通知必须是同步的：\n%s", c)
	}
	if !strings.Contains(c, `SBX_EXIT_CODE="$code"`) {
		t.Errorf("通知命令拿不到退出码：\n%s", c)
	}
}
