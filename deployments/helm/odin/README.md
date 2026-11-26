# Odin Helm chart

Deploys the Odin API Gateway: a Deployment running the image as uid 65532 with a read-only root
filesystem, a Service, an optional Ingress, HPA and ServiceMonitor, a ConfigMap rendered into
Odin's config schema, and a Secret for the JWT secret and admin password.

```bash
helm install odin deployments/helm/odin \
  --set secrets.jwtSecret="$(openssl rand -hex 32)" \
  --set-json 'services=[{"name":"api","basePath":"/api","targets":["http://api.default.svc:80"]}]'
```

With the admin interface:

```bash
helm install odin deployments/helm/odin \
  --set config.admin.enabled=true \
  --set secrets.adminPassword="$(openssl rand -base64 18)" \
  --set secrets.jwtSecret="$(openssl rand -hex 32)"
kubectl port-forward svc/odin 8080:8080   # then open http://localhost:8080/admin
```

## Values

| Key | Default | Meaning |
| --- | --- | --- |
| `image.repository` / `image.tag` | `ghcr.io/sepehrmohseni/odin` / appVersion | Image to run |
| `config.gateway.port` | `8080` | Listen port (also the Service target) |
| `config.gateway.readTimeout`, `writeTimeout`, `idleTimeout` | `30s`, `30s`, `120s` | Server timeouts |
| `config.gateway.trustedProxies` | `[]` | CIDRs whose `X-Forwarded-For` is trusted (your ingress controller) |
| `config.admin.enabled` / `username` | `false` / `admin` | Admin interface |
| `config.logging.level` / `format` | `info` / `json` | Logging |
| `config.rateLimit`, `config.cache` | `{}` | Copied into config.yaml as they are (see docs/configuration.md) |
| `services` | `[]` | Services in Odin's format, rendered into services.yaml |
| `secrets.jwtSecret`, `secrets.adminPassword` | empty | Stored in the chart's Secret |
| `secrets.existingSecret` | empty | Use your own Secret with keys `jwt-secret` and `admin-password` |
| `monitoring.serviceMonitor.enabled` | `false` | Prometheus Operator ServiceMonitor |

Pods restart when the ConfigMap or Secret changes (checksum annotations).

Chart 0.2.0 replaces 0.1.0, which did not render (`templates/service.yaml` was not valid
template syntax). Its admin port and Redis/Prometheus dependencies are gone, and
`config.admin.password` moved to `secrets.adminPassword`.
