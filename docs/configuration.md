# Configuration

Odin is configured with one YAML file, and optionally a `services.yaml` next to it. Start the
gateway with `odin -config path/to/config.yaml`. Every key is listed in the
[configuration reference](reference/configuration.md), which is generated from the code; this
page shows how the pieces fit together.

## A complete example

```yaml
server:
  port: 8080
  readTimeout: 30s
  writeTimeout: 60s
  maxBodySize: 10M
  trustedProxies: [10.0.0.0/8]     # your load balancer, so X-Forwarded-For is believed

logging:
  level: info
  json: true

monitoring:
  enabled: true
  path: /metrics

auth:
  jwtSecret: ${ODIN_JWT_SECRET}    # from the environment
  issuer: https://auth.example.com
  ignorePathRegexes: [^/health$, ^/metrics$]

rateLimit:
  enabled: true
  limit: 600
  duration: 1m
  strategy: redis
  redisUrl: redis://redis:6379/0

cache:
  enabled: true
  ttl: 30s
  strategy: redis
  redisUrl: redis://redis:6379/1

admin:
  enabled: true
  username: admin
  passwordHash: ${ODIN_ADMIN_PASSWORD_HASH}   # bcrypt

services:
  - name: products
    basePath: /api/products
    targets: [http://products-1:8080, http://products-2:8080]
    timeout: 10s
    healthCheck: {enabled: true, path: /health, interval: 10s}
    circuitBreaker: {enabled: true}

  - name: orders
    basePath: /api/orders
    targets: [http://orders:8080]
    authentication: true
    cache: {enabled: false}
    rateLimit: {limit: 60, window: 1m}
```

## Environment variables

Anywhere in the file, `${NAME}` is replaced with the variable `NAME` and `${NAME:-fallback}` with
its value or the fallback. An unset `${NAME}` without a fallback stops startup and names the
variable. Comment lines are not expanded, and a bare `$` (as in bcrypt hashes) is left alone;
write `$${` for a literal `${`.

Some settings can also be set directly:

| Variable | Overrides |
| --- | --- |
| `ODIN_JWT_SECRET` | `auth.jwtSecret` |
| `ODIN_ADMIN_USERNAME`, `ODIN_ADMIN_PASSWORD`, `ODIN_ADMIN_PASSWORD_HASH` | `admin.username`, `admin.password`, `admin.passwordHash` |
| `GATEWAY_PORT` | `server.port` |
| `LOG_LEVEL` | `logging.level` (until the file is loaded) |

A file named `auth_secrets.yaml` (`jwtSecret: ...`) in `config/`, `/etc/odin/` or `~/.odin/`
takes precedence over the environment for the JWT secret, and `admin_creds.yaml` likewise for
the admin login.

## Splitting services out

Services in `services.yaml` (same directory as the main file, `services:` at the top) are added
to those in the main file. The admin interface never rewrites `services.yaml`; services defined
there are read-only in the admin.

## Services in MongoDB

With `mongodb.loadServices: true`, the gateway also serves the services stored in MongoDB's
`services` collection, which `odin-migrate` fills from YAML files and the admin API manages:

```bash
curl -u admin:$PASSWORD -H 'Content-Type: application/json' \
  -d '{"name":"billing","basePath":"/api/billing","targets":["http://billing:8080"],"timeout":"5s"}' \
  http://localhost:8080/admin/api/mongodb/services
```

`GET`, `PUT` and `DELETE /admin/api/mongodb/services/{name}` read, replace and remove one;
`GET /admin/api/mongodb/services` lists them, with `served` telling whether the running gateway
loaded it. Names, base paths and targets are checked like the admin's service form. Changes
apply when the gateway restarts, like edits to the config file. A service in the config file
wins over a stored one with the same name; stored services appear on the admin dashboard marked
"MongoDB". If the stored services cannot be read at startup, the gateway does not start, so it
never runs with routes silently missing.

## Validation

At startup Odin rejects: a port outside 1-65535, services without a name, base path or targets,
targets that are not `http(s)://host[:port]`, invalid `auth.ignorePathRegexes`, an unknown
`rateLimit.algorithm` or `rateLimit.strategy`, and unparsable `services.yaml`. A key that is not
part of the schema (a misspelling, or a key from an older version) has no effect; Odin logs a
warning with its file and line, for example
`config.yaml line 12: unknown key "ratelimit": it has no effect`, and starts anyway.
