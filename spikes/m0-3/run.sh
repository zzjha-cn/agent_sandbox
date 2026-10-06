#!/usr/bin/env bash
# M0-3: squid（dedicated 模式）串联宿主机代理 + internal 网络验证
set -u
cd "$(dirname "$0")"
docker rm -f m03-proxy >/dev/null 2>&1; docker network rm m03-int m03-egress >/dev/null 2>&1
docker network create --internal m03-int >/dev/null
docker network create m03-egress >/dev/null
docker run -d --name m03-proxy --network m03-egress \
  -v "$PWD/squid.conf:/etc/squid/squid.conf:ro" -v "$PWD/allowlist.txt:/etc/squid/allowlist.txt:ro" \
  ubuntu/squid:latest >/dev/null
docker network connect --alias proxy m03-int m03-proxy
sleep 4
c() { docker run --rm --network m03-int curlimages/curl -s -o /dev/null -w "%{http_code}" --max-time 15 "$@"; }
echo "allow  https://api.anthropic.com  -> $(c -x http://proxy:3128 https://api.anthropic.com/)"
echo "allow  https://github.com         -> $(c -x http://proxy:3128 https://github.com/)"
echo "deny   https://example.com        -> $(c -x http://proxy:3128 https://example.com/)"
echo "direct https://api.anthropic.com  -> $(c https://api.anthropic.com/) (无代理直连，期望 000)"
echo "direct 1.1.1.1:443                -> $(c https://1.1.1.1/) (直连 IP，期望 000)"
echo "--- access.log ---"
docker exec m03-proxy cat /var/log/squid/access.log
