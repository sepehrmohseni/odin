# Odin codebase audit

Audit of the whole repository at commit `1a66ef4` (the state before this hardening pass). Every
location below is `file:line` **at `1a66ef4`**; see `git show 1a66ef4:<file>`. The Status column
says what has happened since: the commit that fixed it, or the [ROADMAP](../ROADMAP.md) section
that holds it.

Scope: all Go code under `cmd/`, `pkg/` and `test/`, the admin templates and JavaScript, configs,
Dockerfiles, Compose files, the Helm chart, CI workflows and docs. Not reviewed: generated or
vendored files (`go.sum`, `test/services/package-lock.json`), binary images under `docs/images/`,
and the Python demo service `deployments/grok/` beyond its Compose wiring.

## Baseline

| Check | Result at `1a66ef4` |
| --- | --- |
| `go build ./...`, `go vet ./...` | Pass |
| `go test -race ./...` | Pass (16 test packages; all tests live in `test/unit`) |
| Coverage of `./pkg/...` | **9.5 %** of statements (CI measured coverage per test package, which hid this) |
| golangci-lint | Could not run: `.golangci.yml` is v1 format and CI pins `version: latest` (v2) |
| govulncheck | **27 vulnerabilities reachable** from Odin's code, in 11 modules |
| gitleaks (full history) | No real secrets; 2 expired sample JWTs and documentation examples |

Severity: **critical** = exploitable remotely with no or default credentials; **high** = serious
security or correctness problem in a supported path; **medium** = real bug or risk with limited
reach; **low** = hygiene.

## Security

