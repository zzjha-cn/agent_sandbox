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

# 注入了 API key 时，claude 会弹"Detected a custom API key … Do you want to use this API key?"
# 并默认停在 No 上，无人值守就卡死在这里（M2-13 实测）。它记在 ~/.claude.json 的
# customApiKeyResponses.approved 里，值是 key 的后 20 个字符。
if [ -n "${SBX_API_KEY_APPROVE:-}" ]; then
  edit "$HOME/.claude.json" --arg k "$SBX_API_KEY_APPROVE" \
    '.customApiKeyResponses.approved = ((.customApiKeyResponses.approved // []) + [$k] | unique)
     | .customApiKeyResponses.rejected = ((.customApiKeyResponses.rejected // []) - [$k])'
fi

link() { # link <name> <target>
  dst="$HOME/.claude/$1"
  if [ -L "$dst" ] || [ ! -e "$dst" ]; then
    if [ -e "$2" ]; then ln -sfn "$2" "$dst"; else rm -f "$dst"; fi
  else
    echo "sbx: ~/.claude/$1 already exists and is not a symlink created by sbx; skipping" >&2
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

// APIKeyApproval 返回写进 customApiKeyResponses.approved 的值：key 的后 20 个字符
// （claude 自己就是这么记的，弹窗里显示的也是这一段）。key 太短时原样返回。
func APIKeyApproval(key string) string {
	if key == "" {
		return ""
	}
	r := []rune(key)
	if len(r) <= 20 {
		return key
	}
	return string(r[len(r)-20:])
}
