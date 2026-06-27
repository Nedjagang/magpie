# Dockerfile for the Magpie control plane (magpied).
#
# Two-stage build: compile in golang, ship a distroless final image. Result
# is ~30 MB and holds just the binary + migrations, no shell.
#
# Build from the repo root:
#   docker build -f packaging/docker/Dockerfile -t magpie/magpied:1.0.0 .

# ── build stage ────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads across rebuilds
COPY control-plane/go.mod control-plane/go.sum ./control-plane/
RUN cd control-plane && go mod download

COPY control-plane/ ./control-plane/

ARG VERSION=1.0.0
RUN cd control-plane && \
    CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/magpied \
      ./cmd/magpied

# Pre-create /data so the distroless runtime has an owned, writable mount
# point for the SQLite DB. Without this, the volume Docker creates at /data
# is root-owned and the nonroot process gets SQLITE_CANTOPEN (error 14).
RUN mkdir -p /data

# ── runtime stage ──────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="magpied" \
      org.opencontainers.image.description="Magpie control plane" \
      org.opencontainers.image.source="https://github.com/magpie-project/magpie" \
      org.opencontainers.image.licenses="Apache-2.0"

WORKDIR /app
COPY --from=build /out/magpied /app/magpied
# Carry the nonroot-owned /data directory over so Docker seeds the named
# volume with correct permissions on first run.
COPY --from=build --chown=nonroot:nonroot /data /data

# SQLite database lives in /data so it can be mounted as a volume.
ENV MAGPIE_DB_PATH=/data/magpie.db \
    MAGPIE_HTTP_ADDR=:12002
VOLUME ["/data"]

EXPOSE 12002
USER nonroot:nonroot

ENTRYPOINT ["/app/magpied"]
