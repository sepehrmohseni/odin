# Routing and resilience

## Load balancing

Requests to a service are spread across its `targets`:

| `loadBalancing` | Behaviour |
| --- | --- |
| `round-robin` (default) | Each request goes to the next target |
| `random` | A random target per request |

Targets that fail their health check or have an open circuit are skipped while another target
is available. Weighted, least-connections and consistent-hash balancing are planned.

## Timeouts and retries

```yaml
services:
  - name: catalog
    basePath: /catalog
    targets: [http://catalog-1:8080, http://catalog-2:8080]
    timeout: 5s        # wait at most 5s for the upstream's response headers
    retryCount: 2      # up to 2 retries ...
    retryDelay: 200ms  # ... after 200ms, then 400ms
```

`timeout` bounds the wait for the response headers. Once a response starts, it is streamed for
as long as it lasts, up to `server.writeTimeout`.

A request is retried after a connection error or a 502, 503 or 504, and only if it is safe to
repeat: `GET`, `HEAD`, `OPTIONS`, or any method with an `Idempotency-Key` header. Each retry goes
to a target that has not been tried yet. If the client disconnects, retrying stops. `retryCount: 0`
means the default (3); `-1` turns retries off.

## Health checks

```yaml
    healthCheck:
      enabled: true
      path: /health              # GET <target>/health
      interval: 10s
      timeout: 2s
      unhealthyThreshold: 3      # failures before the target is taken out
      healthyThreshold: 2        # successes before it is put back
      expectedStatus: [200, 204]
```

Each service with health checks has its own checker. Unhealthy targets get no new requests
while a healthy one remains; if every target is unhealthy, Odin still tries them rather than
refusing outright. With `monitoring.webhookUrl` set, a JSON alert is POSTed when a target goes
down or recovers.

## Circuit breakers

```yaml
    circuitBreaker:
      enabled: true
      minRequests: 10      # within the interval, at least this many requests ...
      failureRatio: 0.5    # ... of which half failed (error or 5xx), opens the circuit
      interval: 60s
      timeout: 30s         # how long it stays open before trial requests
      maxRequests: 1       # trial requests in half-open state
```

Every target has its own breaker. An open target is skipped like an unhealthy one; when all of a
service's targets are open, the service answers `503`. One failed trial request re-opens the
circuit. Requests the client abandons are not counted as failures.

## Rate limiting

```yaml
rateLimit:
  enabled: true           # applies limit per duration to every HTTP service
  limit: 600
  duration: 1m
  algorithm: sliding_window   # fixed_window (default), sliding_window, token_bucket
  strategy: redis             # local (default) or redis, shared by all instances
  redisUrl: redis://redis:6379/0

services:
  - name: search
    rateLimit: {limit: 30, window: 1s}   # this service's own limit
  - name: internal
    rateLimit: {enabled: false}          # exempt from the global limit
```

Limits are counted per service and per client: the JWT subject on authenticated services,
otherwise the client IP (see [trusted proxies](security.md#client-ip-addresses)). Responses carry
`X-RateLimit-Limit`, `X-RateLimit-Remaining` and `X-RateLimit-Reset`; rejected requests get `429`
with `Retry-After`. If Redis is unreachable, requests are allowed and the error is logged, unless
`rateLimit.failClosed: true`.

## Canary releases

```yaml
    canary:
      enabled: true
      targets: [http://catalog-canary:8080]
      weight: 10              # 10% of clients
      header: X-Canary        # requests with X-Canary: yes always go to the canary
      headerValue: "yes"
      cookieName: canary
      cookieValue: "1"
```

The weight split hashes the client IP, so a client stays on the same side. `weight: 100` sends
all traffic to the canary.
