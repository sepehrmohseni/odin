# Plugins

Odin loads Go plugins: shared objects built with `go build -buildmode=plugin` against the same
Go release and the same versions of Odin's packages as the gateway binary. A plugin runs inside
the gateway process with full access, so load only plugins you trust.

!!! note
    The Docker image is built without cgo and cannot load Go plugins. Use a binary built with
    `CGO_ENABLED=1` on a glibc system.

## Hook plugins from the config file

A hook plugin exports a variable named `Plugin` that implements:

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
      hooks: [pre-request, post-response]
      config:
        level: info
```

`PreRequest` runs before routing for every request; returning an error stops the request.
`PostResponse` runs after the response. See `examples/plugins/request_logger.go`, built with
`make build-plugins`.

## Uploading plugins in the admin

With MongoDB enabled, the admin's **Plugins** page accepts `.so` uploads. An upload is checked
without loading it: it must be an ELF shared object for the gateway's architecture, built with
`-buildmode=plugin` and the gateway's Go release, and must export the required symbol. The file
is stored with its SHA-256, which is checked again before the plugin is enabled. Plugin names may
only contain letters, digits, `.`, `_`, `+` and `-`, and plugins are only loaded from the plugins
directory.

!!! warning
    Middleware plugins (exporting `New`) can be uploaded and enabled, but do not run on requests
    yet. Ordered middleware phases on the request path are being built; see the
    [roadmap](roadmap.md).
