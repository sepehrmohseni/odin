# Docker deployment

The image is built from the repository's [Dockerfile](../../Dockerfile): a static Odin binary on
Alpine, running as uid 65532, with a container health check (`odin -healthcheck`). It works on
amd64 and arm64.

```bash
docker build -t odin .                                   # from the repository root
docker run --rm -p 8080:8080 odin                         # built-in config: health and metrics only
docker run --rm -p 8080:8080 \
  -v "$PWD/config/config.yaml:/app/config/config.yaml:ro" \
  -e ODIN_JWT_SECRET="$(openssl rand -hex 32)" odin       # your own routes
```

- `docker-compose.prod.yml` here runs Odin with your config and Redis, read-only and with a
  health check.
- The development stack with demo backends is the repository's
  [docker-compose.yml](../../docker-compose.yml); [scripts/quickstart.sh](../../scripts/quickstart.sh)
  sets it up.

Configuration values written as `${NAME}` or `${NAME:-default}` are read from the environment;
see [docs/getting-started.md](../../docs/getting-started.md).

The image does not load Go plugins (`.so` files need cgo and glibc). Build the binary with
`CGO_ENABLED=1` on a Debian base if you need plugin upload.
