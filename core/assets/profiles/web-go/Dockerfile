# Profile web-go：Go + Node LTS + pnpm + 基础 Python（design §5.2）
FROM debian:bookworm-slim

ARG GO_VERSION=1.27.0
ARG NODE_MAJOR=24
ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg xz-utils git build-essential pkg-config \
      python3 python3-venv python3-pip \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/* \
    && npm i -g corepack@latest && corepack enable && corepack prepare pnpm@latest --activate

# 软链接到 /usr/local/bin：login shell（tmux、bash -l）会被 /etc/profile 重置 PATH
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH:-amd64}.tar.gz" | tar -C /usr/local -xz \
    && ln -s /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/local/bin/
