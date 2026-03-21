FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

# System deps: gcc + GTK/appindicator headers (required by systray for Linux),
# hidapi + udev headers (required by go-hid)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git xz-utils \
    gcc pkg-config libc6-dev \
    libhidapi-dev libudev-dev \
    libayatana-appindicator3-dev libgtk-3-dev \
    && rm -rf /var/lib/apt/lists/*

# Go + Zig + CC wrappers in a single layer
ARG GO_VERSION=1.22.12
ARG ZIG_VERSION=0.13.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz \
    && curl -fsSL "https://ziglang.org/download/${ZIG_VERSION}/zig-linux-x86_64-${ZIG_VERSION}.tar.xz" | tar -C /opt -xJ \
    && ln -s /opt/zig-linux-x86_64-${ZIG_VERSION}/zig /usr/local/bin/zig \
    && mkdir -p /usr/local/bin/zig-cc \
    && printf '#!/bin/sh\nexec zig cc -target aarch64-macos-none "$@"\n' > /usr/local/bin/zig-cc/zig-cc-darwin-arm64 \
    && printf '#!/bin/sh\nexec zig cc -target x86_64-windows-gnu "$@"\n' > /usr/local/bin/zig-cc/zig-cc-windows-amd64 \
    && chmod +x /usr/local/bin/zig-cc/*

ENV PATH="/usr/local/go/bin:/usr/local/bin/zig-cc:${PATH}"
WORKDIR /build
