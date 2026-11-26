# Odin API Gateway roadmap

What is planned, in order. Items link to their finding in [docs/AUDIT.md](docs/AUDIT.md); the
audit's Status column records what has been fixed and in which commit.

## Now

Critical and high items that are contained enough to finish in the current hardening pass.

- [x] Admin API secure by default: not mounted unless enabled, no default password, real sessions,
      CSRF protection, secrets redacted ([SEC-01](docs/AUDIT.md#security), SEC-04, SEC-05,
      SEC-09, SEC-16, SEC-17, SEC-18)
- [x] Plugin uploads validated without being executed; plugin paths confined
      ([SEC-02](docs/AUDIT.md#security), SEC-06, SEC-07, SEC-08)
- [x] Response cache after authentication; JWT fails closed; GraphQL/gRPC/aggregate auth;
      Consul token not forwarded ([SEC-03](docs/AUDIT.md#security), SEC-10..SEC-13, SEC-19)
- [x] Correct proxying: retries, streaming, forwarded and hop-by-hop headers, paths, health-aware
      target selection ([COR-01](docs/AUDIT.md#correctness), COR-07, COR-08)
- [x] Rate limiting and circuit breakers on the request path ([COR-02](docs/AUDIT.md#correctness),
      COR-03)
- [x] Canary, transformations, WebSocket and aggregation wired from config
      ([COR-04](docs/AUDIT.md#correctness)..COR-06)
- [x] Lifecycle, timeouts and bounded metrics ([OPS-01](docs/AUDIT.md#operations)..OPS-04,
      COR-14, COR-18)
- [x] Toolchain and dependencies current, lint and vulnerability checks in CI and in Docker
      ([SEC-23](docs/AUDIT.md#security), MNT-01, MNT-02, TST-01)
- [x] Production Docker image, Compose dev stack with `.env.example`, no credentials in shipped
      defaults ([OPS-05](docs/AUDIT.md#operations), OPS-08, SEC-25)
- [x] Helm chart that renders, matches the config schema and keeps secrets in Secrets
      ([OPS-06](docs/AUDIT.md#operations))
- [x] Middleware plugins run on the request path in ordered phases (pre-auth, post-auth,
      pre-proxy) with priorities and routes; plugin manager safe under concurrent changes
      ([SEC-14](docs/AUDIT.md#security), COR-21)
- [x] Every package wired in or removed: error handler, OpenAPI document, WebAssembly plugins,
      traffic anomaly detection, multi-cluster failover, services from MongoDB, and the admin
      APIs for them ([MNT-03](docs/AUDIT.md#maintainability), SEC-28, COR-19, COR-20)
- [x] Release workflow that can publish multi-arch images and binaries
      ([OPS-07](docs/AUDIT.md#operations)) — the first tag and public image wait for the owner
- [x] Admin pages render their own content; plugin pages render at all
      ([COR-11](docs/AUDIT.md#correctness))
- [x] "Export to Postman" exports the configured service, or refuses, instead of overwriting a collection with a placeholder
      ([COR-15](docs/AUDIT.md#correctness))
- [x] First-run setup: one-command quickstart script and a setup guide (Docker, Compose, binary,
      Helm) that ends at the admin login
- [x] README and docs describe only what works; broken links fixed
      ([DOC-01](docs/AUDIT.md#documentation)..DOC-04)

## Next

Medium items, and changes that alter behaviour or stored data and so need the owner's decision
first. Effort: S = hours, M = a day or two, L = a week or more.

| Item | Finding | Effort | Why it waits |
| --- | --- | --- | --- |
| `OAuth2Middleware`: validate tokens (introspection/OIDC) or remove it | [SEC-15](docs/AUDIT.md#security) | M | Exported API; removing it is a breaking change |
| gRPC transcoding through server reflection and `dynamicpb` | [COR-09](docs/AUDIT.md#correctness) | L | New design; current behaviour never worked |
| GraphQL: allow `__typename`, forward headers, keep upstream status, implement or drop query caching | [COR-10](docs/AUDIT.md#correctness) | S | Changes what clients receive |
| Admin UI talks to the real API (JSON tags on config, `OdinAPI` methods, Postman routes) | [COR-12](docs/AUDIT.md#correctness) | M | Adds JSON field names to the config API |
| One writer goroutine per monitoring WebSocket client | [COR-13](docs/AUDIT.md#correctness) | S | — |
| Warn about unknown config keys (strict decoding with fallback) | [COR-24](docs/AUDIT.md#correctness) | S | Noisy for existing configs at first |
| Newman runner: temp file, JSON reporter, correct TLS flag, timeouts | [SEC-26](docs/AUDIT.md#security) | S | — |
| Raise `pkg/` coverage (now measured; was 9.5 %) and replace tests that re-implement code | [TST-01](docs/AUDIT.md#tests), TST-03, TST-05 | M | — |

## Later

- Live configuration reload without dropping connections ([COR-23](docs/AUDIT.md#correctness))
- Real service discovery for Consul, Istio and Linkerd, and upstream mTLS
  ([COR-29](docs/AUDIT.md#correctness))
- OpenAPI generation that is valid OpenAPI ([COR-28](docs/AUDIT.md#correctness))
- Encrypt integration credentials at rest ([SEC-22](docs/AUDIT.md#security))
- One service config type instead of field-by-field copies ([MNT-05](docs/AUDIT.md#maintainability))
- Major upgrades: Echo v5, mongo-driver v2 ([MNT-06](docs/AUDIT.md#maintainability))
- Fold the `docs/GOAL-*` status reports into real documentation ([DOC-05](docs/AUDIT.md#documentation))
- Gateway features that do not exist yet: weighted, least-connections and consistent-hash load
  balancing; key-auth, basic auth, CORS and IP allow/deny plugins; request IDs; TLS termination
  with SNI; Kubernetes Gateway API support; a docs site

## What works today

Verified by the tests in `test/unit/pkg/gateway`, which drive the whole gateway against real
HTTP backends:

- Path-based routing to HTTP services, round-robin and random load balancing, per-service
  timeouts, idempotent-only retries with failover, active health checks that steer traffic
- JWT authentication per service (HMAC, `exp` required, optional issuer and audience)
- Rate limiting per service and client (in memory or Redis; fixed window, sliding window, token
  bucket), per-target circuit breakers
- Response caching (memory or Redis) that respects authentication and Cache-Control
- Canary routing by header, cookie or weight; template and field-mapping transformations;
  response enrichment from aggregation dependencies; `/aggregate`
- WebSocket and server-sent-event proxying; GraphQL proxying with depth and introspection limits
- Prometheus metrics, OpenTelemetry tracing with context propagation, structured access logs
- Admin web interface and API (opt-in, authenticated), Go plugin upload with static validation
- MongoDB storage for plugins and Postman integration settings
