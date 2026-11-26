# A Go toolchain image for running checks in Docker: Go, golangci-lint and govulncheck at pinned
# versions, for x86_64 and arm64. Nothing in it is specific to Odin, so any Go project can build
# the same image (same versions, same tag) and share it and its cache volumes.
#
# Run it through scripts/docker-check.sh rather than by hand. The project's own steps live in
# docker/ci/check.sh, which runs from the read-only checkout, so editing them needs no rebuild.

# Keep in step with the `toolchain` line in go.mod.
ARG GO_IMAGE=golang:1.27.4-trixie
FROM ${GO_IMAGE}

ARG GOLANGCI_LINT_VERSION=2.15.0
# From golangci-lint-${GOLANGCI_LINT_VERSION}-checksums.txt on the release page. Pinned here so a
# replaced release asset fails the build instead of being trusted.
ARG GOLANGCI_LINT_SHA256_AMD64=9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
ARG GOLANGCI_LINT_SHA256_ARM64=b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998
# Installed with `go install`, which checks the module against the Go checksum database.
ARG GOVULNCHECK_VERSION=v1.10.0

RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      amd64) arch=amd64; sum="$GOLANGCI_LINT_SHA256_AMD64" ;; \
      arm64) arch=arm64; sum="$GOLANGCI_LINT_SHA256_ARM64" ;; \
      *) echo "unsupported architecture: $(dpkg --print-architecture)" >&2; exit 1 ;; \
    esac; \
    file="golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${arch}.tar.gz"; \
    cd /tmp; \
    curl -fsSLO "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/${file}"; \
    echo "${sum}  ${file}" | sha256sum -c -; \
    tar -xzf "$file" --no-same-owner; \
    install -m 755 "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${arch}/golangci-lint" /usr/local/bin/golangci-lint; \
    rm -rf "$file" "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${arch}"; \
    GOBIN=/usr/local/bin GOFLAGS=-mod=mod GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomod \
      go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"; \
    rm -rf /tmp/gocache /tmp/gomod; \
    golangci-lint version; \
    govulncheck -version

# The container runs as the caller's own user id, so the caches and the work directory must be
# writable by any id. The caches live in named volumes created from these directories; Go's
# module and build caches are content-addressed and safe to share between projects and runs.
# GOPATH holds the checksum-database cache (pkg/sumdb), which `go get` writes; the image's
# default /go belongs to root.
RUN mkdir -p /cache/gomod /cache/gobuild /cache/golangci /cache/home /cache/gopath /work \
 && chmod 1777 /cache/gomod /cache/gobuild /cache/golangci /cache/home /cache/gopath /work

ENV GOPATH=/cache/gopath \
    GOMODCACHE=/cache/gomod \
    GOCACHE=/cache/gobuild \
    GOLANGCI_LINT_CACHE=/cache/golangci \
    GOTOOLCHAIN=local \
    HOME=/cache/home

WORKDIR /work
