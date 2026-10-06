#!/usr/bin/env bash
# M1 端到端验收（WP9.1 + 9.3）：夹具仓库上开两个并行 Task，让 claude 各提交一次，再做安全检查和清理检查。
# 前提：已 make build；sbx-home 里已登录 claude；Docker Desktop 在运行。
set -u
cd "$(dirname "$0")/.."
SBX="$PWD/bin/sbx"
TIMEOUT="${E2E_TIMEOUT:-300}"
fail=0
pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail+1)); }
check() { if eval "$2" >/dev/null 2>&1; then pass "$1"; else bad "$1"; fi; }

# 1. 夹具仓库
FIX="$(mktemp -d "${TMPDIR:-/tmp}/sbx-e2e.XXXXXX")"
FIX="$(cd "$FIX" && pwd -P)/fixture"
mkdir -p "$FIX" && cd "$FIX"
git init -q -b main
printf '{ "name": "sbx-e2e", "version": "0.0.0", "private": true }\n' > package.json
printf '# sbx e2e fixture\n' > README.md
printf 'node_modules/\n' > .gitignore
git add -A && git -c user.name=e2e -c user.email=e2e@example.com commit -qm init
echo "== 夹具仓库：$FIX"

# 假的宿主机 ~/.claude：带一个 skills 目录和这个仓库的项目记忆（ADR 0016）
export SBX_HOST_CLAUDE="$(dirname "$FIX")/host-claude"
KEY="$(printf '%s' "$FIX" | sed 's/[^A-Za-z0-9]/-/g')"
HMEM="$SBX_HOST_CLAUDE/projects/$KEY/memory"
mkdir -p "$SBX_HOST_CLAUDE/skills/demo" "$HMEM"
printf -- '- [host note](host-note.md)\n' > "$HMEM/MEMORY.md"
printf 'remembered on host\n' > "$HMEM/host-note.md"

cleanup() {
  cd "$FIX" 2>/dev/null && "$SBX" done --force t1 t2 >/dev/null 2>&1
}
trap cleanup EXIT

# 2. 两个 Task
echo "== 启动 t1、t2"
"$SBX" run t1 --detach || { echo "sbx run t1 失败"; exit 1; }
"$SBX" run t2 --detach || { echo "sbx run t2 失败"; exit 1; }
WS="$("$SBX" ls | awk 'NR==1{print $2}')"
STATE="${SBX_HOME:-$HOME/.sbx}/state/$WS"
C1="sbx-$WS-t1"; C2="sbx-$WS-t2"
"$SBX" ls

# 3. 发指令
send() { # send <container> <text>
  docker exec -u agent "$1" tmux send-keys -t agent -l "$2"
  sleep 1
  docker exec -u agent "$1" tmux send-keys -t agent Enter
}
for t in t1 t2; do
  send "sbx-$WS-$t" "在 README.md 末尾追加一行 'hello from $t'，然后用 git commit -am 'hello from $t' 提交。不要做别的事。"
done

# 4. 等两个 Task 回到 idle/Stop
echo "== 等待 Agent 完成（最多 ${TIMEOUT}s）"
deadline=$(( $(date +%s) + TIMEOUT ))
for t in t1 t2; do
  until grep -q '"event":"Stop"' "$STATE/$t/status.json" 2>/dev/null; do
    if [ "$(date +%s)" -gt "$deadline" ]; then bad "$t 超时未回到 idle/Stop"; break; fi
    sleep 5
  done
done
"$SBX" ls

# 5. 分支断言
echo "== 分支"
for t in t1 t2; do
  check "sbx/$t 存在且领先 main 1 个提交" "[ \"\$(git -C '$FIX' rev-list --count main..sbx/$t)\" = 1 ]"
  check "sbx/$t 的 README 有 hello from $t" "git -C '$FIX' show sbx/$t:README.md | grep -q 'hello from $t'"
done

# 5a. 工作目录
check "sbx path 和容器里的工作目录一致" "[ \"\$(\"$SBX\" path t1)\" = \"\$(docker exec -u agent $C1 pwd)\" ]"
check "sbx path（main）是仓库根" "[ \"\$(\"$SBX\" path)\" = '$FIX' ]"
check "sbx ls 有 PATH 列" "\"$SBX\" ls | grep -q 'PATH'"

# 5b. 停止与恢复（WP8.7）
echo "== 停止与恢复"
status_of() { "$SBX" ls | awk -v t="$1" '$1==t{print $2}'; }
"$SBX" stop t2 >/dev/null
check "stop 后显示 stopped" "[ \"\$(status_of t2)\" = stopped ]"
"$SBX" run t2 --detach >/dev/null 2>&1
check "run 恢复后显示 idle" "[ \"\$(status_of t2)\" = idle ]"

