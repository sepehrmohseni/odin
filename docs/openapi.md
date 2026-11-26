# OpenAPI document

Odin can publish an OpenAPI 3 document of its routes, and a Swagger UI page to browse it:

```yaml
openapi:
  enabled: true            # serve /openapi.json and /openapi.yaml
  title: Shop API
  version: "2.3.0"
  description: Public API of the shop.
  uiEnabled: true          # Swagger UI ...
  uiPath: /openapi         # ... at this path (the default)
```

Both are off by default. When enabled they are public, like the routes they describe.

## What the generated document contains

The gateway knows its services' routes, not the APIs behind them, and the document says only
what it knows:

- For each HTTP service, its base path and `{basePath}/{path}`, with `GET`, `POST`, `PUT`,
  `PATCH` and `DELETE`, tagged with the service name. `{path}` stands for anything below the base
  path, including further `/` segments.
- For GraphQL services, `GET` and `POST` on the base path, with the `query`, `operationName`
  and `variables` request body.
- A bearer (JWT) security requirement and a 401 response on services with
  `authentication: true`, and a 429 response with `Retry-After` on rate-limited services.
- The gateway's own errors, 502, 503 and 504, with the `{"message": ...}` body described in
  [Concepts](concepts.md#error-responses); everything else is the upstream's response, passed
  through.

Upstream addresses (`targets`) are never included. gRPC services are left out. The document is
built once at startup, so it follows configuration changes on restart.

To keep a copy, set `outputPath` (for example `/var/lib/odin/openapi.yaml`): the generated
document is written there at startup, as YAML for `.yaml` or `.yml` and JSON otherwise.

## Serving your own document

To publish a hand-written document with the operations and schemas of your APIs instead, set
`autoGenerate: false` and point `outputPath` at it (JSON or YAML):

```yaml
openapi:
  enabled: true
  autoGenerate: false
  outputPath: /etc/odin/openapi.yaml
  uiEnabled: true
```

Odin then serves that file at `/openapi.json` and `/openapi.yaml` (converted as needed). A file
that is not an OpenAPI or Swagger document stops startup with an error.

## Swagger UI

The page at `uiPath` loads Swagger UI 5.36.0 from jsDelivr, pinned and checked with Subresource
Integrity, so the gateway binary stays small; browsers that open it need to reach
`cdn.jsdelivr.net`. It is served with a Content Security Policy that allows scripts only from the
gateway and that CDN, and requests ("Try it out") only to the gateway itself.
