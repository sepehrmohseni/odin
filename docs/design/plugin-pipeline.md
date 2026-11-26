# Design: middleware plugins on the request path

Status: implemented. Findings: [SEC-14 and COR-21](../AUDIT.md).

## Problem

Middleware plugins could be uploaded and "enabled", but the chain they were registered in was
never mounted, so an enabled authentication plugin enforced nothing. The chain was also
appended to and sorted in place while requests read it, the phase stored with a plugin was
ignored, uploaded plugins shared a MongoDB collection with differently shaped records, and
nothing re-activated them after a restart.

## Decisions

**Phases are positions in each service's pipeline.** Rather than inventing request-lifecycle
callbacks for Echo-style `Handle(next)` middleware, a phase says where the middleware is
inserted:

```
service label -> pre-auth -> JWT check -> post-auth -> rate limit -> cache -> pre-proxy -> proxy
```

- `pre-auth` is for authentication plugins (API keys, HMAC signatures) that should answer before,
  or instead of, the JWT check.
- `post-auth` (the default) sees the authenticated user (`user_id` in the context) and runs before
  the rate limit and cache, so it can reject or rewrite requests first.
- `pre-proxy` runs only for requests that reach an upstream (not for cache hits) and can wrap the
  response.

This mirrors the split other gateways make between authentication, access and proxy phases,
without copying their phase names one to one. Names stored by earlier versions (`pre-routing`,
`post-routing`, `pre-route`, `post-route`, `pre-response`) map onto these three, so existing
records keep working. Unknown names are rejected when a plugin is registered.

**Only service routes.** Plugins never run on `/admin`, `/health`, `/metrics` and the other
gateway endpoints, so a broken plugin cannot lock an operator out of the admin.

**Order is priority, then name.** Lower priority runs first; equal priorities are ordered by
name, so the order is stable across restarts.

**Copy-on-write chain.** A change builds a new, sorted slice and publishes it under a lock; a
request takes the current slice and iterates it without locking. Enabling or disabling takes
effect on the next request.

**Isolation.** Each plugin is wrapped so that a panic, at construction or per request, becomes a
500 for that request with the plugin's name in the log. Hook plugins get the same treatment, and
an `*echo.HTTPError` they return keeps its status.

**One way to resolve a middleware.** A plugin exports `func New(map[string]interface{})
(plugins.Middleware, error)`, preferred, or `var Middleware plugins.Middleware`. Uploads accept
either.

**Uploads have their own collection** (`plugin_binaries`). Documents with a GridFS `file_id` are
moved out of `plugins` once, idempotently (upsert by `_id`, then delete). At startup, enabled
uploads are re-activated through the same path as the Enable button: download, SHA-256 check,
static validation, `plugin.Open`.

## Cost

Measured with `BenchmarkPluginPipeline` (see [Plugins](../plugins.md#performance)): with no plugins the pipeline adds
a slice read per phase; each matching plugin adds about two allocations. On a proxied request
this is within measurement noise.

## Not done here

- Plugin configuration schemas and per-service (rather than per-path) scoping.
- Out-of-process or WebAssembly plugins (the WASM package is not wired in yet).
- Reloading a plugin binary in place: Go cannot unload a shared object, so a new version needs
  a new name or a restart.
