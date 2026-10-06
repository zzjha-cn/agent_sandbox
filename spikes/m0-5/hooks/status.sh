#!/usr/bin/env bash
# 由 Claude hooks 调用：$1 = running|idle；stdin 是 hook 的 JSON 输入
# 先写临时文件再 rename，保证 sbx 读到的永远是完整 JSON
set -u
state="$1"; dir="${SBX_STATE_DIR:-/sbx/state}"; mkdir -p "$dir"
ev=$(jq -r '.hook_event_name // "?"' 2>/dev/null || echo "?")
printf '{"state":"%s","event":"%s","ts":%s}\n' "$state" "$ev" "$(date +%s)" > "$dir/.status.tmp"
mv -f "$dir/.status.tmp" "$dir/status.json"
echo "$(date +%T) $state $ev" >> "$dir/events.log"
