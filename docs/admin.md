# Admin interface

A web interface and JSON API under `/admin`, for viewing and editing the gateway's
configuration and watching traffic. It is off unless you turn it on:

```yaml
admin:
  enabled: true
  username: admin
  passwordHash: ${ODIN_ADMIN_PASSWORD_HASH}   # bcrypt: htpasswd -bnBC 10 "" 'secret' | tr -d ':\n'
```

With `admin.enabled: false` no `/admin` route exists at all.

## Logging in

- **Password**: `admin.passwordHash` (bcrypt), `admin.password`, or `ODIN_ADMIN_PASSWORD`. With
  none, Odin generates a random password at startup and prints it once in the log. There is no
  built-in default.
- **Browser**: the login form creates a session (a random token in an `HttpOnly`,
  `SameSite=Strict` cookie, `Secure` over HTTPS, valid for 12 hours). **Log out** ends it.
- **Scripts**: HTTP Basic authentication works for the JSON API:
  `curl -u admin:$PASSWORD http://localhost:8080/admin/api/settings`.
- After five failed logins from one IP within 15 minutes, that IP is blocked for 15 minutes.

State-changing requests must come from the admin's own origin, so other sites cannot submit
forms to it, and the monitoring WebSocket checks its origin too. If the admin sits behind a
proxy that changes the `Host` header, so that the browser's origin never matches it (GitHub
Codespaces port forwarding does this), list the public origin:

```yaml
admin:
  allowedOrigins: ["https://admin.example.com"]   # or ODIN_ADMIN_ALLOWED_ORIGINS, comma-separated
```

## Pages

| Page | What |
| --- | --- |
| Dashboard | Live totals (requests per second, success rate, latency, 4xx and 5xx), and every service with its route, targets and their health, policies (JWT, rate limit, cache, timeout) and traffic; a curl command to copy for each, recent requests, response classes and upstream health |
| Monitoring | Requests over the last five minutes by status class, latency percentiles, per-service traffic and error rates, and the state of every health-checked target |
| Request log | The last 100 requests, filterable by path, service, status class and duration, each copyable as a curl command |
| Add service, edit | Name, base path, targets, authentication, load balancing, timeout and retries |
| Settings | Server, logging, rate-limit, cache, monitoring and tracing settings; config export and backups |
| Middleware | The middleware plugin chain by phase; change a plugin's phase, priority or routes, take it out of the chain, or run it on a test request. Changes apply on the next request, and are saved with MongoDB |
| Plugins | Plugin records, and uploads of Go plugin binaries (needs MongoDB) |
| Postman | Postman collection sync (needs MongoDB) |

The traffic figures count requests to services and to unknown paths since the gateway started;
the admin itself, `/static`, health probes (`/health`, `/ready`, `/live`) and metric scrapes are
left out. The pages refresh every few seconds while they are visible. The theme follows the
system setting, and the sidebar switches between light and dark.

Secrets (JWT secret, passwords, tokens, credentials inside Redis or MongoDB URLs) are masked in
every view and export.

## Saving changes

Edits are validated and written to the config file atomically, with owner-only permissions; the
previous version is kept under `backups/` next to it. **They take effect when the gateway
restarts.** Services defined in `services.yaml` are read-only here. In the Docker images the
config file is mounted read-only, so the admin cannot save changes there; change the file and
restart instead.
