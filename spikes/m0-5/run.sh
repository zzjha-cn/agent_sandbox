#!/usr/bin/env bash
# M0-5: Claude 状态 hooks（headless + tmux 交互）+ 方案 A（拦截云端 MCP）下 claude 是否正常
set -u
cd "$(dirname "$0")"; source ../m0-4/.tokens
PA="http://task-a:${TA}@proxy:3128"
mkdir -p state work && rm -f state/*
COMMON=(--network m04-a -u 1000:1000 -e HOME=/home/node -v m05-home:/home/node
  -v "$PWD/hooks:/sbx/hooks:ro" -v "$PWD/state:/sbx/state" -v "$PWD/work:/work" -w /work
  -e HTTPS_PROXY="$PA" -e HTTP_PROXY="$PA" -e CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1)

echo "== 1. headless：claude -p（会调用一次工具） =="
docker run --rm "${COMMON[@]}" sbx-m05 bash -c \
  'timeout 180 claude -p "用 Bash 执行 ls / ，然后只回复 DONE" --dangerously-skip-permissions --settings /sbx/hooks/settings.json </dev/null 2>&1 | tail -3'
echo "-- events --"; cat state/events.log; echo "-- status --"; cat state/status.json; rm -f state/*

echo; echo "== 2. 交互：tmux 内 claude TUI =="
docker rm -f m05-agent >/dev/null 2>&1
docker run -d --name m05-agent "${COMMON[@]}" sbx-m05 sleep infinity >/dev/null
docker exec m05-agent tmux new-session -d -s agent -x 200 -y 50 \
  'claude --dangerously-skip-permissions --settings /sbx/hooks/settings.json'
sleep 12; echo "-- 启动后画面 --"; docker exec m05-agent tmux capture-pane -p -t agent | grep -v '^\s*$' | head -25
echo "-- status --"; cat state/status.json 2>/dev/null || echo "(无)"
