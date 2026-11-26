---
name: docker-check
description: Build, test, lint or vuln-check Odin, or run the dev stack, in Docker. Use before every commit, when a test or lint fails, or whenever a Go command is needed (the host has no Go toolchain).
---

# Run Odin's checks in Docker

1. Full gate, exactly what CI's "Checks (Docker)" job runs:
   ```bash
   scripts/docker-check.sh
   ```
   It ends with `All checks passed.`. The first run builds the `go-ci:go1.27.4-lint2.15.0` image.
2. Narrow it while iterating:
   ```bash
   scripts/docker-check.sh test                                  # race tests
   scripts/docker-check.sh -- go test -run TestName ./test/unit/pkg/gateway/
   scripts/docker-check.sh lint                                  # golangci-lint
   scripts/docker-check.sh vuln                                  # govulncheck
   scripts/docker-check.sh cover                                 # coverage of ./pkg/...
   ```
3. Formatting or `go.mod` problems: `scripts/docker-check.sh fix` (gofmt -w and go mod tidy; the
   only subcommands that write to the checkout are `fix` and `upgrade`).
4. Behaviour through real containers:
   ```bash
   scripts/quickstart.sh                       # builds and starts the dev stack, prints the admin login
   curl http://localhost:8080/api/products     # proxied
   curl -i http://localhost:8080/api/orders    # 401 (JWT route)
   docker compose logs odin
   docker compose down -v
   ```
5. Helm chart: `docker run --rm -v "$PWD/deployments/helm/odin:/chart:ro" alpine/helm:3.22.1 lint /chart`.

Notes: the checkout is mounted read-only and copied into the container, so build outputs never
touch it. `GO_CI_CPUS=N` changes the CPU share (default half the cores). Never commit with a red
gate; fix the cause rather than relaxing `.golangci.yml`.
