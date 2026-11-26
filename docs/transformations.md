# Transformations

Odin can rewrite requests and responses in three ways. They compose: field mappings run first,
then enrichment, then templates.

## Templates

`transformation` rewrites headers, query parameters and bodies with Go
[text/template](https://pkg.go.dev/text/template) expressions:

```yaml
services:
  - name: users
    basePath: /api/users
    targets: [http://users:8080]
    transformation:
      request:
        headers:
          X-Source: gateway
        queryParams:
          page: '{{ default "1" .page }}'
        body: '{"user": {"name": "{{ .name }}", "email": "{{ lower .email }}"}}'
      response:
        headers:
          X-Powered-By: odin
        body: '{"data": {{ toJson . }}}'
```

| Template | Sees |
| --- | --- |
| request header | the request headers by name |
| request query parameter | the current query parameters |
| request body | the JSON body's fields (or `.body` for non-JSON) |
| response header | the response headers |
| response body | the JSON response's fields plus `.statusCode` (or `.body`) |

Functions: `upper`, `lower`, `title`, `trim`, `replace`, `contains`, `split`, `join`,
`default`, `toJson`, `fromJson`. Templates are parsed once. If a template fails, the original
request or response is passed through and a warning is logged.

## Field mapping

`transform` moves JSON values between paths, with a default when the source is missing:

```yaml
    transform:
      request:
        - from: $.username
          to: $.user.name
        - from: $.includeInactive
          to: $.filters.inactive
          default: "false"          # parsed as JSON when possible
      response:
        - from: $.data
          to: $.users
```

`{"username": "john"}` is sent as `{"user": {"name": "john"}, "filters": {"inactive": false}}`.
Rules apply in order to JSON objects; other bodies pass through unchanged. Response rules apply
to 2xx responses only.

## Enrichment from other services

`aggregation` adds data from other services to a service's JSON responses:

```yaml
  - name: users
    basePath: /api/users
    targets: [http://users:8080]
    aggregation:
      dependencies:
        - service: preferences              # another configured service
          path: /prefs/{user_id}
          parameterMapping:
            - from: $.user.id               # taken from the users response
              to: "{user_id}"
          resultMapping:
            - from: $                       # the whole dependency response ...
              to: $.preferences             # ... goes here
```

For a 2xx JSON response, each dependency is called on its first target with the client's
`Authorization` header. Values substituted into the path are URL-escaped. A dependency that fails
is skipped, and the response is returned without it.

The `/aggregate?services=users,orders` endpoint is different: it calls up to ten services in
parallel and returns their responses side by side, enforcing each service's authentication.

When any of these is configured, the response is buffered (up to 10 MiB) instead of streamed,
and its `Content-Length` is recomputed.
