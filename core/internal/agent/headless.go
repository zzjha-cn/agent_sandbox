package agent

import (
	"fmt"
	"strings"
)

// 容器内的固定路径。state 目录是 rw 挂载，gen 目录是只读挂载。
const (
	RunLog     = "/sbx/state/run.log"
	RunExit    = "/sbx/state/run.exit"
	PromptFile = "/sbx/gen/prompt.txt"
	NotifySh   = "/sbx/gen/hooks/notify.sh"
)

// HeadlessOpts 是一次 headless 运行的参数（M3-7）。
type HeadlessOpts struct {
	Continue bool // 接上这个 Task 的上次对话
	Notify   bool // 配了 on_exit：收尾时同步调一次通知
}

// HeadlessCmd 组装 tmux 窗口里跑的 headless 命令。
//
// 几个不显然的地方：
//   - prompt 从文件读，不进命令行：prompt 里必然有引号和换行，拼进 tmux 的命令字符串
//     是引号地狱，而且会出现在 ps 里。
//   - 用 tee 而不是重定向：输出既要落 run.log，又要在 tmux 里看得见（sbx attach 能围观）。
//     退出码因此要从 PIPESTATUS 取。
//   - 跑完 kill 1 停掉容器（design §3.1 的状态机）：整夜批量派发时，跑完一个就释放
//     一份内存和一个 max_running 名额。容器主进程是 sleep infinity，不会自己退。
//   - on_exit 必须在 kill 1 之前**同步**跑完，否则通知进程会被连带杀掉。
func HeadlessCmd(o HeadlessOpts) string {
	claude := "claude -p \"$(cat " + PromptFile + ")\" --dangerously-skip-permissions --settings /sbx/gen/settings.sbx.json"
	if o.Continue {
		claude += " --continue"
	}
	var b strings.Builder
	b.WriteString("set -o pipefail\n")
	fmt.Fprintf(&b, "printf '\\n===== sbx run -p @ %%s =====\\n' \"$(date '+%%F %%T %%z')\" >> %s\n", RunLog)
	fmt.Fprintf(&b, "%s 2>&1 | tee -a %s\n", claude, RunLog)
	b.WriteString("code=${PIPESTATUS[0]}\n")
	fmt.Fprintf(&b, "printf '%%s' \"$code\" > %s\n", RunExit)
	fmt.Fprintf(&b, "printf '\\n[sbx] headless 结束（exit=%%s）\\n' \"$code\" | tee -a %s\n", RunLog)
	if o.Notify {
		fmt.Fprintf(&b, "SBX_EXIT_CODE=\"$code\" %s exit --sync\n", NotifySh)
	}
	// 容器主进程是 sleep infinity；杀掉它容器就退出，tmux 会话也随之结束
	b.WriteString("kill 1\n")
	return b.String()
}
