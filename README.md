<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo.svg">
  <img src="docs/assets/logo-dark.svg" alt="Odin" width="140">
</picture>

# Odin API Gateway

[![CI](https://github.com/sepehrmohseni/odin/actions/workflows/ci.yml/badge.svg)](https://github.com/sepehrmohseni/odin/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go)](go.mod)
[![Docs](https://img.shields.io/badge/docs-sepehrmohseni.github.io%2Fodin-0b6e7a)](https://sepehrmohseni.github.io/odin/)
[![Release](https://img.shields.io/github/v/release/sepehrmohseni/odin)](https://github.com/sepehrmohseni/odin/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
<br>
[![Release downloads](https://img.shields.io/github/downloads/sepehrmohseni/odin/total?label=release%20downloads&color=0b6e7a)](https://github.com/sepehrmohseni/odin/releases)
[![Image pulls](https://img.shields.io/endpoint?url=https%3A%2F%2Fsepehrmohseni.github.io%2Fodin%2Ftraffic%2Fimage-pulls.json)](https://github.com/sepehrmohseni/odin/pkgs/container/odin)
[![Clones](https://img.shields.io/endpoint?url=https%3A%2F%2Fsepehrmohseni.github.io%2Fodin%2Ftraffic%2Fclones.json)](https://github.com/sepehrmohseni/odin/graphs/traffic)
[![Views](https://img.shields.io/endpoint?url=https%3A%2F%2Fsepehrmohseni.github.io%2Fodin%2Ftraffic%2Fviews.json)](https://github.com/sepehrmohseni/odin/graphs/traffic)

An API gateway written in Go: routing, authentication, rate limiting, caching, resilience and
observability for HTTP, WebSocket and GraphQL services, with a web admin interface.

</div>

## Try it

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/sepehrmohseni/odin?quickstart=1)

The codespace runs the whole stack in demo mode, with generated traffic: open port 8080, go to
`/admin` and sign in with admin / admin. Screenshots and the demo's generated API documentation
are on the [demo page](https://sepehrmohseni.github.io/odin/demo/).

## Quick start

With Docker (Compose v2):

```bash
git clone https://github.com/sepehrmohseni/odin.git && cd odin
scripts/quickstart.sh
```

This creates `.env` with random secrets, starts Odin with Redis, MongoDB and four demo backends,
waits until everything is healthy and prints the admin login. Then:

```bash
curl http://localhost:8080/api/products       # proxied to a demo backend
curl -i http://localhost:8080/api/orders      # 401: needs a JWT
open http://localhost:8080/admin              # admin interface (login printed by the script)
scripts/quickstart.sh --down                  # stop
```

`scripts/quickstart.sh --demo` does the same with admin / admin and generated traffic, for a
demo only.

The [documentation site](https://sepehrmohseni.github.io/odin/) covers this and the other ways to install
(plain Docker image, binary, Helm), your first routes and the admin interface.

## Features

What Odin does today. Each item is covered by the end-to-end tests in
[test/unit/pkg/gateway](test/unit/pkg/gateway):

- **Routing and load balancing**: path-prefix services, round-robin or random targets,
  optional base-path stripping, per-service static headers, `X-Forwarded-*` headers.
- **Resilience**: upstream timeouts, retries for idempotent requests with failover to another
  target, active health checks that take unhealthy targets out of rotation, per-target circuit
  breakers, and failover to (or traffic sharing with) other clusters.
- **Security**: JWT authentication per service (HMAC, expiry required, optional issuer and
  audience), request body limits, client IPs trusted from configured proxies only.
- **Traffic control**: rate limits per service and client (user or IP), in memory or shared
  through Redis (fixed window, sliding window, token bucket); canary routing by header, cookie
  or weight.
- **Transformation**: Go-template rewriting of headers, query and bodies; JSON field mappings;
  enrichment of responses with data from dependent services; `/aggregate`.
- **Caching**: response cache in memory or Redis that respects authentication, cookies and
  `Cache-Control`.
- **Protocols**: HTTP/1.1, WebSocket and server-sent events (streamed), GraphQL with depth and
  introspection limits.
- **Observability**: Prometheus metrics per service, OpenTelemetry traces with context
  propagation, structured access logs, `/health`, `/ready` and `/live`; traffic anomaly
  detection per service (rate, server errors, latency) with webhook alerts.
- **Plugins**: Go and WebAssembly middleware plugins in three phases of each service's pipeline
  (before authentication, after it, before proxying), ordered by priority and limited to routes;
  WebAssembly plugins are sandboxed, memory-capped and time-limited. Hook plugins around every
  request.
- **API description**: a generated OpenAPI 3 document of the routes, or your own, with
  Swagger UI.
- **Admin interface** (opt-in): dashboard, services, monitoring, traces and settings, behind
  sessions with CSRF protection and login throttling; Go plugin upload with static validation;
  Postman collection sync (with MongoDB).

Planned and known gaps (gRPC transcoding, OAuth2/OIDC, live reload, more
load-balancing algorithms and others) are listed in [ROADMAP.md](ROADMAP.md),
and the findings behind them in [docs/AUDIT.md](docs/AUDIT.md).

## Configuration

Odin reads one YAML file (`-config`, default `config/config.yaml`) and, next to it, an optional
`services.yaml`. Values written `${NAME}` or `${NAME:-default}` come from the environment.
Start from [config/config.example.yaml](config/config.example.yaml) or
[config/compose.yaml](config/compose.yaml); every option is described in
[docs/configuration.md](docs/configuration.md).

Environment variables used by the Docker setup ([.env.example](.env.example)):

| Variable | Used for | Default |
| --- | --- | --- |
| `ODIN_PORT` | Host port of the gateway (Compose) | `8080` |
| `ODIN_JWT_SECRET` | Signing secret for services with `authentication: true` | required |
| `ODIN_ADMIN_USERNAME` | Admin login | `admin` |
| `ODIN_ADMIN_PASSWORD` | Admin password (or `admin.passwordHash`, bcrypt) | required in Compose; generated and logged otherwise |
| `ODIN_ADMIN_ENABLED` | Turns the admin on with the image's built-in config | `false` |
| `MONGO_ROOT_USERNAME`, `MONGO_ROOT_PASSWORD` | MongoDB account (Compose) | `odin`, required |
| `LOG_LEVEL` | `debug`, `info`, `warn` or `error` | `info` |
| `GATEWAY_PORT` | Overrides `server.port` | from config |
| `GRAFANA_ADMIN_PASSWORD` | Grafana (`--profile observability`) | — |

## Development

Everything runs in Docker; no local Go toolchain is needed:

```bash
scripts/docker-check.sh            # what CI runs: mod verify, gofmt, vet, build, race tests,
                                   # golangci-lint, govulncheck
scripts/docker-check.sh test       # tests only;  ... lint, vuln, cover, shell, -- <command>
scripts/docker-check.sh fix        # gofmt -w and go mod tidy (writes to the checkout)
docker compose up -d --build --wait && docker compose down -v   # the dev stack
```

The check image ([docker/ci](docker/ci)) pins Go, golangci-lint and govulncheck and holds no
project code, so other Go projects can use it too.

With Go 1.26+ installed locally: `make build`, `make test`, `make lint`.

## Project structure

```
cmd/odin/            gateway entry point          cmd/migrate/   YAML -> MongoDB service import
pkg/gateway/         wiring: builds the server, middleware and routes from the config
pkg/routing/         per-service proxy handler (targets, retries, streaming, transforms)
pkg/auth/ ratelimit/ circuit/ cache/ canary/ transform/ aggregator/ websocket/ graphql/
pkg/health/ monitoring/ tracing/   health checks and alerts, Prometheus, OpenTelemetry
pkg/admin/           admin web interface and API (templates/ and static/ are served from disk)
pkg/plugins/         Go plugin loading, validation and upload
pkg/wasm/ ai/ multicluster/  WebAssembly plugins, anomaly detection, multi-cluster failover
pkg/config/          configuration schema and loading
test/unit/           tests (test/unit/pkg/gateway drives the whole gateway)
docker/ci/ scripts/  check image and wrappers;  deployments/  Docker, Kubernetes, Helm
```


## Deployment

- Docker: [Dockerfile](Dockerfile) (multi-arch, non-root, health check),
  [deployments/docker](deployments/docker)
- Kubernetes: [Helm chart](deployments/helm/odin), [plain manifests](deployments/kubernetes)
- Releases: tagging `vX.Y.Z` publishes binaries and a signed multi-arch image
  ([release.yml](.github/workflows/release.yml))

## Documentation

[Getting started](docs/getting-started.md) ·
[Configuration](docs/configuration.md) ·
[Authentication](docs/auth.md) ·
[Canary deployments](docs/canary-deployments.md) ·
[Transformations](docs/transformations.md) ·
[Aggregation](docs/aggregation.md) ·
[Monitoring](docs/monitoring.md) ·
[Plugins](docs/plugin-development-guide.md) ·
[Roadmap](ROADMAP.md) ·
[Audit](docs/AUDIT.md) ·
[Changelog](CHANGELOG.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Run `scripts/docker-check.sh` before opening a pull
request; commits follow [Conventional Commits](https://www.conventionalcommits.org/).

## License

[MIT](LICENSE)
