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
- Services from MongoDB (`mongodb.loadServices`): the services `odin-migrate` stores are served
  next to the config file's (which win on a name clash), and `/admin/api/mongodb/services`
  creates, lists, changes and removes them (applied on restart); the dashboard marks them. The
  unused `pkg/service/mongodb_loader.go`, which opened a second connection, is removed (MNT-03).
- Multi-cluster failover (`multiCluster`, `services[].clusters`): a service can fail over to, or
  (with `failoverStrategy: active`) share traffic with, the same routes on other gateways, which
  are health-checked and can use their own TLS settings; `X-Odin-Cluster-Hop` stops requests
  bouncing between clusters. `pkg/multicluster` is rewritten (COR-20): it polled a
  `/api/services` endpoint that does not exist and wrote its affinity map under a read lock.
  Removed keys, which never had an effect: `syncInterval`, `loadBalancing`, `affinityEnabled`,
  `affinityTTL`, and per cluster `zone`, `weight` and `metadata`.
- Traffic anomaly detection (`ai`): per-service baselines of request rate, server error share
  and latency, z-score detection with floors and minimum counts, ongoing anomalies that resolve
  themselves, webhook alerts (`alertMinSeverity` is new), optional explanations from an analysis
  service such as `deployments/grok`, MongoDB storage with retention, and an Anomalies page in the
  admin. `pkg/ai` is rewritten (COR-19): the old detector divided by zero deviations, flagged
  every user agent as a bot, analysed no service, and computed latency percentiles from request
  sizes. `ai.flushInterval`, which no longer applies, is removed.
- WebAssembly plugins (`wasm`): sandboxed middleware in any language that compiles to wasm32, in
  the same phases, priorities and routes as Go plugins and working in the Docker image. A small
  JSON ABI (`odin_alloc`, `odin_handle`) lets a plugin answer the request or change its headers;
  each request is interrupted at the plugin's timeout (SEC-28), instances are pooled
  (`maxInstances`) and memory-capped (`maxMemoryPages`), and a plugin that fails to load stops
  startup. Example: `examples/wasm-plugins/api-key`. Config: `phase`, `routes`, `readBody` and
  `failOpen` are new; `allowedUrls` and `cacheEnabled`, which never had an effect, are removed;
  `type` is deprecated (`auth` means `phase: pre-auth`).
- `openapi`: an OpenAPI 3 document of the service routes at `/openapi.json` and `/openapi.yaml`,
  and Swagger UI at `openapi.uiPath`. The document describes only what the gateway knows (paths,
  methods, JWT requirement, rate-limit and gateway error responses), never upstream addresses;
  `autoGenerate: false` serves a hand-written document instead. `openapi.autoGenerate` defaults
  to true (MNT-03).
- A redesigned admin: a sidebar layout with light and dark themes, a dashboard of live traffic
  and per-service routes, targets, policies and health, a monitoring page with a five-minute
  chart, latency percentiles and target health, and a filterable request log with curl commands.
  Bootstrap, Bootstrap Icons and htmx are pinned and checked with Subresource Integrity (Chart.js,
  loaded unpinned, and an unused date library are gone).
- A demo: `scripts/quickstart.sh --demo` (admin / admin, a traffic generator, and an unreliable
  inventory service that shows timeouts, health checks and the circuit breaker), a GitHub
  Codespaces configuration that starts it, and a demo page on the documentation site with
  screenshots and the quickstart's generated OpenAPI document. The quickstart stack now serves
  its OpenAPI document and health-checks every service, and re-running the quickstart applies
  edits to `config/compose.yaml`.
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
- The admin's Middleware page works: its API was never mounted. Phase changes now apply to the
  running chain (they only changed the database record) and accept only the pipeline's phases;
  the API works without MongoDB. The unimplemented "compile" endpoint and the bulk
  enable/disable endpoints, which only flipped database flags, are removed (MNT-03).
- Postman's Newman test runner works (SEC-26): it passed "-" as the collection, which Newman
  does not read, looked for the JSON report on standard output instead of its file, added
  `--insecure` when told not to allow insecure TLS (the option is now `Insecure`), and put
  environment values on the command line. The collection, environment and report now go to a
  private temporary directory, and a run has an overall time limit. The stub
  `GET /admin/api/integrations/postman/test/results`, which always answered an empty list, is removed.
- Unknown configuration keys are logged as warnings with their file and line; they were ignored
  silently, so a misspelt key did nothing without notice (COR-24). The example configs that
  used keys which do not exist are corrected, and two examples of a `routes:` file format that
  nothing reads are removed.
- The admin's monitoring WebSocket gives each client its own writer: the first snapshot could
  race a broadcast on the same connection, and one slow client held up all others (COR-13).
- GraphQL proxying (COR-10): `__typename` is no longer blocked as introspection (it broke Apollo
  clients); client headers, `Authorization` included, reach the server; the server's status,
  headers and body come back unchanged instead of a re-encoded 200 (unreachable is 502, timeout
  504); and `enableQueryCaching`, which did nothing, caches anonymous queries.
- The admin's Postman page works: it called `/admin/api/integrations/postman/...` while the API
  was mounted at `/admin/integrations/postman/...`. The API moves under `/admin/api/`, like the
  admin's other JSON APIs (COR-12).
- The admin's Settings page shows and saves the real settings: the API returned raw structs
  (`{"Port": 8080, "Timeout": 30000000000}`) and the page called helpers that did not exist, so
  every field was empty (COR-12, in part). Masked secrets are kept on save.
- MongoDB records can be read, updated and deleted by ID: MongoDB stores generated IDs as
  ObjectIDs, the driver returned them as hex strings, and querying with the string matched
  nothing; updates also tried to change `_id`. This affected stored services, alerts, clusters,
  plugins, users and API keys (COR-17).
- Admin monitoring counts rejected requests with their real status (every 401, 404, 429 or 502
  used to be recorded as a 200), leaves out the admin's own pages and assets, measures the
  request rate over the last minute (it was the total divided by 60), and reports per-service
  traffic, a five-minute timeline and the health of checked targets (`/admin/api/monitoring/metrics`).
- The admin dashboard lists the configured services; it always said "No services configured".
  Its Edit links lead to the edit page instead of a missing `/edit` route.
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
