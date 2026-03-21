FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# System deps for CGO builds (hidapi, systray, zig cross-compilation)
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    curl \
    git \
    libhidapi-dev \
    libudev-dev \
    libayatana-appindicator3-dev \
    libgtk-3-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

# Go
ARG GO_VERSION=1.22.12
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# Zig (cross-compiler for CGO)
ARG ZIG_VERSION=0.13.0
RUN curl -fsSL "https://ziglang.org/download/${ZIG_VERSION}/zig-linux-x86_64-${ZIG_VERSION}.tar.xz" \
    | tar -C /opt -xJ \
    && ln -s "/opt/zig-linux-x86_64-${ZIG_VERSION}/zig" /usr/local/bin/zig

# Zig CC wrapper scripts for cross-compilation
RUN mkdir -p /usr/local/bin/zig-cc \
    && printf '#!/bin/sh\nexec zig cc -target x86_64-linux-gnu "$@"\n' > /usr/local/bin/zig-cc/zig-cc-linux-amd64 \
    && printf '#!/bin/sh\nexec zig cc -target aarch64-macos-none "$@"\n' > /usr/local/bin/zig-cc/zig-cc-darwin-arm64 \
    && printf '#!/bin/sh\nexec zig cc -target x86_64-windows-gnu "$@"\n' > /usr/local/bin/zig-cc/zig-cc-windows-amd64 \
    && chmod +x /usr/local/bin/zig-cc/*
ENV PATH="/usr/local/bin/zig-cc:${PATH}"

WORKDIR /build
