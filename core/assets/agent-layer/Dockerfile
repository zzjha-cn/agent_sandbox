# Agent 层：叠加在任意 Debian/Ubuntu 系 Profile 之上（ADR 0007、design §5.3）
ARG BASE=debian:bookworm-slim
FROM ${BASE}

ARG UID=1000
ARG GID=1000
ARG CLAUDE_VERSION=latest

USER root
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      tini tmux git ripgrep jq less procps ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

RUN npm i -g @anthropic-ai/claude-code@${CLAUDE_VERSION} && npm cache clean --force

# agent 用户：UID 和宿主机一致；已有同 UID 用户（如 node 镜像的 node）则改名复用，
# 已有同 GID 的组直接沿用。
RUN set -eux; \
    if ! getent group "${GID}" >/dev/null; then groupadd -g "${GID}" agent; fi; \
    if getent passwd "${UID}" >/dev/null; then \
      old="$(getent passwd "${UID}" | cut -d: -f1)"; \
      [ "$old" = agent ] || usermod -l agent -d /home/agent -m -g "${GID}" -s /bin/bash "$old"; \
    else \
      useradd -m -u "${UID}" -g "${GID}" -s /bin/bash agent; \
    fi; \
    mkdir -p /home/agent /sbx/cache /sbx/state /sbx/gen /sbx/host-claude; \
    chown -R "${UID}:${GID}" /home/agent /sbx

COPY entrypoint.sh /sbx/entrypoint.sh
RUN chmod 0755 /sbx/entrypoint.sh

# npm 全局目录归 root，agent 用户无法自更新；升级走 sbx upgrade（重建 Agent 层）
ENV HOME=/home/agent \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    DISABLE_AUTOUPDATER=1 \
    npm_config_cache=/sbx/cache/npm \
    PIP_CACHE_DIR=/sbx/cache/pip \
    UV_CACHE_DIR=/sbx/cache/uv \
    GOMODCACHE=/sbx/cache/go-mod \
    GOCACHE=/sbx/cache/go-build \
    CARGO_HOME=/sbx/cache/cargo \
    LANG=C.UTF-8 \
    TERM=xterm-256color

USER agent
WORKDIR /home/agent
ENTRYPOINT ["tini", "--", "/sbx/entrypoint.sh"]
