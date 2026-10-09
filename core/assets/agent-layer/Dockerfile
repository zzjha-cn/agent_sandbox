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

# claude 是 npm 包，所以 Node 属于 Agent 层而不是 Profile（design §5.1）：
# py-rust 这类 Profile 里没有 Node，不能因此装不上 Agent。底层自带就不动它。
ARG NODE_MAJOR=24
RUN if ! command -v npm >/dev/null 2>&1; then \
      curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
      && apt-get install -y --no-install-recommends nodejs \
      && rm -rf /var/lib/apt/lists/*; \
    fi

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

# mise：按项目里的版本文件装运行时（M3-3、design §5.2）。
# 装到 /usr/local/bin，运行时数据放共享 volume sbx-mise。
# 先下载再执行：`curl | sh` 的退出码是 sh 的，下载失败时这一层会"构建成功"
# 但什么都没装上（py-rust 的 uv 实测踩到过）
RUN curl -fsSL https://mise.run -o /tmp/mise-install.sh \
    && MISE_INSTALL_PATH=/usr/local/bin/mise sh /tmp/mise-install.sh \
    && rm -f /tmp/mise-install.sh \
    && chmod 0755 /usr/local/bin/mise \
    && mise --version
# login shell（tmux、bash -l）会被 /etc/profile 重置 PATH，ENV 里的 shims 目录会被挤掉
# （design §5.3 的老坑）。放一份 profile.d 片段，两种 shell 都能拿到 shims。
RUN printf 'export PATH="$HOME/.local/share/mise/shims:$PATH"\n' > /etc/profile.d/10-sbx-mise.sh

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
    MISE_DATA_DIR=/home/agent/.local/share/mise \
    MISE_YES=1 \
    MISE_IDIOMATIC_VERSION_FILE_ENABLE_TOOLS=node,python,ruby \
    PATH=/home/agent/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin \
    LANG=C.UTF-8 \
    TERM=xterm-256color

USER agent
WORKDIR /home/agent
ENTRYPOINT ["tini", "--", "/sbx/entrypoint.sh"]
