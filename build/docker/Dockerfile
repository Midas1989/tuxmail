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
# The Go binary is cross-compiled on the build platform (no QEMU emulation needed).

ARG FRONTEND=build
ARG NODE_IMAGE=docker.io/library/node:22-alpine
ARG GO_IMAGE=docker.io/library/golang:1.24-alpine
ARG RUNTIME_IMAGE=docker.io/library/alpine:3.22

# ---------------------------------------------------------------- frontend
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
COPY locales/ /src/locales/
COPY assets/branding/ /src/assets/branding/
RUN npm run build && mkdir -p /out && cp -R dist/. /out/

FROM scratch AS frontend-prebuilt
COPY web/dist/ /out/

# hadolint ignore=DL3006
FROM frontend-${FRONTEND} AS frontend

# ---------------------------------------------------------------- go
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go
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

# ---------------------------------------------------------------- runtime
# hadolint ignore=DL3006
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
LABEL org.opencontainers.image.title="TuxMail" \
      org.opencontainers.image.description="TuxMail server (web client, PWA and API)" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.source="https://github.com/Midas1989/Mailclient" \
      org.opencontainers.image.version="${VERSION}"

# ca-certificates: TLS to mail servers; tzdata: time zones; wget (busybox) for the health check.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 tuxmail \
    && adduser -S -D -H -u 10001 -G tuxmail -h /data -s /sbin/nologin tuxmail \
    && mkdir -p /data /etc/tuxmail \
    && chown tuxmail:tuxmail /data

COPY --from=go /out/tuxmail /usr/local/bin/tuxmail
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
    CMD wget -q -O /dev/null "http://127.0.0.1:${TUXMAIL_LISTEN##*:}/healthz" || exit 1

ENTRYPOINT ["/usr/local/bin/tuxmail"]
CMD ["serve", "--config", "/etc/tuxmail/config.yaml"]
