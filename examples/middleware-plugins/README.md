# Example middleware plugins

Three middleware plugins that run in a service's request pipeline. Each exports
`var Middleware` and receives its configuration in `Initialize`. How phases, priorities and
routes work is described in [docs/plugins.md](../../docs/plugins.md).

| Plugin | What it does | Suggested phase |
| --- | --- | --- |
| `api-key-auth` | Rejects requests without a listed key in a header (401) | `pre-auth` |
| `request-logger` | Logs method, path, client IP, status and duration | `post-auth` |
| `request-transformer` | Adds, removes and replaces request headers | `pre-proxy` |

## Build

```bash
examples/middleware-plugins/build.sh            # writes build/plugins/<name>.so
```

The script builds inside this checkout, so the plugins share the gateway's package versions; a
plugin built against other versions of Echo or Odin will not load. Build the gateway from the
same checkout with `CGO_ENABLED=1` (the Docker image cannot load plugins).

## Enable

In the admin interface (needs MongoDB), open **Plugins**, upload the `.so` with a name, version,
phase, priority, routes and the JSON configuration below, then enable it. It runs from the next
request, and again after a restart.

## Configuration

`api-key-auth`:

```json
{ "keys": ["key1", "key2"], "headerName": "X-API-Key", "errorMessage": "Invalid or missing API key" }
```

`request-logger`:

```json
{ "prefix": "[REQUEST]", "logLevel": "info", "includeHeaders": false }
```

`request-transformer`:

```json
{
  "addHeaders": { "X-Gateway": "Odin" },
  "removeHeaders": ["X-Internal-Only"],
  "replaceHeaders": { "User-Agent": "Odin-Gateway/1.0" }
}
```
