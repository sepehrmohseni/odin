# Odin API Gateway: notes for agents

Odin is an API gateway in Go (module `odin`, Echo v4): path-based routing to HTTP services with
JWT auth, rate limiting, caching, retries, health checks, circuit breakers, canary routing,
transformations, WebSocket/SSE and GraphQL proxying, Prometheus/OpenTelemetry, and an opt-in web
admin. Read [docs/AUDIT.md](docs/AUDIT.md) (what was wrong, what is fixed) and
[ROADMAP.md](ROADMAP.md) (what is next) before changing behaviour.

## Stack

- Go 1.27.4 toolchain (`go.mod`: `go 1.26.0`, `toolchain go1.27.4`); Echo v4, logrus, yaml.v3,
  golang-jwt v5, go-redis v9, mongo-driver v1, OpenTelemetry 1.47, gorilla/websocket (pinned to
  a commit that fixes GO-2026-6278), wazero.
- Lint: golangci-lint 2.15.0 with `.golangci.yml` (v2 format). Vulns: govulncheck 1.10.0.
- Runtime image: `golang:1.27.4-alpine3.24` build, `alpine:3.24.4` run, uid 65532.

## Commands (Docker first; the host has no Go)

```bash
scripts/docker-check.sh              # everything CI runs: mod verify, gofmt, vet, build,
                                     # race tests, golangci-lint, govulncheck
scripts/docker-check.sh test         # race tests      | lint | vuln | cover | build | fmt
scripts/docker-check.sh -- go test -run TestX ./test/unit/pkg/gateway/   # any command
scripts/docker-check.sh fix          # gofmt -w + go mod tidy (only fix/upgrade write files)
scripts/docker-check.sh upgrade      # go get -u ./... + tidy
scripts/quickstart.sh                # dev stack (Odin, Redis, MongoDB, demo backends) + admin login
docker compose down -v               # tear it down
```

Local fallback with Go installed: `make build`, `make test`, `make lint`. `GO_CI_CPUS` caps
cores (default half). The check image holds no project code and is shared across Go projects
(`go-ci:go<ver>-lint<ver>`, volumes `go-ci-*`).

## Map

| Path | What |
| --- | --- |
| `cmd/odin/main.go` | Entry point: flags (`-config`, `-version`, `-healthcheck`), signals, shutdown |
| `pkg/gateway/gateway.go` | Builds everything from `config.Config`: middleware order, auth, limiter, cache, health checkers, admin; `Handler()` for tests |
| `pkg/routing/` | `router.go` mounts a group per HTTP service (label, auth, rate limit, cache, handler); `handler.go` proxies (targets, retries, breakers, WebSocket, transforms, streaming) |
| `pkg/config/` | YAML schema, defaults, validation, `${NAME}` expansion (`env.go`) |
| `pkg/service/` | `service.Config`, the runtime form that `gateway.go` copies from `config.ServiceConfig` field by field |
| `pkg/admin/` | Admin UI and API; templates and static files are read from `pkg/admin/templates` and `pkg/admin/static` relative to the working directory |
| `pkg/plugins/` | Go plugin manager, static validation, uploads (GridFS) |
| `test/unit/pkg/gateway/` | End-to-end tests through the whole gateway with httptest backends |
| `docker/ci/`, `scripts/` | Check image and wrappers; `quickstart.sh` |
| `deployments/` | Docker prod compose, Kubernetes manifests, Helm chart (`deployments/helm/odin`) |

## Conventions

- Errors: wrap with `fmt.Errorf("...: %w", err)`; handlers return `echo.NewHTTPError(code, msg)`;
  log with the injected `*logrus.Logger` and fields.
- New per-service options: add to `config.ServiceConfig` (yaml tag, doc comment), copy into
  `service.Config` in `gateway.go`, use in `pkg/routing`, test in `test/unit/pkg/gateway`, document
  in `docs/configuration.md`. See the `service-feature` skill.
- Tests live in `test/unit/...` as separate packages and use testify; end-to-end behaviour is
  tested through `gateway.New` + `Handler()` (helpers `newBackend`, `newGateway`, `serve`,
  `loadConfig` in `test/unit/pkg/gateway`).
- Comments explain why, especially where they replace buggy behaviour; keep that style.
- Secure defaults: admin off, no default credentials, fail closed when a secret is missing.

## Commits

Conventional Commits (`feat:`, `fix:`, `docs:`, `ci:`, `build:`, `test:`, `refactor:`), one
logical change each, `scripts/docker-check.sh` green before committing, push after each commit to
`main`, never force-push. **No AI attribution**: no `Co-Authored-By`, no "Generated with" lines;
check with `git log -1 --format=%B`. Never commit `.env` or real secrets.

## Gotchas

- A YAML key that does not exist in the schema is silently ignored (AUDIT COR-24); check field
  names against `pkg/config/config.go`.
- `config.ServiceConfig` and `service.Config` are separate types: a new field must be copied in
  `gateway.go` or it does nothing.
- The admin writes the config file atomically in its directory; in the containers that file is
  read-only, so admin edits fail there (elsewhere they apply on restart).
- Middleware order matters: auth must run before the cache (AUDIT SEC-03).
- Many packages are not wired into the gateway (wasm, multicluster, openapi, ai, proxy, errors,
  debug, utils): AUDIT MNT-03. Don't document them as features.
- Go plugins need cgo; the Docker image is static and cannot load them.

## Don't

- Don't change config keys, admin API routes or CLI flags without a migration note (ROADMAP Next
  explains which changes need the owner's approval).
- Don't create release tags, enable GitHub Pages or push public images without the owner's
  go-ahead.
- Don't hand-edit `go.sum`; don't loosen `.golangci.yml` to get green.

## Skills

`.claude/skills/`: `docker-check` (build/test/lint in Docker), `service-feature` (add a
per-service option end to end), `roadmap-item` (work an AUDIT/ROADMAP item), `dependency-upgrade`,
`release`, `gateway-milestone` (advance the gateway roadmap toward APISIX/Kong parity).
