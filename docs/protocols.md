# WebSocket, SSE and GraphQL

## WebSocket

WebSocket upgrades on an HTTP service are proxied automatically: no extra configuration.

1. Odin connects to the chosen target first, forwarding the client's headers (cookies,
   `Authorization`, `Origin`), the `X-Forwarded-*` headers and the requested subprotocols.
2. If the upstream refuses (for example because of its origin check), the client gets that HTTP
   status.
3. Otherwise the client is upgraded with the subprotocol the upstream chose, and messages are
   relayed both ways. Both sides are pinged, idle connections close after 60 seconds without a
   message or pong, messages are limited to 512 KiB, and close codes are passed through.

Authentication, rate limits and canary rules apply to the upgrade request like any other.

## Server-sent events and streaming

Responses are streamed to the client as they arrive. Responses without a fixed length
(`text/event-stream` or chunked) are flushed after every read, so events are not held back. The
service `timeout` only bounds the wait for response headers; set `server.writeTimeout` high
enough for long streams.

## GraphQL

```yaml
services:
  - name: graph
    basePath: /graphql
    targets: [http://graphql:4000/graphql]
    protocol: graphql
    authentication: true
    graphql:
      maxQueryDepth: 10
      maxQueryComplexity: 1000
      enableIntrospection: false
```

`POST <basePath>` validates the query (depth by brace nesting, a size limit, introspection
blocked unless enabled) and forwards it to the first target. Planned: forwarding client headers,
passing the upstream status through, allowing `__typename` when introspection is off, and query
caching.

## gRPC

`protocol: grpc` is not usable yet: HTTP-to-gRPC transcoding needs server reflection, which is on
the [roadmap](roadmap.md).
