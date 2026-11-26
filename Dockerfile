# syntax=docker/dockerfile:1
# Odin API Gateway runtime image: a static binary on a small Alpine base, running as a non-root
# user. Multi-arch: `docker buildx build --platform linux/amd64,linux/arm64 .` cross-compiles in
# the build stage (no emulation needed).
#
#   docker build -t odin .
#   docker run --rm -p 8080:8080 odin
#   docker run --rm -p 8080:8080 -v "$PWD/config/config.yaml:/app/config/config.yaml:ro" odin
#
# Go plugins (.so) need cgo and a glibc base, so this image does not load them; build the binary
# with CGO_ENABLED=1 on a Debian base if you need plugin upload.

# Keep the Go version in step with the toolchain line in go.mod and docker/ci/Dockerfile.
ARG GO_IMAGE=golang:1.27.4-alpine3.24
ARG RUNTIME_IMAGE=alpine:3.24.4

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG BUILD_TIME=unknown
WORKDIR /src

# Dependencies first, so source edits do not invalidate the module download layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY pkg ./pkg
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}" \
      -o /out/odin ./cmd/odin

FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 65532 odin \
 && adduser -S -D -H -u 65532 -G odin odin

WORKDIR /app
# The admin interface reads its templates and assets relative to the working directory.
COPY --from=build /out/odin /app/odin
COPY pkg/admin/templates /app/pkg/admin/templates
COPY pkg/admin/static /app/pkg/admin/static
COPY config/default_config.yaml /app/config/config.yaml

USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/app/odin", "-healthcheck"]
ENTRYPOINT ["/app/odin"]
CMD ["-config", "/app/config/config.yaml"]
