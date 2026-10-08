#!/bin/bash
# sbx 的 statusLine 脚本。渲染成 /sbx/gen/statusline.sh，由 settings.sbx.json 注入。
#
# 这是 sbx 自己的实现，版权随本仓库（Apache-2.0），没有引入外部代码。
# 输出一行，形如：
#
#   fix-login · Opus 5 · sbx/fix-login +3 ?1 · ████▄░░░░░ 42% of 200k
#
# 取舍（和通用的 statusLine 脚本不一样的地方）：
#   - 第一段是 Task 名（$SBX_TASK）而不是目录名：沙箱里 cwd 恒等于这个 Task 的
#     worktree，目录名没有信息量，Task 名才是你 attach 时要对上的东西。
#   - 不查 upstream / ahead / behind：`sbx/<task>` 分支是本地的，没有 upstream，
#     查了永远是 "no upstream"。要看领先多少用宿主机的 `sbx ls`。
#   - 不回显最后一条用户消息：这行会出现在 tmux 回滚和截图里，提示词不该跟着漏出去。
#
# 依赖：bash、jq、git —— Agent 层镜像里都有。jq 缺失时降级成最短的一行。

set -u

ACCENT=$'\033[38;5;80m'   # 青，和项目主色一致
DIM=$'\033[38;5;245m'
FAINT=$'\033[38;5;238m'
OFF=$'\033[0m'

input=$(cat)

command -v jq >/dev/null 2>&1 || { printf '%s\n' "${SBX_TASK:-sbx}"; exit 0; }

# 一次 jq 取齐所有字段。statusLine 每次重绘都会跑，别起五个 jq。
# 分隔符用 US(0x1f) 而不是 tab：tab 属于 IFS 空白，bash 的 read 会把连续两个
# 并成一个，中间字段为空（比如没有 transcript_path）时后面的字段会整体左移。
IFS=$'\037' read -r model cwd transcript max_ctx <<<"$(
  printf '%s' "$input" | jq -j '
    [ (.model.display_name // .model.id // "?")
    , (.cwd // "")
    , (.transcript_path // "")
    , (.context_window.context_window_size // .model.context_window // 200000 | tostring)
    ] | join("\u001f")' 2>/dev/null
)"
[ -n "${model:-}" ] || model="?"
[ -n "${max_ctx:-}" ] || max_ctx=200000

# ── 1. Task（沙箱外退回目录名）
head="${SBX_TASK:-}"
[ -n "$head" ] || head=$(basename "${cwd:-$PWD}" 2>/dev/null || echo "?")

# ── 2. 分支和工作区脏数
branch="" dirty=""
if [ -n "${cwd:-}" ] && [ -d "$cwd" ]; then
  branch=$(git -C "$cwd" branch --show-current 2>/dev/null || true)
  if [ -n "$branch" ]; then
    # --no-optional-locks：Agent 可能正在跑 git，别和它抢 index.lock
    changed=0 untracked=0
    while IFS= read -r line; do
      case "$line" in
        '??'*) untracked=$((untracked + 1)) ;;
        ?*)    changed=$((changed + 1)) ;;
      esac
    done <<<"$(git -C "$cwd" --no-optional-locks status --porcelain -uall 2>/dev/null)"
    [ "$changed" -gt 0 ] && dirty="+$changed"
    [ "$untracked" -gt 0 ] && dirty="${dirty:+$dirty }?$untracked"
  fi
fi

# ── 3. 上下文占用
# 用 transcript 最后一条带 usage 的记录，而不是 total_input_tokens —— 后者不含
# system prompt、工具定义和记忆，会明显低估。倒着读，命中即停，长会话也不用整份 slurp。
used=0
if [ -n "${transcript:-}" ] && [ -f "$transcript" ]; then
  rev=$(tac "$transcript" 2>/dev/null || tail -r "$transcript" 2>/dev/null || true)
  if [ -n "$rev" ]; then
    used=$(printf '%s\n' "$rev" | head -n 400 | jq -r '
      select(.isSidechain != true and .isApiErrorMessage != true)
      | .message.usage
      | select(. != null)
      | (.input_tokens // 0) + (.cache_read_input_tokens // 0) + (.cache_creation_input_tokens // 0)
    ' 2>/dev/null | head -n 1)
  fi
fi
case "${used:-}" in ''|*[!0-9]*) used=0 ;; esac

approx=""
if [ "$used" -eq 0 ]; then
  # 会话刚开始，transcript 里还没有 usage。system prompt + 工具定义 + 记忆
  # 大约已经占掉这么多，标上 ~ 表示是估的。
  used=20000
  approx="~"
fi
pct=$((used * 100 / max_ctx))
[ "$pct" -gt 100 ] && pct=100

bar=""
for i in 0 1 2 3 4 5 6 7 8 9; do
  fill=$((pct - i * 10))
  if   [ "$fill" -ge 7 ]; then bar="${bar}${ACCENT}█${OFF}"
  elif [ "$fill" -ge 3 ]; then bar="${bar}${ACCENT}▄${OFF}"
  else                         bar="${bar}${FAINT}░${OFF}"
  fi
done

k=$((max_ctx / 1000))
if [ "$k" -ge 1000 ]; then scale="$((k / 1000))M"; else scale="${k}k"; fi

# ── 4. 拼一行
sep="${DIM} · ${OFF}"
out="${ACCENT}${head}${OFF}${sep}${DIM}${model}${OFF}"
[ -n "$branch" ] && out="${out}${sep}${DIM}${branch}${OFF}${dirty:+ ${DIM}${dirty}${OFF}}"
out="${out}${sep}${bar} ${DIM}${approx}${pct}% of ${scale}${OFF}"
printf '%s\n' "$out"
