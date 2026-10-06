#!/usr/bin/env bash
# M0-2: macOS bind mount IO 性能对比（宿主机原生 vs 容器：依赖在 volume / 依赖在 bind mount）
# 每个场景先跑一轮预热（填充包缓存），再计时第二轮。
set -u
cd "$(dirname "$0")/work"; W="$PWD"
PX=http://host.docker.internal:7890
IMG_NODE=sbx-m04-tools IMG_GO=golang:latest
docker volume create m02-npm-cache >/dev/null; docker volume create m02-go-mod >/dev/null

ts() { python3 -c 'import time;print(time.time())'; }
measure() { # name, cmd...
  local name="$1"; shift; local s e; s=$(ts); "$@" >/tmp/m02.out 2>&1; local rc=$?; e=$(ts)
  printf '| %-38s | %6.1fs | rc=%s |\n' "$name" "$(python3 -c "print($e-$s)")" "$rc"; }

node_host()  { (cd "$W/nextapp" && rm -rf node_modules .next && npm ci --no-audit --no-fund && npx next build); }
node_ctr()   { # $1 = vol|bind
  docker volume rm -f m02-nm >/dev/null 2>&1; docker volume create m02-nm >/dev/null
  local extra=(); [ "$1" = vol ] && extra=(-v m02-nm:"$W/nextapp/node_modules")
  docker run --rm -e HTTPS_PROXY=$PX -e HTTP_PROXY=$PX -v "$W/nextapp:$W/nextapp" ${extra[@]+"${extra[@]}"} \
    -v m02-npm-cache:/root/.npm -w "$W/nextapp" $IMG_NODE \
    bash -c 'rm -rf .next; find node_modules -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null; npm ci --no-audit --no-fund && npx next build'; }
go_host()    { (cd "$W/gin" && go clean -testcache && go test -skip TestSaveUploadedFileWithPermissionFailed ./... ); }
go_ctr()     { docker run --rm -e HTTPS_PROXY=$PX -e HTTP_PROXY=$PX -v "$W/gin:$W/gin" -v m02-go-mod:/go/pkg/mod \
    -v m02-go-build:/root/.cache/go-build -w "$W/gin" $IMG_GO bash -c 'go clean -testcache && go test -skip TestSaveUploadedFileWithPermissionFailed ./...'; }  # 该用例依赖非 root，root 下必失败
gst_host()   { (cd "$W/nextapp" && for i in $(seq 20); do git status -s >/dev/null; done); }
gst_ctr()    { docker run --rm -v "$W/nextapp:$W/nextapp" -w "$W/nextapp" $IMG_NODE bash -c 'git config --global --add safe.directory "*"; for i in $(seq 20); do git status -s >/dev/null; done'; }

(cd "$W/nextapp" && [ -d .git ] || (git init -q && git add -A && git -c user.email=x@x -c user.name=x commit -qm init))

echo "## 预热"; node_host >/dev/null 2>&1; node_ctr vol >/dev/null 2>&1; go_host >/dev/null 2>&1; go_ctr >/dev/null 2>&1
echo "| 场景 | 耗时 | 结果 |"; echo "|---|---|---|"
measure "Next.js 宿主机原生 (npm ci + build)"    node_host
measure "Next.js 容器 · node_modules 在 volume"  node_ctr vol
measure "Next.js 容器 · node_modules 在 bind"    node_ctr bind
measure "Go gin 宿主机原生 (go test ./...)"      go_host
measure "Go gin 容器 · 源码 bind，缓存 volume"    go_ctr
measure "git status ×20 宿主机"                  gst_host
measure "git status ×20 容器 (bind)"             gst_ctr
