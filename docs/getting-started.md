# Getting started

From nothing to a request proxied through Odin, and a look at the admin interface. Pick one way
to install:

| Way | Needs | Best for |
| --- | --- | --- |
| [Quickstart script](#1-quickstart-script) | Docker with Compose v2 | Trying Odin with demo backends and the admin UI |
| [Docker image](#2-docker-image) | Docker | Running Odin with your own config |
| [Binary](#3-binary) | Linux or macOS | Running without containers |
| [Helm](#4-kubernetes-helm) | A Kubernetes cluster | Kubernetes |

## 1. Quickstart script

```bash
git clone https://github.com/sepehrmohseni/odin.git
cd odin
scripts/quickstart.sh
```

The script:

1. checks that Docker and Compose v2 are available;
2. creates `.env` from [.env.example](../.env.example) with random secrets (JWT signing key,
   admin password, MongoDB password), mode 0600 and ignored by git;
3. builds and starts the stack in [docker-compose.yml](../docker-compose.yml): Odin, Redis,
   MongoDB and four demo backends (users, products, orders, categories);
4. waits until every container is healthy and prints the admin URL, the login and sample
   requests.

The first run builds the images, which takes a few minutes. Then:

```bash
curl http://localhost:8080/api/products         # proxied to the products backend
curl -i http://localhost:8080/api/orders        # 401: this route requires a JWT
curl http://localhost:8080/metrics | head       # Prometheus metrics
```

Open **http://localhost:8080/admin** and log in with the username and password the script
printed (they are in `.env`). The admin shows the dashboard, the configured services,
real-time monitoring, traces and settings. Secrets are masked in the settings views. After five
wrong passwords from one address, logins are blocked for 15 minutes.

To call the JWT-protected route, sign a token with `ODIN_JWT_SECRET` from `.env`. Tokens must
have an `exp` claim. For example, with the bundled generator:

```bash
ODIN_JWT_SECRET="$(sed -n 's/^ODIN_JWT_SECRET=//p' .env)" go run ./test/auth   # needs Go
curl -H "Authorization: Bearer <token>" http://localhost:8080/api/orders
```

The routes live in [config/compose.yaml](../config/compose.yaml). Edit it and run
`scripts/quickstart.sh` again to apply. Other commands:

```bash
scripts/quickstart.sh --status          # container health
scripts/quickstart.sh --observability   # also Prometheus (:9090) and Grafana (:3000)
docker compose logs -f odin             # gateway logs
scripts/quickstart.sh --down            # stop (keeps data)
scripts/quickstart.sh --purge           # stop and delete the volumes
```

## 2. Docker image

The image runs a configuration with no routes (health and metrics only) until you give it
yours:

```bash
docker build -t odin .
cat > my-config.yaml <<'YAML'
server:
  port: 8080
services:
  - name: httpbin
    basePath: /httpbin
    stripBasePath: true
    targets: [https://httpbin.org]
YAML
docker run --rm -p 8080:8080 -v "$PWD/my-config.yaml:/app/config/config.yaml:ro" odin
curl http://localhost:8080/httpbin/get
```

To turn on the admin interface, add `-e ODIN_ADMIN_ENABLED=true -e ODIN_ADMIN_PASSWORD=...`
(with the built-in config), or `admin: {enabled: true}` in your file. Any value in the config can
be taken from the environment as `${NAME}` (required) or `${NAME:-default}`.

## 3. Binary

Download the archive for your platform from the
[releases page](https://github.com/sepehrmohseni/odin/releases) (once releases are published),
or build it with Go 1.26 or newer:

```bash
make build                      # bin/odin
cp config/config.example.yaml config/config.yaml
ODIN_JWT_SECRET="$(openssl rand -hex 32)" ./bin/odin -config config/config.yaml
```

Run the binary from the repository (or the unpacked archive) directory, since the admin
interface reads `pkg/admin/templates` and `pkg/admin/static` relative to the working directory.
With the admin enabled and no password set, Odin generates one at startup and prints it once in
the log.

## 4. Kubernetes (Helm)

```bash
helm install odin deployments/helm/odin \
  --set secrets.jwtSecret="$(openssl rand -hex 32)" \
  --set config.admin.enabled=true \
  --set secrets.adminPassword="$(openssl rand -base64 18)" \
  --set-json 'services=[{"name":"api","basePath":"/api","targets":["http://api.default.svc:80"]}]'
kubectl port-forward svc/odin 8080:8080    # then http://localhost:8080/admin
```

See the [chart README](../deployments/helm/odin/README.md) for all values.

## Your first routes

A service maps a path prefix to one or more targets:

```yaml
services:
  - name: products
    basePath: /api/products     # requests under this prefix
    targets:                    # are sent to one of these, round-robin
      - http://products-1:8080
      - http://products-2:8080
    stripBasePath: false        # true sends /api/products/7 as /7
    timeout: 10s                # wait for the upstream's response headers
    retryCount: 2               # GET/HEAD/OPTIONS (or Idempotency-Key) only
    healthCheck: {enabled: true, path: /health}
    rateLimit: {limit: 100, window: 1m}
```

Protect a service with JWTs by setting `authentication: true` and a signing secret
(`ODIN_JWT_SECRET`). Every option is described in [configuration.md](configuration.md).

## Admin interface: what to know

- It is **off by default** and only served when `admin.enabled` is true.
- There is no built-in password. Set `ODIN_ADMIN_PASSWORD` or `admin.passwordHash` (bcrypt), or
  read the generated one from the startup log.
- Changes made in the admin are written to the config file and apply when the gateway restarts.
  In the Compose stack the config is mounted read-only, so edit `config/compose.yaml` instead.
  Live reload and a writable config store are on the [roadmap](../ROADMAP.md).
- Behind a reverse proxy, list the proxy in `server.trustedProxies` so client IPs (used for
  login throttling and rate limits) are read from `X-Forwarded-For`.
