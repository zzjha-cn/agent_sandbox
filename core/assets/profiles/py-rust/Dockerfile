# Profile py-rust：uv（管 Python 版本和依赖）+ rustup stable（含 clippy、rustfmt）（design §5.2）
FROM debian:bookworm-slim

ARG RUST_VERSION=stable

ENV DEBIAN_FRONTEND=noninteractive
# 不装系统 python：这个 Profile 里 Python 由 uv 负责（版本和依赖都是），
# 再来一个系统解释器只会让"我现在用的是哪个 python"变得不好回答（design §5.2）
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git build-essential pkg-config libssl-dev \
    && rm -rf /var/lib/apt/lists/*

# 软链接到 /usr/local/bin：login shell（tmux、bash -l）会被 /etc/profile 重置 PATH，
# 装在 CARGO_HOME/bin、/root/.local/bin 里的东西在那之后就找不到了（design §5.3）
ENV CARGO_HOME=/usr/local/cargo RUSTUP_HOME=/usr/local/rustup
RUN curl -fsSL https://sh.rustup.rs | sh -s -- -y --no-modify-path \
      --default-toolchain ${RUST_VERSION} --profile minimal --component clippy,rustfmt \
    && ln -sf "$CARGO_HOME"/bin/* /usr/local/bin/ \
    && chmod -R a+rwX "$CARGO_HOME" "$RUSTUP_HOME"

# uv 装 Python 版本和依赖；UV_CACHE_DIR 等在 Agent 层指向 /sbx/cache。
# 先下载再执行、最后 uv --version 验一下：`curl | sh` 的退出码是 sh 的，
# 下载失败时这一层会"构建成功"但什么都没装上（实测踩到）。
RUN curl -fsSL https://astral.sh/uv/install.sh -o /tmp/uv-install.sh \
    && env UV_INSTALL_DIR=/usr/local/bin sh /tmp/uv-install.sh \
    && rm -f /tmp/uv-install.sh \
    && uv --version
