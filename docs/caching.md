# Caching

```yaml
cache:
  enabled: true        # cache responses of every HTTP service ...
  ttl: 30s
  strategy: redis      # local (default) or redis
  redisUrl: redis://redis:6379/1

services:
  - name: prices
    cache: {ttl: 5s}           # ... with a shorter TTL here
  - name: orders
    cache: {enabled: false}    # ... and never here
```

The cache runs inside each service's route, after authentication and the rate limit. It only
stores a response when sharing it is safe:

- the request is `GET` (or `HEAD` for lookups), has no `Authorization` or `Cookie` header, is not
  a WebSocket upgrade, and does not say `Cache-Control: no-store` or `no-cache`;
- the response is `200`, sets no cookie, is not `no-store`, `no-cache` or `private`, has no
  `Vary: *`, and its body is at most 1 MiB.

A shorter `max-age` or `s-maxage` from the upstream overrides the TTL. Entries are keyed by method,
host, path and query string, and keep the upstream's headers. Responses carry `X-Cache: HIT` or
`X-Cache: MISS`, which the metrics count.

With `strategy: local` each instance has its own cache; with `redis`, all instances share one. The
admin's "clear cache" action empties it.
