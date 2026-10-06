#!/usr/bin/env bash
# M0-1: 在容器里完成订阅登录，凭据落在 volume m01-home（模拟 sbx-home）
# 用法：./login.sh claude | codex | claude-token
set -eu
PX=http://host.docker.internal:7890
run() { docker run -it --rm -v m01-home:/root -e HTTPS_PROXY=$PX -e HTTP_PROXY=$PX sbx-m04-tools "$@"; }
case "${1:-}" in
  claude)       run claude auth login ;;            # 方式 A：交互登录（URL + 粘贴授权码）
  claude-token) run claude setup-token ;;           # 方式 B：生成长期 token（打印出来，不落盘）
  codex)        run codex login --device-auth ;;    # 设备码登录，无需回调端口
  *) echo "用法：$0 claude | claude-token | codex"; exit 1 ;;
esac
