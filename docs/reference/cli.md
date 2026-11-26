# Command line

## odin

```text
odin [-config path] [-version] [-healthcheck]
```

| Flag | Default | What |
| --- | --- | --- |
| `-config` | `config/config.yaml` | Configuration file. `services.yaml` in the same directory is read too. |
| `-version` | | Print the version and build time, then exit. |
| `-healthcheck` | | Request `http://127.0.0.1:$GATEWAY_PORT/health` (port 8080 by default) and exit 0 when it answers 200, 1 otherwise. Used by the Docker `HEALTHCHECK`. |

Environment variables are listed in [Configuration](../configuration.md#environment-variables).

Signals:

| Signal | Effect |
| --- | --- |
| `SIGINT`, `SIGTERM` | Stop accepting connections, finish in-flight requests (up to `server.gracefulTimeout`), then stop health checks, flush traces and close connections. |
| `SIGHUP` | Validate the configuration file and log the result. It does not apply changes; restart for that. |

Exit status is 1 when the configuration cannot be loaded or the listener cannot start.

## odin-migrate

Copies services from YAML files into MongoDB (`mongodb.*` in the config).

```text
odin-migrate [-config path] [-services file] [-routes file] [-mongodb-uri uri]
             [-mongodb-database name] [-dry-run] [-force] [-verbose]
```

| Flag | Default | What |
| --- | --- | --- |
| `-config` | `config/config.yaml` | Config file: MongoDB settings and services to migrate |
| `-services`, `-routes` | | Extra YAML files with `services:` to migrate |
| `-mongodb-uri`, `-mongodb-database` | from config | Override the MongoDB connection |
| `-dry-run` | false | Show what would be migrated |
| `-force` | false | Migrate even if services already exist |
| `-verbose` | false | Debug logging |

The gateway itself does not read services from MongoDB yet; the migration prepares data for that.
