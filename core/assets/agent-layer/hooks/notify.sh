#!/usr/bin/env bash
# Claude hooks 调用：执行个人配置里的 on_idle / on_exit（M3-10、design §7.2）。
#
# 这个脚本永远立刻返回 0：通知再重要，也不能把 claude 卡住，也不能让 hook 报错
# 污染 transcript。失败只记进 notify.log。
#   notify.sh <idle|exit> [--sync]
# --sync 是 headless 收尾用的：那之后容器就要停了，后台进程会被一起杀掉。
set -u
kind="${1:-}"; sync="${2:-}"
dir="${SBX_STATE_DIR:-/sbx/state}"
cmd="/sbx/gen/hooks/on_${kind}.sh"
[ -f "$cmd" ] || exit 0

# 节流：Notification 是"等人处理"时反复触发的事件，不拦一下会刷屏。
# 窗口由 sbx 渲染进 gen 目录，这样改配置不用重建容器（环境变量只能建容器时注入）。
win="$(cat /sbx/gen/notify.throttle 2>/dev/null || echo 600)"
win="${SBX_NOTIFY_THROTTLE:-$win}"
mark="$dir/.notify.$kind"
now=$(date +%s)
last=$(cat "$mark" 2>/dev/null || echo 0)
if [ "$win" -gt 0 ] && [ $((now - last)) -lt "$win" ]; then exit 0; fi
echo "$now" > "$mark"

export SBX_EVENT="$kind" SBX_TS="$now"
SBX_STATE=$(jq -r '.state // "?"' "$dir/status.json" 2>/dev/null || echo "?")
export SBX_STATE
{
  printf '\n===== %s %s =====\n' "$(date '+%F %T %z')" "$kind"
} >> "$dir/notify.log"

# 子 shell 里跑，超时杀掉，退出码记进日志
inner="timeout ${SBX_NOTIFY_TIMEOUT:-20} bash $cmd >> $dir/notify.log 2>&1 || \
       printf '[sbx] 通知命令失败（exit=%s）\n' \$? >> $dir/notify.log"

if [ "$sync" = "--sync" ]; then
  bash -c "$inner"
else
  # setsid：claude 等不到它，回合结束时也不会把它一起收掉
  setsid bash -c "$inner" >/dev/null 2>&1 &
fi
exit 0
