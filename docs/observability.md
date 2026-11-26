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

## Traffic anomaly detection

With `ai.enabled`, Odin learns what each service's traffic normally looks like and reports
windows that fall far outside it, on the admin's **Anomalies** page and, optionally, to a webhook.

```yaml
ai:
  enabled: true
  analysisInterval: 1m        # window length
  baselineWindow: 1h          # roughly how far back "normal" reaches
  anomalyThreshold: 3         # standard deviations that count as anomalous
  minSamplesForBaseline: 10   # windows to learn from before judging a service
  enableAlerts: true
  alertWebhookUrl: https://hooks.example.com/odin   # JSON with a Slack-compatible "text"
  alertMinSeverity: medium
  tags: {environment: production}
```

For every service, each window's request rate, share of server errors (5xx) and average latency
are compared with an exponentially weighted mean and standard deviation of earlier windows. A
metric more than `anomalyThreshold` deviations away is an anomaly: a traffic spike or drop, an
error spike or a latency spike. The deviations have floors (10% of the mean or one request per
window for traffic, 2 percentage points for errors, 20% or 5 ms for latency) and each check has
a minimum count, so a very steady service or a handful of requests does not raise alarms. One
client sending at least half of a busy service's traffic, while others are active too, is
reported as well. Anomalous values are not learned, so an outage does not become the new normal.

A deviation that continues is one ongoing anomaly; it is resolved when a window is normal again,
and can be resolved or marked a false positive by hand. Severity rises with the deviation (two
and three times the threshold are high and critical; half of all responses failing is critical).
Only new anomalies are sent to the webhook.

Anomalies are kept in memory (the newest 500) and, with MongoDB, in the `ai_anomalies`
collection for `retentionDays` (default 30). Baselines are learned again after a restart.

An analysis service can add a written explanation and suggestions to each new anomaly: set
`useGrokModel: true` and `grokServiceUrl` to a service that answers `POST /analyze`, such as the
one in `deployments/grok` (`docker compose --profile ai up` starts it as `http://odin-grok:8000`).
Odin sends it the service name and figures, never client addresses.
