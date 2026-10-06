#!/usr/bin/env bash
# M0-4: shared 模式（proxy_auth 识别 Task）+ 各工具对带认证代理 URL 的兼容性
set -u
cd "$(dirname "$0")"; source .tokens
docker rm -f m04-proxy >/dev/null 2>&1; docker network rm m04-a m04-b m04-egress >/dev/null 2>&1
docker network create m04-egress >/dev/null
docker network create --internal m04-a >/dev/null
docker network create --internal m04-b >/dev/null
docker run -d --name m04-proxy --network m04-egress \
  -v "$PWD/conf:/etc/sbx:ro" --entrypoint squid ubuntu/squid:latest -f /etc/sbx/squid.conf -NYC >/dev/null
docker network connect --alias proxy m04-a m04-proxy
docker network connect --alias proxy m04-b m04-proxy
sleep 4

PA="http://task-a:${TA}@proxy:3128"
run_a() { docker run --rm --network m04-a -e HTTP_PROXY="$PA" -e HTTPS_PROXY="$PA" -e http_proxy="$PA" -e https_proxy="$PA" \
            -e NO_PROXY=localhost,127.0.0.1 sbx-m04-tools bash -lc "$1" >/tmp/m04.out 2>&1; echo $?; }

echo "== 身份隔离 =="
cb() { docker run --rm --network "$1" curlimages/curl -s -o /dev/null -w "%{http_code}" --max-time 15 -x "$2" "$3"; }
echo "task-a 正确凭据 → github.com      : $(cb m04-a "$PA" https://github.com/)  (期望 200)"
echo "task-b 正确凭据 → github.com      : $(cb m04-b "http://task-b:${TB}@proxy:3128" https://github.com/)  (期望 000/403 拒绝)"
echo "task-b 正确凭据 → api.anthropic   : $(cb m04-b "http://task-b:${TB}@proxy:3128" https://api.anthropic.com/)  (期望 404 放行)"
echo "task-b 冒用 task-a 名+错 token    : $(cb m04-b "http://task-a:wrong@proxy:3128" https://github.com/)  (期望 000/407)"
echo "无凭据                            : $(cb m04-a http://proxy:3128 https://github.com/)  (期望 000/407)"
echo "task-a 网络直连 task-b 网络 proxy 外的东西: 两个 internal 网络互不相通（无路由）"

echo; echo "== 工具兼容性（task-a 凭据） =="
t() { local name="$1" cmd="$2" r; r=$(run_a "$cmd"); printf '%-8s exit=%-3s %s\n' "$name" "$r" "$(tail -c 160 /tmp/m04.out | tr '\n' ' ')"; }
t curl   'curl -s -o /dev/null -w "%{http_code}" https://api.anthropic.com/'
t git    'git ls-remote https://github.com/octocat/Hello-World HEAD'
t npm    'npm view left-pad version'
t pnpm   'pnpm view left-pad version'
t pip    'pip download --no-deps -d /tmp/p six --break-system-packages -q && ls /tmp/p'
t uv     'uv venv -q /tmp/v && uv pip install -q -p /tmp/v six && echo uv-ok'
t go     'mkdir -p /tmp/g && cd /tmp/g && go mod init x >/dev/null 2>&1 && go mod download -x rsc.io/quote@v1.5.2 2>&1 | tail -1; go list -m rsc.io/quote@v1.5.2'
t cargo  'cargo new -q /tmp/c && cd /tmp/c && echo "itoa = \"1\"" >> Cargo.toml && cargo fetch -q && echo cargo-ok'
t claude 'ANTHROPIC_API_KEY=sk-ant-dummy timeout 60 claude -p hi'
t codex  'OPENAI_API_KEY=sk-dummy timeout 60 codex exec --skip-git-repo-check hi'

echo; echo "== access.log =="
docker exec m04-proxy cat /var/log/squid/access.log
