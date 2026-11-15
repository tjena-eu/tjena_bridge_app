# tjena Bridge in Docker: the same programs as the phone app, with a web UI.
#
#   docker compose -f docker/compose.yaml -f docker/compose.build.yaml up -d --build
#
# Prebuilt images (amd64, arm64): ghcr.io/tjena-eu/tjena-bridge, built by
# .github/workflows/docker.yml.
#
# Build context is the repository root (needs relay/ and wacall/).
# mautrix-whatsapp is built from its upstream tag WITHOUT changes; keep
# MAUTRIX_TAG equal to PINNED_TAG in android-binary/build.sh.

ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-trixie AS build
ARG MAUTRIX_TAG=v0.2610.0
ENV CGO_ENABLED=1 GOTOOLCHAIN=auto
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src

# 1. mautrix-whatsapp, unmodified upstream tag.
RUN git clone --quiet --depth 1 --branch "${MAUTRIX_TAG}" https://github.com/mautrix/whatsapp whatsapp
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd whatsapp \
 && test -z "$(git status --porcelain)" \
 && MAUTRIX_VERSION="$(go list -m -f '{{.Version}}' maunium.net/go/mautrix)" \
 && CGO_CFLAGS="-O2 -DSQLITE_ENABLE_FTS5" go build -trimpath -tags goolm,sqlite_omit_load_extension \
      -ldflags "-s -w -X main.Tag=${MAUTRIX_TAG} -X main.Commit=$(git rev-parse HEAD) \
        -X 'main.BuildTime=$(date -u -Iseconds)' -X 'maunium.net/go/mautrix.GoModVersion=${MAUTRIX_VERSION}'" \
      -o /out/mautrix-whatsapp ./cmd/mautrix-whatsapp

# 2. Our relay and calls daemon (same source as in the phone app).
COPY relay/ relay/
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd relay && go build -trimpath -ldflags "-s -w" -o /out/bridge-relay .
COPY wacall/ wacall/
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd wacall && go build -trimpath -ldflags "-s -w" -o /out/wacall .

# 3. The manager with the web UI.
ARG VERSION=
COPY docker/manager/ manager/
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd manager && go test ./... \
 && go build -trimpath -ldflags "-s -w ${VERSION:+-X main.version=${VERSION}}" -o /out/tjena-bridge .

FROM debian:trixie-slim
LABEL org.opencontainers.image.title="tjena Bridge" \
      org.opencontainers.image.description="Personal WhatsApp bridge for tjena!chat, with a web UI" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.source="https://github.com/tjena-eu/tjena_bridge_app"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 1000 --home-dir /data --no-create-home --shell /usr/sbin/nologin bridge \
 && mkdir -p /data && chown bridge:bridge /data && chmod 700 /data
COPY --from=build /out/ /usr/local/bin/
COPY LICENSE /usr/share/doc/tjena-bridge/LICENSE
COPY wacall/THIRD_PARTY.md /usr/share/doc/tjena-bridge/wacall-THIRD_PARTY.md
USER bridge
ENV TJENA_DATA=/data TJENA_BIN=/usr/local/bin TJENA_RUN=/tmp/tjena-run WEBUI_LISTEN=:8080
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=6s --start-period=20s CMD ["tjena-bridge", "healthcheck"]
ENTRYPOINT ["tjena-bridge"]