# 5c. 项目记忆（ADR 0016）
echo "== 项目记忆"
SMEM="/home/agent/.claude/projects/$KEY/memory"
check "宿主机记忆已导入沙箱" "docker exec -u agent '$C1' grep -q 'remembered on host' '$SMEM/host-note.md'"
docker exec -u agent "$C1" bash -c "printf 'learned in sandbox\n' > '$SMEM/sandbox-note.md' && printf -- '- [sandbox note](sandbox-note.md)\n' >> '$SMEM/MEMORY.md'"
"$SBX" memory pull --yes >/dev/null
check "memory pull 导回了沙箱新记的文件" "grep -q 'learned in sandbox' '$HMEM/sandbox-note.md'"
check "MEMORY.md 两边的条目都在" "grep -q 'host note' '$HMEM/MEMORY.md' && grep -q 'sandbox note' '$HMEM/MEMORY.md'"
check "再次 pull 没有新内容" "\"$SBX\" memory pull --yes | grep -q '没有需要导回的内容'"

# 6. 安全检查（9.3）
echo "== 安全检查"
ex() { docker exec -u agent "$C1" bash -c "$1"; }
check "example.com 被拒（不在白名单）" "! ex 'curl -sf --max-time 15 https://example.com'"
check "squid 日志里有 TCP_DENIED/403" "docker exec sbx-proxy grep -q 'TCP_DENIED/403.*example.com' /var/log/squid/access.log"
check "不经代理直连失败" "! ex \"curl -sf --max-time 10 --noproxy '*' https://api.anthropic.com\""
check "云端 MCP 被策略拦截（ADR 0015）" "[ \"\$(ex 'curl -s -o /dev/null -w %{http_connect} --max-time 15 https://mcp-proxy.anthropic.com')\" = 403 ]"
check "~/.ssh 不存在" "ex '[ ! -e ~/.ssh ] && [ ! -e $HOME/.ssh ]'"
check "宿主机 home 里只看得到挂载进来的路径" "[ -z \"\$(ex 'ls -A $HOME 2>/dev/null' | grep -v '^.sbx$')\" ]"
check "以非 root 运行" "[ \"\$(ex 'id -u')\" != 0 ]"
check "claude 自动更新已关闭" "[ \"\$(ex 'echo \$DISABLE_AUTOUPDATER')\" = 1 ]"
WT1="$(ex 'pwd')"
ex 'echo x > node_modules/e2e-probe' >/dev/null 2>&1
check "node_modules 写入不落到宿主机" "ex 'test -f node_modules/e2e-probe' && [ ! -e '$WT1/node_modules/e2e-probe' ]"
docker exec -d -u agent "$C2" python3 -m http.server 8765 --bind 0.0.0.0 >/dev/null
sleep 2
IP2="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$C2")"
check "t2 自己能访问自己的服务" "docker exec -u agent '$C2' curl -sf --noproxy '*' --max-time 5 http://127.0.0.1:8765/"
check "t1 访问不到 t2（网络隔离）" "! ex \"curl -sf --noproxy '*' --max-time 5 http://$IP2:8765/\""
if ex '[ -d /sbx/host-claude/skills ]'; then
  check "宿主机 skills 在容器里只读" "! ex 'touch /sbx/host-claude/skills/.e2e-probe'"
else
  echo "  - 宿主机没有 ~/.claude/skills，跳过只读检查"
fi

# 7. 清理
echo "== 清理"
docker exec -u agent "$C1" bash -c "printf 'late note\n' > '$SMEM/late-note.md'"
DONE_OUT="$("$SBX" done t1 t2 2>&1)"
echo "$DONE_OUT"
trap - EXIT
check "done 提醒还没导回的记忆" "printf '%s' \"\$DONE_OUT\" | grep -q '还没导回宿主机'"
check "done 不删除沙箱里的记忆（在 sbx-home）" "\"$SBX\" memory pull --yes | grep -q 'late-note.md'"
check "没有残留容器" "[ -z \"\$(docker ps -aq --filter label=sbx.ws=$WS)\" ]"
check "没有残留网络" "[ -z \"\$(docker network ls -q --filter label=sbx.ws=$WS)\" ]"
check "没有残留 volume" "[ -z \"\$(docker volume ls -q --filter label=sbx.ws=$WS)\" ]"
check "没有残留 state" "[ ! -d '$STATE' ]"
check "分支保留" "git -C '$FIX' rev-parse --verify -q sbx/t1 && git -C '$FIX' rev-parse --verify -q sbx/t2"

echo
if [ "$fail" -eq 0 ]; then echo "E2E 通过"; else echo "E2E 失败 $fail 项"; fi
exit "$fail"
