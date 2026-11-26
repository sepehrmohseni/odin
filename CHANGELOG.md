# Changelog

All notable changes to Odin. The format follows [Keep a Changelog](https://keepachangelog.com/),
and versions follow [Semantic Versioning](https://semver.org/). Finding IDs refer to
[docs/AUDIT.md](docs/AUDIT.md).

## [Unreleased]

### Added

- Middleware plugins run on service routes, in three phases (`pre-auth`, `post-auth`,
  `pre-proxy`) ordered by priority and limited to their routes; enabling or disabling one takes
  effect on the next request. Programs embedding the gateway can register compiled-in plugins
  with `Gateway.Plugins()` (SEC-14).
- `openapi`: an OpenAPI 3 document of the service routes at `/openapi.json` and `/openapi.yaml`,
  and Swagger UI at `openapi.uiPath`. The document describes only what the gateway knows (paths,
  methods, JWT requirement, rate-limit and gateway error responses), never upstream addresses;
  `autoGenerate: false` serves a hand-written document instead. `openapi.autoGenerate` defaults
  to true (MNT-03).
- `odin -openapi json|yaml` prints the OpenAPI document for a configuration without starting
  the gateway.
- `admin.allowedOrigins` (and `ODIN_ADMIN_ALLOWED_ORIGINS`): further origins the admin accepts
  logins, form posts and the monitoring WebSocket from, for proxies that rewrite `Host` such as
  GitHub Codespaces port forwarding.

### Fixed

- Plugin manager: the chain and hook lists are copy-on-write, duplicate names are refused,
  plugin code runs outside the lock and a panicking plugin fails only its request; the
  `post-request` and `pre-response` hooks fire; an `*echo.HTTPError` from a plugin keeps its
  status (COR-21).
- Enabled uploaded plugins are activated again at startup.
- Gateway errors go through one handler: the `{"message": ...}` body is unchanged, a deadline
  is a 504 instead of a 500, errors wrapping `pkg/errors` sentinels get their status, and
  server errors are logged with the request (MNT-03).

### Changed

- Uploaded plugins are stored in the MongoDB collection `plugin_binaries`. Existing upload
  documents (those with a GridFS `file_id`) are moved out of `plugins` at startup; plugin records
  created through the API stay in `plugins`.
- Phase names are checked: `pre-auth`, `post-auth` (the default) or `pre-proxy`. The names used
  before (`pre-routing`, `post-routing`, `pre-route`, `post-route`, `pre-response`) are mapped to
  these; any other name is rejected.
- Upload validation accepts plugins that export `New` or `Middleware`.

### Removed

- `pkg/proxy`, `pkg/debug` and `pkg/utils`, which nothing used: the proxy is `pkg/routing`, the
  debug endpoints are in `pkg/health` (behind `server.debugEndpoints`) (MNT-03).

## [0.1.0] - 2026-11-26

First release: the hardening pass described in docs/AUDIT.md.

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

[Unreleased]: https://github.com/sepehrmohseni/odin/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/sepehrmohseni/odin/releases/tag/v0.1.0
