#!/usr/bin/env bash
# M0-1 验证：用全新容器读取 volume 里的凭据，非交互地调用一次
# 再通过 M0-4 的 shared squid（带认证代理）跑一次，补完 claude 的真实凭据验证
set -u
cd "$(dirname "$0")"
PX=http://host.docker.internal:7890
echo "== 新容器 + 宿主机代理 =="
docker run --rm -v m01-home:/root -e HTTPS_PROXY=$PX -e HTTP_PROXY=$PX sbx-m04-tools bash -lc '
  echo "[claude auth status]"; claude auth status 2>&1 | head -5
  echo "[claude -p]";          timeout 120 claude -p "只回复 OK 两个字母" </dev/null 2>&1 | tail -2
  echo "[codex login status]"; codex login status 2>&1 | head -3
  echo "[codex exec]";         timeout 180 codex exec --skip-git-repo-check "只回复 OK 两个字母" </dev/null 2>&1 | tail -2'
if docker ps --format '{{.Names}}' | grep -q '^m04-proxy$'; then
  source ../m0-4/.tokens; PA="http://task-a:${TA}@proxy:3128"
  echo; echo "== internal 网络 + shared squid（带认证代理） =="
  docker run --rm --network m04-a -v m01-home:/root -e HTTPS_PROXY="$PA" -e HTTP_PROXY="$PA" -e CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 sbx-m04-tools bash -lc '
    echo "[claude -p]"; timeout 120 claude -p "只回复 OK 两个字母" </dev/null 2>&1 | tail -2
    echo "[codex exec]"; timeout 180 codex exec --skip-git-repo-check "只回复 OK 两个字母" </dev/null 2>&1 | tail -2'
  echo "--- 本轮 squid 日志（去重） ---"
  docker exec m04-proxy tail -60 /var/log/squid/access.log | awk '{print $2, $4, $6}' | sort | uniq -c | sort -rn | head -15
fi
