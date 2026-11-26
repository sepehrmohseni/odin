---
name: dependency-upgrade
description: Upgrade Odin's Go modules, the Go toolchain or the pinned CI tools, and clear govulncheck findings. Use for dependency bumps, vulnerability reports or new Go releases.
---

# Upgrade dependencies

1. Baseline: `scripts/docker-check.sh vuln` (and `lint`) to see what is reported now.
2. Modules within their major versions:
   ```bash
   scripts/docker-check.sh upgrade          # go get -u ./... and go mod tidy, in the checkout
   git diff go.mod
   ```
   Major upgrades (Echo v5, mongo-driver v2) are roadmap items needing approval; don't do them
   here. A fix that exists only as an untagged commit can be pinned to its pseudo-version (see
   gorilla/websocket in go.mod) with
   `scripts/docker-check.sh -- go get <module>@<pseudo-version>` followed by `fix`.
3. Run `scripts/docker-check.sh`. New staticcheck SA1019 deprecations are part of the upgrade:
   replace the deprecated API (for example otelecho became `pkg/tracing/middleware.go`).
4. Toolchain or tools:
   - Go: `ARG GO_IMAGE` in `docker/ci/Dockerfile` and `Dockerfile`, plus the `toolchain` line in
     `go.mod`.
   - golangci-lint: `GOLANGCI_LINT_VERSION` and both `GOLANGCI_LINT_SHA256_*`, from the
     release's `checksums.txt`.
   - govulncheck: `GOVULNCHECK_VERSION`.
   The image tag follows the versions, so the next run rebuilds it.
5. Commit as `build: ...`, listing what moved and which vulnerabilities closed; push; check CI.
