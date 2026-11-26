# Observability

## Metrics

With `monitoring.enabled`, Prometheus metrics are served at `monitoring.path` (default
`/metrics`):

| Metric | Labels | What |
| --- | --- | --- |
| `api_gateway_requests_total` | `service`, `method`, `status` | Requests handled |
| `api_gateway_request_duration_seconds` | `service`, `method` | Latency histogram |
| `api_gateway_response_size_bytes` | `service` | Response size histogram |
| `api_gateway_active_requests` | `service="all"` | Requests in flight |
| `api_gateway_cache_hits_total`, `api_gateway_cache_misses_total` | `service` | Response cache |
| `api_gateway_rate_limited_total` | `service` | Requests rejected with 429 |

`service` is the name of the service the request was routed to, or `gateway` for the gateway's
own endpoints and unknown paths, so the number of series stays bounded. Go runtime and process
metrics are included.

The Compose stack ships a Prometheus scrape config (`docker compose --profile observability up`):

```yaml
scrape_configs:
  - job_name: odin
    static_configs:
      - targets: ['odin:8080']
```

## Traces

```yaml
tracing:
  enabled: true
  endpoint: http://otel-collector:4318/v1/traces   # OTLP over HTTP
  serviceName: odin-gateway
  sampleRate: 0.1
```

Every request gets a server span named `<METHOD> <route>`, continuing the caller's W3C trace
context. Requests to upstreams carry `traceparent`, so upstream spans join the same trace.
Buffered spans are flushed on shutdown.

## Logs

Access logs go through the gateway's logger, as text or JSON (`logging.json`), with `method`,
`uri`, `status`, `remote_ip`, `latency_ms` and `user_agent` per request. `logging.level` (or
`LOG_LEVEL`) is `debug`, `info`, `warn` or `error`; `debug` adds a line per upstream attempt.

## Health

| Endpoint | Use |
| --- | --- |
| `/health` | Liveness for load balancers |
| `/ready` | Kubernetes readiness probe |
| `/live` | Kubernetes liveness probe |
| `odin -healthcheck` | Exits 0 when the local `/health` answers; used by the Docker `HEALTHCHECK` |

Active health checks of upstream targets are described in
[Routing and resilience](traffic.md#health-checks).
