#!/bin/sh
# 不带参数时常驻；Agent 由 sbx 通过 docker exec 在 tmux 会话里拉起。
# 带参数时直接执行（一次性命令，如 claude auth login）。
if [ "$#" -gt 0 ]; then exec "$@"; fi
exec sleep infinity