| ID | Sev | Location | Finding | Why it matters | Fix | Status |
| --- | --- | --- | --- | --- | --- | --- |
| SEC-01 | critical | `pkg/admin/auth.go:13`, `pkg/gateway/gateway.go:501` | The admin auth middleware returns `next(c)` when `admin.enabled` is false, and the admin routes are registered unconditionally. Every example config omits `admin`, so it is false. | Anyone can call every admin API: edit services, read settings, and (with MongoDB) upload and load plugins, i.e. run code. | Mount the admin only when enabled; no bypass in the middleware. | Fixed `c07b7e1` |
| SEC-02 | critical | `pkg/plugins/validation.go:116` | Upload validation calls `plugin.Open` (dlopen) before its ELF and "security" checks. | dlopen runs constructors and Go `init()` inside the gateway: uploading a file executes it. | Validate statically (ELF type, build info, dynamic symbols); load only on explicit enable, after a hash check. | Fixed `3be5e10` |
| SEC-03 | critical | `pkg/gateway/gateway.go:493`, `pkg/middleware/middleware.go:40` | The response cache is global middleware that runs before route middleware, keyed by method+path+query only. | The first authenticated response for a URL is served to anyone, with no token. Admin pages, `/metrics` and errors are cached as well. | Cache per service after authentication; never store requests with credentials or responses with cookies or `no-store`/`private`. | Fixed `bc34392` |
| SEC-04 | high | `pkg/admin/admin.go:89` | With no password configured, the admin accepts `admin`/`admin1`. | Known default credentials. | No built-in password: generate one per run and log it; support bcrypt `passwordHash` and env vars. | Fixed `c07b7e1` |
| SEC-05 | high | `pkg/admin/auth.go:77` | The login "session" cookie is `base64(user:pass)`. There is no SameSite, no Secure flag, no CSRF protection and no throttling, and credentials are compared with `==`. | The password sits in the cookie; CSRF and brute force are possible. | Random session tokens, SameSite=Strict, Origin checks, a lockout after 5 failures, constant-time compare, logout. | Fixed `c07b7e1` |
| SEC-06 | high | `pkg/admin/plugin_upload.go:70`, `pkg/plugins/upload.go:194`, `pkg/plugins/management.go:142` | Plugin name and version from the form are joined into file paths. | `../../..` overwrites (then deletes) any `.so` on the host. | Restrict names to `[A-Za-z0-9._+-]`; private temp directories and files. | Fixed `3be5e10` |
| SEC-07 | high | `pkg/admin/plugin_upload.go:140` | `buildDir` is `/tmp/odin-plugin-build/<name>` with a deferred `os.RemoveAll`. | The name `../..` removes `/`. | Validate the name; use `os.MkdirTemp`. | Fixed `3be5e10` |
| SEC-08 | high | `pkg/admin/plugin_handler.go:121`, `pkg/gateway/gateway.go:211` | The admin API stores an arbitrary `binaryPath`; the gateway dlopens it again on every boot. | Arbitrary code execution that persists across restarts through MongoDB. | Plugin manager loads only from allowed directories, with symlinks resolved. | Fixed `3be5e10` |
| SEC-09 | high | `pkg/admin/settings_api.go:32` (also `/json`, `/export`) | Settings endpoints return the whole config. | They leak the JWT secret, admin and MongoDB passwords, Redis URLs and the Consul token. | Redact secrets; keep stored values when the redacted form is posted back. | Fixed `c07b7e1` |
| SEC-10 | high | `pkg/auth/jwt.go:119`, `pkg/auth/jwt.go:152` | Without a secret it only prints a warning and verifies against an empty key. Tokens without `exp` are accepted. | Forged tokens; tokens that never expire. | Fail closed (503); require `exp`, HMAC only; optional issuer, audience and leeway. | Fixed `bc34392` |
| SEC-11 | high | `pkg/graphql/proxy.go:242`, `pkg/grpc/proxy.go:269` | GraphQL and gRPC services are registered without their auth middleware. | `authentication: true` is ignored for them. | Pass the service's auth middleware into `RegisterRoutes`. | Fixed `bc34392` |
| SEC-12 | high | `pkg/aggregator/aggregator.go:51`, `:46` | `/aggregate?services=` calls any service, including authenticated ones (`/aggregate` is in the example `ignorePathRegexes`). Fan-out is unbounded and the client has no timeout. | Auth bypass; request amplification. | Per-service auth check; at most 10 services; timeouts; size limits; escaped path values. | Fixed `bc34392` |
| SEC-13 | high | `pkg/servicemesh/consul.go:135` | The Consul ACL token is written into inbound request headers, which the proxy forwards. | Every upstream, including third parties, receives the token. | Never inject it; drop the header if a client sends it. | Fixed `bc34392` |
| SEC-14 | high | `pkg/plugins/management.go:196`, `pkg/plugins/manager.go:421` | Enabling an uploaded middleware plugin registers it in a chain that no request path runs. | An "enabled" auth plugin (for example `examples/middleware-plugins/api-key-auth`) enforces nothing: auth fails open. | Mount the middleware chain, or refuse to enable. Needs a design decision. | Open: [Next](../ROADMAP.md#next) |
| SEC-15 | high (if used) | `pkg/auth/oauth2.go:180` | The exported `OAuth2Middleware` accepts any `Bearer` value ("Here you would validate"). Unused today. | Anyone wiring it in gets no authentication. | Validate tokens (introspection or userinfo), or remove it. That is an API change. | Open: [Next](../ROADMAP.md#next) |
| SEC-16 | medium | `pkg/admin/services.go:59`, `pkg/admin/templates/monitoring.html:462`, `traces.html:329` | Service names and targets go into HTML via `Sprintf`; request paths (chosen by any client) go into `innerHTML`. | Stored XSS in the admin origin. | Escape everywhere; validate service input. | Fixed `c07b7e1` |
| SEC-17 | medium | `pkg/admin/monitoring.go:89` | The monitoring WebSocket accepts any Origin. | Cross-site WebSocket hijacking with the admin's cookie. | Same-origin check. | Fixed `c07b7e1` |
| SEC-18 | medium | `pkg/admin/routes.go:39`, `pkg/health/endpoints.go:100` | `/admin/debug/metrics` has no auth; `/debug/routes` and `/debug/config` are public. | They disclose traffic and routes. | Remove the admin one; gate debug endpoints behind `server.debugEndpoints`. | Fixed `c07b7e1`, `bc34392` |
| SEC-19 | medium | `pkg/gateway/gateway.go:49` (Echo default `IPExtractor`) | Echo trusts `X-Forwarded-For` from any client. | Clients choose their own IP for rate limits, throttling and logs. | Direct IP unless the request comes through `server.trustedProxies`. | Fixed `bc34392` |
| SEC-20 | medium | `pkg/admin/settings_api.go:475` | The backup name in the restore URL is not validated. | Reads or restores outside the backup directory. | Base names only. | Fixed `7b6ad1c` |
| SEC-21 | medium | `pkg/admin/admin.go:138` (and settings and backups) | Config files that contain secrets are written 0644. | Readable by other local users. | Write 0600, atomically. | Fixed `7b6ad1c`, `c07b7e1` |
| SEC-22 | medium | `pkg/admin/integration_handler.go:215` | The masked Postman key (`****abcd`) is round-tripped by the form and saved over the real key; keys shorter than 4 characters panic. The key is stored in plaintext. | The integration breaks after any edit; admin request panics. | Keep the stored key when a masked one comes back; safe masking. Encryption at rest is still open. | Fixed `c07b7e1`; encryption: [Later](../ROADMAP.md#later) |
| SEC-23 | medium | `go.mod:3` and module versions | 27 reachable vulnerabilities (echo, x/net, otel, grpc, mongo-driver, compress, gorilla/websocket and others) on an end-of-life Go 1.25. | Known CVEs in the request path. | Upgrade (Go 1.27.4 toolchain, all modules); govulncheck in CI. | Fixed `448f76e` |
| SEC-24 | medium | `pkg/ratelimit/limiter.go:5`, `pkg/canary/canary.go`, `pkg/cache/strategies.go`, `pkg/integrations/postman/sync.go` | MD5 is used for keys and hashes. The canary split used one MD5 byte, which skewed weights. | Weak primitive; wrong traffic split. | SHA-256 / FNV. | Fixed `7b6ad1c` |
| SEC-25 | medium | `config/default_config.yaml:32`, `:42`; `deployments/helm/odin/values.yaml:78`; `docker-compose.yml:61` | Shipped defaults: a placeholder JWT secret, a bcrypt hash of `admin`, Helm admin password `admin123` in a ConfigMap, MongoDB `admin/password`. | Known credentials in images and charts. | No credentials in defaults; secrets from env or Secrets; `.env.example`. | Hash removed `c07b7e1`; rest: [Now](../ROADMAP.md#now) |
| SEC-26 | medium | `pkg/integrations/postman/newman.go:177` | `NoInsecureSSL` ("don't allow insecure") adds `--insecure`. | Inverted TLS verification (latent: the admin does not set it). | Invert the flag. | Open: [Next](../ROADMAP.md#next) |
| SEC-27 | low | `test/test-api-gateway.sh:19`, `test/postman/api-gateway-postman-collection.json` | Sample JWTs are committed (expired June 2024). | Harmless unless signed with a real secret. | Rotate that secret if it was ever real; generate tokens at test time. | Reported |
| SEC-28 | low | `pkg/wasm/runtime.go:50` | No `WithCloseOnContextDone`, so plugin timeouts are not enforced (WASM is not wired in). | An endless guest loop blocks that plugin forever. | Enable close-on-done when WASM is wired. | Open: [Later](../ROADMAP.md#later) |

## Correctness

| ID | Sev | Location | Finding | Status |
| --- | --- | --- | --- | --- |
| COR-01 | high | `pkg/routing/handler.go:199`, `:188` | Retries resend an empty body (the first attempt consumed it), every method is retried (a timed-out POST can apply twice), and the sleep ignores cancellation. | Fixed `3688608` |
| COR-02 | high | `pkg/gateway/gateway.go:469`, `pkg/ratelimit/limiter.go:357`, `:368` | Rate limiting is never wired, only logged. Without Redis the limiter allows everything; windows under 1s divide by zero; the Redis TTL refreshes on every hit. | Fixed `90f0406` |
| COR-03 | high | `pkg/circuit/breaker.go:216` | The circuit breaker is unused; in half-open state a failed trial never re-opens it. | Fixed `90f0406` |
| COR-04 | high | `pkg/websocket/proxy.go:134` | WebSocket is not proxied at all (upgrades go through `http.Client`). The proxy package sets the read deadline once, so every connection dies after 60s, and it forwards no headers. | Fixed `e37ffbc` |
| COR-05 | medium | `pkg/gateway/gateway.go:101`, `pkg/canary/canary.go:50` | Canary and template transformations are never read from config; canary weight 100 routes nothing. | Fixed `e37ffbc` |
| COR-06 | medium | `pkg/routing/handler.go:134` | Aggregation dependencies are "not processed yet"; `transform:` rules are parsed and ignored; result mappings are applied twice. | Fixed `e37ffbc` |
| COR-07 | medium | `pkg/routing/handler.go:36`, `:65`, `:102`, `:177` | It follows upstream redirects, buffers every response (breaking SSE), copies hop-by-hop headers, never sends configured `headers`, loses `%2F`, produces double slashes, leaves a stale Content-Length after transforms, and sends no X-Forwarded-*. | Fixed `3688608` |
| COR-08 | medium | `pkg/gateway/gateway.go:436` | Health checks never affect routing; services share one checker that ignores their settings. | Fixed `3688608` |
| COR-09 | medium | `pkg/grpc/proxy.go:142`, `:91` | HTTP-to-gRPC transcoding cannot work: it passes `[]byte` to the proto codec, and only paths starting with `/grpc/` are accepted. | Open: [Next](../ROADMAP.md#next) |
| COR-10 | medium | `pkg/graphql/proxy.go:193`, `:145` | `__typename` counts as introspection (breaks Apollo clients when introspection is off). Client headers are not forwarded, the status is always 200, and query caching is not implemented. | Open: [Next](../ROADMAP.md#next) |
| COR-11 | high | `pkg/admin/templates.go:67`, `pkg/admin/plugin_handler.go:355` | All templates are parsed into one set, so pages show the last-defined `content` (Traces). The plugin pages call `c.Render` with no renderer registered (500). | Fixed `6a6dbec` |
| COR-12 | medium | `pkg/admin/static/js/api.js:6`, `pkg/admin/templates/integrations_postman.html:256` vs `pkg/admin/integration_handler.go:91` | The UI calls `OdinAPI.get/put` (not exported) and `/admin/api/integrations/...` (routes are `/admin/integrations/...`). The config structs have no JSON tags, so settings forms read `undefined`. | Open: [Next](../ROADMAP.md#next) |
| COR-13 | medium | `pkg/admin/monitoring.go:288`, `:340` | The broadcaster deletes from its client map under a read lock (fatal concurrent map write). The initial snapshot write can race a broadcast on the same connection. | Map fixed `7b6ad1c`; per-client writer: [Next](../ROADMAP.md#next) |
| COR-14 | high | `pkg/integrations/postman/sync.go:140`, `pkg/admin/integration_handler.go:240` | A sync interval of 0 panics in `NewTicker`; the setting loads at boot, so the gateway crash-loops. The loop dies with the request context; stopping and restarting panics; old engines leak. | Fixed `27e9a6a` |
| COR-15 | high | `pkg/integrations/postman/sync.go:312`, `:317` | "Export to Postman" overwrites the chosen collection with a placeholder service (no targets), destroying the user's collection. | Fixed: exports the configured service or refuses |
| COR-16 | medium | `pkg/mongodb/adapter.go:131` | Unchecked `int64` assertions on BSON int32 panic; `primitive.A` arrays are silently dropped. | Fixed `7b6ad1c` |
| COR-17 | medium | `cmd/migrate/main.go:22`, `pkg/mongodb/repository_ops.go` | `-mongodb-database` defaulted to `odin_gateway`, overriding config. String IDs vs ObjectID make Get/Update/Delete by ID never match (`--force` re-migration fails). | Flag fixed `7b6ad1c`; IDs: [Next](../ROADMAP.md#next) (stored data) |
| COR-18 | medium | `pkg/mongodb/repository.go:32` | Without `connectTimeout` the context is already expired, so MongoDB is silently disabled. A failed ping leaks the client. | Fixed `27e9a6a` |
| COR-19 | medium | `pkg/ai/collector.go:94`, `pkg/ai/detector.go:190`, `pkg/ai/repository.go:169` | Status keys came out as `"\x02xx"`; z-scores divide by σ=0 (NaN/Inf); the bot check is always true; the detector filters `service_name: ""`; the baseline never refreshes. The package is not wired in. | Status key fixed `7b6ad1c`; rest: [Later](../ROADMAP.md#later) |
| COR-20 | medium | `pkg/multicluster/manager.go:79`, `:493` | The per-cluster TLS client is built and dropped; `affinityMap` is unsynchronised; stale services are never removed. Not wired in. | TLS fixed `7b6ad1c`; rest: [Later](../ROADMAP.md#later) |
| COR-21 | medium | `pkg/plugins/manager.go:369`, `:421`; `pkg/plugins/repository.go:41` vs `pkg/plugins/upload.go:77` | Hook slices are mutated in place while requests iterate them. Duplicate loads survive unload. The lock is held across plugin code. `post-request`/`pre-response` hooks never fire. Plugin HTTP errors become 500. Two schemas share the `plugins` collection, so uploaded plugins are not restored on boot. | Open: [Next](../ROADMAP.md#next) |
| COR-22 | low | `pkg/wasm/middleware.go:129`, `:33` | `allowedUrls` never matched (shadowed variable); `service_id` assertion panics. Not wired in. | Fixed `7b6ad1c` |
| COR-23 | medium | `cmd/odin/main.go:141` | SIGHUP logs "Configuration reloaded successfully" and applies nothing. | Honest message `27e9a6a`; live reload: [Later](../ROADMAP.md#later) |
| COR-24 | low | `pkg/config/config.go:421` | `services.yaml` parse errors are ignored. The admin copies those services into `config.yaml` on every save (duplicates). Unknown YAML keys are silently ignored. | Fixed `c07b7e1`; unknown keys: [Next](../ROADMAP.md#next) |
| COR-25 | medium | `pkg/tracing/tracing.go:73` | A full URL is passed to `WithEndpoint` (host:port only), so the default config never exports. Trace context is not propagated upstream. | Fixed `448f76e`, `3688608` |
| COR-26 | low | `pkg/cache/store.go:121`, `:70` | The Redis store returns a different type than the middleware expects (never hits); `Clear` pattern never matches; local TTLs are ignored. | Fixed `bc34392` |
| COR-27 | low | `cmd/gateway/main.go:96`, `cmd/odin/main.go:118` | On a failed `Start` the process waits forever; shutdown errors are compared as strings. | Fixed `27e9a6a` |
| COR-28 | low | `pkg/openapi/openapi.go:380` | `ToYAML` returns JSON; base paths are picked in random map order; `bearerAuth` is undefined. Not wired in. | Ordering fixed `7b6ad1c`; rest: [Later](../ROADMAP.md#later) |
| COR-29 | low | `pkg/servicemesh/consul.go:98` (and Istio/Linkerd) | Service discovery is stubbed; the mTLS client is never used by the proxy. | Open: [Later](../ROADMAP.md#later) |

## Operations

| ID | Sev | Location | Finding | Status |
| --- | --- | --- | --- | --- |
| OPS-01 | medium | `pkg/gateway/gateway.go:515` | No `ReadHeaderTimeout` (slowloris), `IdleTimeout` or header limit. | Fixed `27e9a6a` |
| OPS-02 | medium | `pkg/gateway/gateway.go:525`, `pkg/health/alerts.go:180` | Shutdown stops background work before the server, never closes tracing (spans lost), MongoDB, gRPC or per-service checkers. Late alerts panic on the closed queue. | Fixed `27e9a6a` |
| OPS-03 | medium | `pkg/monitoring/prometheus.go:86` | The Prometheus `service` label is the first path segment, so a client can create unbounded label cardinality. | Fixed `27e9a6a` |
| OPS-04 | medium | `pkg/gateway/gateway.go` (no `Recover`, no `BodyLimit`) | A panic drops the connection; request bodies are unbounded. | Fixed `bc34392` |
| OPS-05 | high | `Dockerfile:22`, `:32` | Runs from `/root/` while config is mounted at `/app/config` (ignored); runs as root; unpinned `alpine:latest`; admin static assets missing; `default_config.yaml` uses a schema the code does not read. A second Dockerfile hard-codes amd64. | Open: [Now](../ROADMAP.md#now) |
| OPS-06 | high | `deployments/helm/odin/templates/service.yaml:4` | The chart does not render (`{ { include ... } }`, mangled by a formatter). Its config keys do not match the code. It exposes a non-existent admin port and declares unused Redis/Prometheus dependencies. | Open: [Now](../ROADMAP.md#now) |
| OPS-07 | medium | `.github/workflows/release.yml:33` | Pushes to GHCR without logging in; GoReleaser has no config; amd64 only. | Open: [Now](../ROADMAP.md#now) |
| OPS-08 | medium | `docker-compose.yml` | No healthcheck-gated `depends_on`, unpinned images, mounts a `config/` with no `config.yaml`, no `.env.example`. | Open: [Now](../ROADMAP.md#now) |

## Tests

| ID | Sev | Location | Finding | Status |
| --- | --- | --- | --- | --- |
| TST-01 | high | `.github/workflows/ci.yml` | Coverage was measured per test package, and all tests sit in `test/unit`, so `pkg/` coverage (9.5 %) was invisible; "80 %+" in the roadmap is not true. | CI measures `-coverpkg=./pkg/...` `7b6ad1c`; raising it: [Next](../ROADMAP.md#next) |
| TST-02 | medium | `test/unit/pkg/ratelimit/limiter_test.go:142` | A test asserted the bug ("Changed expectation to match implementation"). | Fixed `90f0406` |
| TST-03 | medium | `test/unit/pkg/plugins/validation_test.go:237` and `upload_test.go` | Plugin tests re-implement the logic locally instead of importing `pkg/plugins`. | Real tests added `3be5e10`; old ones: [Next](../ROADMAP.md#next) |
| TST-04 | medium | none | No test exercised the request path end to end (auth, cache, proxy, limits). | `test/unit/pkg/gateway` added across `bc34392`..`27e9a6a` |
| TST-05 | low | `test/integration/` | README only; no integration tests. | Open: [Next](../ROADMAP.md#next) |

## Maintainability

| ID | Sev | Location | Finding | Status |
| --- | --- | --- | --- | --- |
| MNT-01 | medium | `.golangci.yml:6`, `.github/workflows/ci.yml:65` | v1 lint config against `version: latest` (v2); four files not gofmt'd; about 150 lint findings. | Fixed `7b6ad1c` (0 findings) |
| MNT-02 | medium | `go.mod:3`, workflows | End-of-life Go 1.25; unpinned tools; duplicate `build.yml`. | Fixed `7b6ad1c`, `448f76e` |
| MNT-03 | medium | `pkg/proxy`, `pkg/errors`, `pkg/debug`, `pkg/utils`, `pkg/wasm`, `pkg/multicluster`, `pkg/openapi`, `pkg/ai`, `pkg/service/mongodb_loader.go`, admin `middleware_api.go`/`mongodb_api.go`/`ai_api.go` | Packages and admin APIs that nothing calls, while docs list them as working features. | Open: [Next](../ROADMAP.md#next) (wire or remove: rule 7) |
| MNT-04 | low | `.gitignore:14` | A bare `odin` pattern ignored `cmd/odin/` and `deployments/helm/odin/`. | Fixed `7b6ad1c` |
| MNT-05 | low | `pkg/gateway/gateway.go` | `config.ServiceConfig` is copied field by field into `service.Config`, so every new option needs two edits. | Open: [Later](../ROADMAP.md#later) |
| MNT-06 | low | `go.mod` | Echo v4 (the OpenTelemetry middleware's successor needs v5) and mongo-driver v1 (v2 exists): major upgrades. | Open: [Later](../ROADMAP.md#later) |

## Documentation

| ID | Sev | Location | Finding | Status |
| --- | --- | --- | --- | --- |
| DOC-01 | medium | `README.md:154`, `:161` | It claims weighted load balancing, role-based access control, OAuth2 and API-key auth, and distributed rate limiting. None existed in the request path. | Open: [Now](../ROADMAP.md#now) |
| DOC-02 | medium | `README.md:283` | It links `docs/IMPLEMENTATION_PLAN.md`, `IMPLEMENTATION_CHECKLIST.md`, `VISUAL_ROADMAP.md`, `EXECUTIVE_SUMMARY.md` and `security.md`, which do not exist. | Open: [Now](../ROADMAP.md#now) |
| DOC-03 | low | `README.md` badges | They point to `sepehr-mohseni/odin` (a redirect); the repository is `sepehrmohseni/odin`. | Open: [Now](../ROADMAP.md#now) |
| DOC-04 | low | `ROADMAP.md` | Lists unwired packages (circuit breaker, WebSocket, WASM, multi-cluster, AI, OpenAPI) as completed. | Open: [Now](../ROADMAP.md#now) |
| DOC-05 | low | `docs/GOAL-*.md`, `*_SUMMARY.md` | About 20 status reports written as docs, several contradicting the code. | Open: [Later](../ROADMAP.md#later) |
