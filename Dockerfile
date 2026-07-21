# syntax=docker/dockerfile:1
#
# Stowage — the deployable memory-server image.
#
# Two stages:
#   1. build — compiles the CGo-free static Go binary, the SAME
#              `CGO_ENABLED=0 go build ./cmd/stowage` that `make build` runs
#              (CLAUDE.md §5/§6: one static binary, no CGo, cross-compiles).
#   2. final — distroless static base, non-root (uid 65532), carrying exactly
#              the binary at /stowage and an empty /data owned by the non-root
#              user, where the SQLite driver writes when self-hosting on a real
#              disk. On a managed platform Stowage points at Postgres (Neon) and
#              never touches /data.
#
# The image binds 0.0.0.0:10000 (STOWAGE_SERVER_LISTEN) so a single-port PaaS
# (Render/Heroku/Fly, whose default web port is 10000) routes to it with no extra
# config. Override STOWAGE_SERVER_LISTEN if your platform assigns another port.
#
# Health check: distroless ships no shell or curl, so there is no in-image
# HEALTHCHECK — the platform pings the HTTP health path GET /healthz (set it as
# the service's Health Check Path). For docker-compose, add a healthcheck that
# hits /healthz from a sidecar, or run the non-distroless variant.
#
# Build:  docker build -t stowage .
# Run:    docker run -p 10000:10000 -e STOWAGE_GATEWAY_API_KEY=... stowage

# ---- Stage 1: the Go binary --------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# VERSION lands in /healthz output and `stowage version`; override with
# --build-arg VERSION=$(git describe --tags --always) from a tagged checkout.
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/hurtener/stowage/internal/version.Version=${VERSION}" \
    -o /out/stowage ./cmd/stowage
# An empty, non-root-owned /data skeleton for the final stage: when a volume is
# first mounted at /data, Docker seeds it from the image directory — including
# this ownership — so the non-root process can write its SQLite file there when
# self-hosting. Managed deployments use Postgres and leave /data untouched.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

# ---- Stage 2: the runtime image ----------------------------------------------
# distroless/static: no shell, no package manager, no libc — just enough to run a
# static binary as the non-root `nonroot` user (uid/gid 65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stowage /stowage
COPY --from=build --chown=65532:65532 /out/data /data

# Bind all interfaces on the platform's default web port; the SQLite fallback
# writes under /data. A managed deployment overrides STOWAGE_STORE_DRIVER/DSN to
# point at Postgres and never uses /data.
#
# STOWAGE_SERVER_MCP_TRUST_PROXY=true because this image is built for hosting
# behind a reverse proxy (Render/Heroku/Fly/a load balancer) that terminates TLS
# and forwards over loopback — without it the MCP transport's DNS-rebinding guard
# 403s every proxied request (D-156). Set it to false only if you run this image
# bound directly to localhost as a local MCP server.
ENV STOWAGE_SERVER_LISTEN=0.0.0.0:10000 \
    STOWAGE_STORE_DSN=/data/stowage.db \
    STOWAGE_SERVER_MCP_TRUST_PROXY=true
EXPOSE 10000

USER nonroot
ENTRYPOINT ["/stowage"]
CMD ["serve"]
