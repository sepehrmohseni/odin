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
forms to it, and the monitoring WebSocket checks its origin too.

## Pages

| Page | What |
| --- | --- |
| Dashboard | Configured services |
| Services | Add and edit services (name, base path, targets, auth, load balancing, timeout, retries) |
| Monitoring | Live request rate, latency, status codes and recent requests |
| Traces | Recent requests with timing |
| Settings | Server, logging, rate-limit, cache, monitoring and tracing settings; config export and backups |
| Plugins | Upload and manage Go plugins (needs MongoDB) |
| Integrations | Postman collection sync (needs MongoDB) |

Secrets (JWT secret, passwords, tokens, credentials inside Redis or MongoDB URLs) are masked in
every view and export.

## Saving changes

Edits are validated and written to the config file atomically, with owner-only permissions; the
previous version is kept under `backups/` next to it. **They take effect when the gateway
restarts.** Services defined in `services.yaml` are read-only here. In the Docker images the
config file is mounted read-only, so the admin cannot save changes there; change the file and
restart instead.
