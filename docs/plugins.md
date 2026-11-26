# Plugins

Odin runs Go plugins: shared objects built with `go build -buildmode=plugin` against the same Go
release and the same versions of Odin's packages as the gateway binary. A plugin runs inside the
gateway process with full access, so load only plugins you trust.

!!! note
    The Docker image is built without cgo and cannot load Go plugins. Use a binary built with
    `CGO_ENABLED=1` on a glibc system, or write a [WebAssembly plugin](#webassembly-plugins),
    which works everywhere.

There are three kinds of plugins: **middleware plugins** (Go shared objects) and
**WebAssembly plugins**, which sit in a service's request pipeline, and **hook plugins**, which
are called at fixed points of every request.

## Middleware plugins

A middleware plugin wraps the next handler, like Echo middleware:

```go
package main

import (
    "net/http"

    "odin/pkg/plugins"
    "github.com/labstack/echo/v4"
)

type apiKey struct{ key string }

func (a *apiKey) Name() string                            { return "api-key" }
func (a *apiKey) Version() string                         { return "1.0.0" }
func (a *apiKey) Initialize(map[string]interface{}) error { return nil }
func (a *apiKey) Cleanup() error                          { return nil }
func (a *apiKey) Handle(next echo.HandlerFunc) echo.HandlerFunc {
    return func(c echo.Context) error {
        if c.Request().Header.Get("X-API-Key") != a.key {
            return echo.NewHTTPError(http.StatusUnauthorized, "missing or wrong API key")
        }
        return next(c)
    }
}

// New is called with the plugin's configuration when it is enabled.
func New(config map[string]interface{}) (plugins.Middleware, error) {
    key, _ := config["key"].(string)
    return &apiKey{key: key}, nil
}
```

Build it inside a checkout of the same Odin version: `go build -buildmode=plugin -o api-key.so
./path/to/plugin`. Exporting `var Middleware plugins.Middleware` instead of `New` also works;
then `Initialize` receives the configuration. Three examples (API keys, request logging, header
rewriting) are in `examples/middleware-plugins`; `examples/middleware-plugins/build.sh` builds
them.

### Phases

Each middleware plugin runs in one phase of every service route whose path matches its routes:

```mermaid
flowchart LR
  L[service label] --> A[pre-auth] --> J[JWT check] --> B[post-auth] --> R[rate limit] --> C[cache] --> P[pre-proxy] --> X[proxy]
```

| Phase | Runs | Typical use |
| --- | --- | --- |
| `pre-auth` | before the JWT check | API keys, request signatures, IP rules |
| `post-auth` (default) | after authentication, before rate limits and the cache; `user_id` is set | per-user rules, request rewriting |
| `pre-proxy` | after the cache, only for requests that reach an upstream | upstream headers, response wrapping |

Within a phase, plugins run by priority (lower first), then by name. Routes are path patterns:
`*` (every service route), an exact path, a prefix `/api/*` (matches `/api` and `/api/...`, not
`/apiary`) or a glob such as `/api/*/orders`. Plugins never run on the gateway's own endpoints
(`/admin`, `/health`, `/metrics`).

A plugin that panics fails only that request, with a 500 and the plugin's name in the log.
Enabling and disabling take effect on the next request, without a restart. The admin's
**Middleware** page shows the chain and changes a plugin's phase, priority and routes live.

### Enabling middleware plugins

- **Upload** (admin, **Plugins**, needs MongoDB): upload the `.so` with a name, version, priority,
  phase, routes and a JSON config, then enable it. The upload is checked without running it: it
  must be an ELF shared object for the gateway's architecture, built with `-buildmode=plugin` and
  the gateway's Go release, exporting `New` or `Middleware`. Its SHA-256 is stored and checked
  again before it is enabled. Enabled uploads are activated again at startup.
- **Plugin records** (admin API `POST /admin/api/plugins` with `"pluginType": "middleware"`,
  `"appliedTo"`, `"priority"`, `"phase"`): the binary must be in the plugins directory
  (`plugins.directory`, default `/var/odin/plugins`).

Plugin names may only contain letters, digits, `.`, `_`, `+` and `-`.

## WebAssembly plugins

A WebAssembly plugin runs in the same pipeline as a middleware plugin (same phases, priorities
and routes, and it shows on the admin's **Middleware** page), but in a sandbox: it has no file
system, network or environment, its memory is capped, and each request it handles is
interrupted at a timeout. Plugins can be written in any language that compiles to wasm32, and
they work in the Docker image (no cgo needed).

```yaml
wasm:
  enabled: true
  pluginDir: /etc/odin/plugins
  maxInstances: 4          # requests one plugin handles at a time (default 4)
  maxMemoryPages: 512      # memory per instance, in 64 KiB pages (default 512 = 32 MiB)
  plugins:
    - name: api-key
      path: api-key.wasm
      enabled: true
      phase: pre-auth      # pre-auth, post-auth (default) or pre-proxy
      priority: 10
      routes: ["/api/*"]   # default ["*"]
      services: []         # limit to these services (default: all its routes match)
      timeout: 100ms       # per request (default 100ms)
      readBody: false      # hand it the request body, up to 1 MiB
      failOpen: false      # on a plugin error, continue instead of answering 500
      config:              # handed to the plugin with every request
        keys: {k-123: mobile-app}
```

A plugin that cannot be loaded stops the gateway from starting, so a missing authentication
plugin never leaves its routes open. When a plugin fails or times out on a request, the client
gets a 500 (unless `failOpen`), the error is logged with the plugin's name, and the next request
gets a fresh instance.

### Writing one

The module exports its `memory` and two functions:

| Export | Signature | What |
| --- | --- | --- |
| `odin_alloc` | `(size i32) -> i32` | Returns an address where the gateway may write `size` bytes of input |
| `odin_handle` | `(ptr i32, size i32) -> i64` | Handles the input; returns the output's address in the high 32 bits and its length in the low 32 |

It may import `env.log(ptr i32, len i32)` to write a line to the gateway's log, and WASI for
its language runtime (clocks and random numbers are real; there are no files or arguments).

The input is JSON:

```json
{
  "service": "orders",
  "user": "usr-001",
  "request": {
    "method": "GET",
    "path": "/api/orders",
    "query": "page=2",
    "headers": {"X-Api-Key": "k-123"},
    "remoteAddr": "203.0.113.7",
    "body": ""
  },
  "config": {"keys": {"k-123": "mobile-app"}}
}
```

`user` is set after authentication (post-auth and pre-proxy), `body` only with `readBody`.
Header values with several entries are joined with `, `.

The output is JSON too. `"action": "respond"` answers the client and stops the request;
anything else (or an empty output) lets it continue:

```json
{"action": "respond", "status": 401, "headers": {"Content-Type": "application/json"}, "body": "{\"message\":\"no key\"}"}
```

```json
{
  "action": "continue",
  "setHeaders": {"X-Consumer": "mobile-app"},
  "removeHeaders": ["X-Api-Key"],
  "responseHeaders": {"X-Checked-By": "wasm-api-key"}
}
```

`setHeaders` and `removeHeaders` change the request before the next step (and the upstream);
`responseHeaders` are set on the response to the client.

[examples/wasm-plugins/api-key](https://github.com/sepehrmohseni/odin/tree/main/examples/wasm-plugins/api-key)
is a complete plugin in Go; build it with:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o api-key.wasm ./examples/wasm-plugins/api-key
```

Instances are reused across requests, so a plugin may keep state in memory, but it must not
rely on it: an instance is replaced after an error or timeout, and several run at once.

### Cost

On an Intel Core i5-10500 with six cores given to Docker, a proxied `GET` to a local backend took
89 µs without plugins and 328 µs with the Go-built API key plugin (about 0.24 ms per request,
most of it the plugin's JSON handling in the Go WebAssembly runtime). To reproduce:

```bash
scripts/docker-check.sh -- go test -run x -bench WASMPlugin -benchmem ./test/unit/pkg/gateway/
```

## Hook plugins

A hook plugin exports `var Plugin` implementing:

```go
type Plugin interface {
    Name() string
    Version() string
    Initialize(config map[string]interface{}) error
    PreRequest(ctx context.Context, pluginCtx *plugins.PluginContext) error
    PostRequest(ctx context.Context, pluginCtx *plugins.PluginContext) error
    PreResponse(ctx context.Context, pluginCtx *plugins.PluginContext) error
    PostResponse(ctx context.Context, pluginCtx *plugins.PluginContext) error
    Cleanup() error
}
```

```yaml
plugins:
  enabled: true
  directory: /var/odin/plugins
  plugins:
    - name: request-logger
      path: /var/odin/plugins/request_logger.so
      enabled: true
      hooks: [pre-request, post-request, pre-response, post-response]
      config:
        level: info
```

| Hook | When |
| --- | --- |
| `pre-request` | before routing; returning an error stops the request |
| `post-request`, `pre-response` | when the response status is known, just before the headers are written |
| `post-response` | after the response has been written |

`PluginContext` has the method, path and request headers, and once known the service name, the
authenticated user and the response status (`Metadata["status"]`). Returning an
`*echo.HTTPError` from `PreRequest` rejects the request with that status. Hook plugins run for
every request when `plugins.enabled` is true. See `examples/plugins/request_logger.go`
(`make build-plugins`).

Programs that embed the gateway can register plugins compiled into them, without shared objects:
`gateway.Plugins().RegisterMiddleware(...)` and `RegisterPlugin(...)`.

## Performance

The pipeline costs a slice read per phase, plus about two allocations for each plugin that
matches. On an Intel Core i5-10500 with six cores given to Docker, a proxied `GET` through the
whole gateway to a local backend took 210 to 255 µs with zero, three or nine pass-through
plugins, and allocations grew from 133 to 151 per request; the time differences were within
run-to-run noise, because the loopback HTTP call dominates. To reproduce:

```bash
scripts/docker-check.sh -- go test -run x -bench PluginPipeline -benchmem ./test/unit/pkg/gateway/
```

The reasoning behind the phases is in the [design note](design/plugin-pipeline.md).
