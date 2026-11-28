# syntax=docker/dockerfile:1
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Midas Wollinger and TuxMail contributors
#
# TuxMail server image (tuxmail serve). Build from the repository root:
#
#   docker build -f build/docker/Dockerfile -t tuxmail .
#   docker buildx build -f build/docker/Dockerfile --platform linux/amd64,linux/arm64 .
#
# Build arguments:
#   VERSION   version string (default: dev)
#   FRONTEND  "build" (default: build frontend/ in a Node stage) or
#             "prebuilt" (use web/dist from the build context, as in CI)
#   SIGNAL_CLI "auto" (default: bundle signal-cli when pinned and reachable),
#             "bundle" (required, as in release CI) or "skip" (the app then
#             offers the on-demand install into /data/plugins/signal-cli)
# signal-cli (Signal plugin) is the GraalVM native build on amd64 and the JVM
# build with a Temurin JRE on arm64 (build/signal-cli.sh); both need glibc,
# hence the Debian runtime image.
# The Go binary is cross-compiled on the build platform (no QEMU emulation needed).

ARG FRONTEND=build

# ---------------------------------------------------------------- base images
# Pinned by digest of the multi-arch index (re-audit ND-08). Dependabot's
# docker ecosystem (.github/dependabot.yml, directory /build/docker) keeps tag
# and digest current. The digests were read from the registry on 2026-11-05 (golang: 2026-11-28).
FROM --platform=$BUILDPLATFORM docker.io/library/node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS node-base
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS go-base
FROM docker.io/library/debian:trixie-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0 AS runtime-base

# ---------------------------------------------------------------- frontend
FROM node-base AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
COPY locales/ /src/locales/
COPY internal/mailcmd/ /src/internal/mailcmd/
COPY assets/branding/ /src/assets/branding/
# Settings → About embeds these repository-root files (?raw imports).
COPY VERSION LICENSE NOTICE THIRD_PARTY_NOTICES.md /src/
RUN npm run build && mkdir -p /out && cp -R dist/. /out/

FROM scratch AS frontend-prebuilt
COPY web/dist/ /out/

# hadolint ignore=DL3006
FROM frontend-${FRONTEND} AS frontend

# ---------------------------------------------------------------- go
FROM go-base AS go
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY . .
COPY --from=frontend /out/ /src/web/dist/
RUN test -f web/dist/index.html || { echo "web/dist/index.html missing: run 'go generate ./web' or use FRONTEND=build" >&2; exit 1; }
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tuxmail ./cmd/tuxmail
# Messenger plugins (WhatsApp, Telegram, Matrix, Signal), pure Go.
# Limited build parallelism: amd64 and arm64 build side by side on one runner,
# and the large messenger libraries otherwise exhaust the runner's memory.
# hadolint ignore=DL3059
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOFLAGS=-p=2 GOMEMLIMIT=3GiB sh build/plugins.sh target "$TARGETOS" "$TARGETARCH" /out/plugins
# signal-cli for the Signal plugin: pinned downloads, SHA-256 verified
# (internal/messenger/signalcli/pins.env).
ARG SIGNAL_CLI=auto
# hadolint ignore=DL3059
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/src/build/cache/signal-cli \
    case "$SIGNAL_CLI" in \
      skip) echo "signal-cli not bundled (SIGNAL_CLI=skip)" ;; \
      bundle|auto) sh build/signal-cli.sh bundle "$TARGETOS" "$TARGETARCH" /out/plugins/signal-cli \
        || { [ "$SIGNAL_CLI" = auto ] && rm -rf /out/plugins/signal-cli \
             && echo "WARNING: signal-cli not bundled; TuxMail offers the on-demand install" >&2; } ;; \
      *) echo "SIGNAL_CLI must be auto, bundle or skip" >&2; exit 1 ;; \
    esac

# ---------------------------------------------------------------- runtime
FROM runtime-base
ARG VERSION=dev
LABEL org.opencontainers.image.title="TuxMail" \
      org.opencontainers.image.description="TuxMail server (web client, PWA and API)" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.source="https://github.com/Midas1989/tuxmail" \
      org.opencontainers.image.version="${VERSION}"

# ca-certificates: TLS to mail servers; tzdata: time zones; wget for the health check.
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 tuxmail \
    && useradd --system --uid 10001 --gid tuxmail --home-dir /data --no-create-home --shell /usr/sbin/nologin tuxmail \
    && mkdir -p /data /etc/tuxmail \
    && chown tuxmail:tuxmail /data

COPY --from=go /out/tuxmail /usr/local/bin/tuxmail
# Found by the plugin host as <exe dir>/../lib/tuxmail/plugins.
# signal-cli (if bundled) is in plugins/signal-cli.
COPY --from=go /out/plugins/ /usr/local/lib/tuxmail/plugins/
COPY build/docker/config.yaml /etc/tuxmail/config.yaml
COPY LICENSE NOTICE /usr/share/doc/tuxmail/

ENV TUXMAIL_LISTEN=0.0.0.0:8080 \
    TUXMAIL_DATA_DIR=/data \
    TUXMAIL_PUBLIC_URL=http://localhost:8080 \
    TUXMAIL_LOG_LEVEL=info

USER 10001:10001
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${TUXMAIL_LISTEN##*:}/healthz?deep=1" || exit 1

ENTRYPOINT ["/usr/local/bin/tuxmail"]
CMD ["serve", "--config", "/etc/tuxmail/config.yaml"]
