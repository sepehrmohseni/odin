# Changelog

All notable changes to Odin. The format follows [Keep a Changelog](https://keepachangelog.com/),
and versions follow [Semantic Versioning](https://semver.org/). Finding IDs refer to
[docs/AUDIT.md](docs/AUDIT.md).

## [Unreleased]

### Security

- The admin interface is only served when `admin.enabled` is true; it used to be served with
  authentication switched off (SEC-01). No built-in password (was `admin`/`admin1`); random
  session tokens, CSRF protection, login throttling, bcrypt `admin.passwordHash` (SEC-04, SEC-05).
- Plugin uploads are validated without executing them; plugin names and load paths are
  confined (SEC-02, SEC-06..SEC-08).
- The response cache runs after authentication and never stores credentialed or private
  responses (SEC-03).
- JWT fails closed without a secret and requires `exp`; GraphQL, gRPC and `/aggregate` enforce
  authentication; the Consul token is no longer forwarded upstream (SEC-10..SEC-13).
- Settings APIs mask secrets; config files are written 0600; admin XSS sinks escaped
  (SEC-09, SEC-16, SEC-21).
- 27 known vulnerabilities in dependencies fixed; Go toolchain 1.27.4 (SEC-23).

### Added

- Rate limiting and circuit breakers on service routes (`rateLimit`, `services[].rateLimit`,
  `services[].circuitBreaker`), in memory or Redis.
- WebSocket proxying; canary routing, template transformations, field-mapping rules and
  aggregation enrichment read from the config.
- `${NAME}` / `${NAME:-default}` environment references in config files.
- `server.maxBodySize`, `server.trustedProxies`, `server.debugEndpoints`,
  `server.readHeaderTimeout`, `server.idleTimeout`, `services[].cache`,
  `services[].healthCheck.path`, `auth.issuer`, `auth.audience`, `auth.leeway`,
  `rateLimit.algorithm`, `rateLimit.failClosed`.
- `odin -version` and `odin -healthcheck`.
- Production Dockerfile (multi-arch, non-root, health check), Compose development stack with
  `.env.example`, `scripts/quickstart.sh` first-run setup, working Helm chart (0.2.0), release
  pipeline for binaries and signed images, Docker-based checks (`scripts/docker-check.sh`).
- `docs/getting-started.md`, `docs/AUDIT.md`, `CLAUDE.md` and agent skills.

### Changed (check these when upgrading)

- `admin.enabled: false` (the default) now means no `/admin` routes at all.
- An admin without a configured password gets a random one, printed once in the log.
- `/debug/routes`, `/debug/config`, `/debug/content-types` need `server.debugEndpoints: true`;
  `/admin/debug/metrics` is gone.
- Services with `authentication: true` answer 503 when no JWT secret is configured, and tokens
  without `exp` are rejected.
- Retries apply only to GET, HEAD and OPTIONS, or requests with an `Idempotency-Key` header.
- Upstream redirects are passed to the client instead of being followed. `services[].timeout`
  bounds the wait for response headers, not the whole response, so streams are not cut off.
- Invalid targets (no scheme or host) and unparsable `services.yaml` files are startup errors.
- The client IP comes from the connection unless the request comes through
  `server.trustedProxies`.
- The Prometheus `service` label is the service name (or `gateway`) instead of the first path
  segment.
- Rate limits are enforced. Before, without Redis, every request was allowed.
- SIGHUP validates the config and says a restart is needed, instead of claiming a reload.
- Helm chart 0.2.0: `config.admin.password` became `secrets.adminPassword`; the admin port and
  the Redis/Prometheus dependencies were removed.

### Fixed

- Retries resent empty bodies; responses were fully buffered; hop-by-hop headers were forwarded;
  configured `headers` were never sent; `%2F` was decoded (COR-01, COR-07).
- Health checks now steer traffic (COR-08); every admin page rendered the traces page (COR-11);
  Postman export overwrote collections (COR-15); a zero Postman sync interval crash-looped the
  gateway (COR-14); MongoDB was disabled without `connectTimeout` (COR-18); OTLP endpoint URLs
  were rejected (COR-25); shutdown order and leaks (OPS-02); unbounded metric labels (OPS-03).
