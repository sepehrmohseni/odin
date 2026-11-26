# Security

## JWT authentication

A service with `authentication: true` only accepts requests that carry a valid bearer token:

```yaml
auth:
  jwtSecret: ${ODIN_JWT_SECRET}
  issuer: https://auth.example.com      # optional: iss must match
  audience: odin                        # optional: aud must contain it
  leeway: 30s                           # optional clock-skew tolerance
  ignorePathRegexes: [^/health$]        # paths that skip the check

services:
  - name: orders
    basePath: /api/orders
    targets: [http://orders:8080]
    authentication: true
```

Tokens must be signed with HMAC (HS256, HS384 or HS512) using the secret and must have an `exp`
claim; `nbf` and `iat` are checked when present. The `Bearer` scheme is case-insensitive. A
missing or invalid token gets `401`. Without a configured secret, authenticated services answer
`503` instead of accepting tokens.

After a successful check the claims are available to later steps: rate limits use the token's
`user_id` (or `sub`) as the client key.

Authentication also covers GraphQL services and the services named in `/aggregate`.
`ignorePathRegexes` exempts request paths, not services: putting `^/aggregate$` there does not
expose authenticated services through it.

RSA/ECDSA keys, JWKS and OIDC are planned.

## Client IP addresses

By default the client IP is the address of the TCP connection, and `X-Forwarded-For` from the
client is ignored. Behind a load balancer, list it so its header is believed:

```yaml
server:
  trustedProxies: [10.0.0.0/8, 192.168.1.10]
```

The client IP is used for rate limits, admin login throttling, canary stickiness and logs.
Upstreams receive `X-Forwarded-For` (extended only for trusted proxies), `X-Real-IP`,
`X-Forwarded-Proto` and `X-Forwarded-Host`.

## Limits

- Request bodies on proxied routes are limited to `server.maxBodySize` (default `10M`); larger
  ones get `413`.
- `server.readHeaderTimeout` (default 10s) and `server.idleTimeout` (default 120s) bound slow
  clients and idle connections.
- Panics in handlers become `500` responses instead of dropped connections.

## Secrets

- Pass secrets through the environment (`${NAME}` in the config, or `ODIN_JWT_SECRET`,
  `ODIN_ADMIN_PASSWORD`). The Docker image and the Helm chart do this.
- The admin interface masks secrets in every view and export, and writes the config file with
  owner-only permissions.
- Hop-by-hop headers are not forwarded, and the Consul ACL token is never sent upstream.

The admin interface has its own protections; see [Admin interface](admin.md). To report a
vulnerability, see the repository's [security policy](../SECURITY.md).
