#!/usr/bin/env bash
# Claude hooks 调用：把 Agent 状态写到 /sbx/state/status.json（M0-5）
set -u
state="$1"; dir="${SBX_STATE_DIR:-/sbx/state}"; mkdir -p "$dir"
ev=$(jq -r '.hook_event_name // "?"' 2>/dev/null || echo "?")
printf '{"state":"%s","event":"%s","ts":%s}\n' "$state" "$ev" "$(date +%s)" > "$dir/.status.tmp"
mv -f "$dir/.status.tmp" "$dir/status.json"
echo "$(date +'%F %T %z') $state $ev" >> "$dir/events.log"
