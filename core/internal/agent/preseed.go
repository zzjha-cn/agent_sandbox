package agent

import (
	"fmt"
	"strings"
)

// PreseedScript 是在容器内以 agent 身份执行的脚本（design §7.2，M0-5）：
//   - 预置 Claude 首次启动状态，避免交互模式卡在引导、登录方式和 bypass 警告对话框上；
//   - 在 ~/.claude 下建立指向宿主机配置（只读挂载）的软链接。
//
// 幂等；JSON 用 jq 改写后 tmp + rename。依赖环境变量 SBX_WORKTREE。
// HOME 可由调用方覆盖（单元测试用）。
const PreseedScript = `set -eu
cd "$HOME"
mkdir -p "$HOME/.claude"

edit() { # edit <file> <jq filter>
  f="$1"; shift
  [ -s "$f" ] || echo '{}' > "$f"
  jq --arg wt "$SBX_WORKTREE" "$@" "$f" > "$f.sbx-tmp"
  mv -f "$f.sbx-tmp" "$f"
}
edit "$HOME/.claude.json" '.hasCompletedOnboarding = true | .projects[$wt].hasTrustDialogAccepted = true'
edit "$HOME/.claude/settings.json" '.theme //= "dark" | .skipDangerousModePermissionPrompt = true'

link() { # link <name> <target>
  dst="$HOME/.claude/$1"
  if [ -L "$dst" ] || [ ! -e "$dst" ]; then
    if [ -e "$2" ]; then ln -sfn "$2" "$dst"; else rm -f "$dst"; fi
  else
    echo "sbx: ~/.claude/$1 已存在且不是 sbx 建立的软链接，跳过" >&2
  fi
}
link CLAUDE.md /sbx/gen/host-claude/CLAUDE.md
`

// linkLines 为每个宿主机目录追加 link 调用；没有挂载的目录也要处理，以清理失效的软链接。
func linkLines() string {
	var b strings.Builder
	for _, d := range hostDirs {
		fmt.Fprintf(&b, "link %s /sbx/host-claude/%s\n", d, d)
	}
	return b.String()
}

// Preseed 返回完整脚本。
func Preseed() string { return PreseedScript + linkLines() }
