# Deployment

## Docker

The image (`Dockerfile`) is a static binary on Alpine, runs as uid 65532, and has a health check
that runs `odin -healthcheck`. It is built for `linux/amd64` and `linux/arm64`. Releases publish
it to `ghcr.io/sepehrmohseni/odin`, signed with keyless cosign, with an SBOM and provenance.

```bash
docker run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config/config.yaml:ro" \
  -e ODIN_JWT_SECRET="$(openssl rand -hex 32)" \
  ghcr.io/sepehrmohseni/odin:0.1.0
```

Without a mounted config, the image serves only `/health` and `/metrics`. The image cannot load
Go plugins, which need cgo.

Verify an image signature:

```bash
cosign verify ghcr.io/sepehrmohseni/odin:0.1.0 \
  --certificate-identity-regexp 'https://github.com/sepehrmohseni/odin/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Docker Compose

- **Development** (`docker-compose.yml`): Odin with Redis, MongoDB and four demo backends, every
  container health-checked; `scripts/quickstart.sh` sets it up. Optional profiles:
  `observability` (Prometheus, Grafana) and `ai`.
- **Single host** (`deployments/docker/docker-compose.prod.yml`): Odin with your config and Redis,
  with a read-only root filesystem.

## Kubernetes with Helm

```bash
helm install odin deployments/helm/odin \
  --set secrets.jwtSecret="$(openssl rand -hex 32)" \
  --set config.gateway.trustedProxies='{10.0.0.0/8}' \
  --set-json 'services=[{"name":"api","basePath":"/api","targets":["http://api.default.svc:80"]}]'
```

The chart runs non-root with a read-only root filesystem, keeps the JWT secret and admin password
in a Secret (or `secrets.existingSecret`), probes `/live` and `/ready`, restarts pods when the
config changes, and offers an Ingress, a HorizontalPodAutoscaler and a Prometheus Operator
ServiceMonitor. All values are listed in the chart's README.

For several replicas, use `strategy: redis` for rate limits and caching so all of them share one
state.

## Binaries

Release archives for Linux and macOS (amd64 and arm64) contain `odin`, `odin-migrate`, the admin
templates and assets, and the default config. Run `odin` from the unpacked directory, because the
admin interface reads its templates relative to the working directory:

```bash
tar xzf odin_0.1.0_linux_amd64.tar.gz && cd odin_0.1.0_linux_amd64
./odin -config config/default_config.yaml
```

## Production checklist

- Set `ODIN_JWT_SECRET` (or `auth.jwtSecret`) if any service has `authentication: true`.
- List your load balancer in `server.trustedProxies`.
- Keep the admin off, or protect it with `admin.passwordHash` and expose it only internally.
- Use Redis for rate limits and caching when running more than one instance.
- Scrape `/metrics` and alert on `api_gateway_requests_total{status=~"5.."}`.
