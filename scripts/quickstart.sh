#!/usr/bin/env bash
# First-run setup for Odin: creates .env with random secrets (if there is none), builds and starts
# the development stack (Odin, Redis, MongoDB, four demo backends) with Docker Compose, waits
# until everything is healthy, and prints where to go next: the admin login, sample requests and
# how to stop it. Works on Linux and macOS with Docker.
#
#   scripts/quickstart.sh                 start (or restart) the stack
#   scripts/quickstart.sh --observability also start Prometheus and Grafana
#   scripts/quickstart.sh --status        show container health
#   scripts/quickstart.sh --down          stop the stack (keeps data)
#   scripts/quickstart.sh --purge         stop the stack and delete its data volumes
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

say() { printf '\033[1m%s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "Docker is not installed (https://docs.docker.com/get-docker/)"
docker info >/dev/null 2>&1 || die "Docker is not running, or this user cannot use it"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required (the 'docker compose' command)"

profiles=()
case "${1:-}" in
  "") ;;
  --observability) profiles=(--profile observability) ;;
  --status) exec docker compose ps ;;
  --down) exec docker compose down ;;
  --purge) exec docker compose down -v ;;
  -h | --help) sed -n '2,12p' "$0"; exit 0 ;;
  *) die "unknown option: $1 (see --help)" ;;
esac

random_secret() {
  LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c "$1"
}

if [ ! -f .env ]; then
  say "Creating .env with random secrets (kept out of git)"
  sed \
    -e "s/^ODIN_JWT_SECRET=.*/ODIN_JWT_SECRET=$(random_secret 48)/" \
    -e "s/^ODIN_ADMIN_PASSWORD=.*/ODIN_ADMIN_PASSWORD=$(random_secret 20)/" \
    -e "s/^MONGO_ROOT_PASSWORD=.*/MONGO_ROOT_PASSWORD=$(random_secret 32)/" \
    -e "s/^GRAFANA_ADMIN_PASSWORD=.*/GRAFANA_ADMIN_PASSWORD=$(random_secret 20)/" \
    .env.example >.env
  chmod 600 .env
else
  say "Using the existing .env"
fi

# Read a value from .env without executing it.
env_value() {
  sed -n "s/^$1=//p" .env | tail -n 1
}

say "Building and starting the stack (the first build takes a few minutes)"
docker compose ${profiles[@]+"${profiles[@]}"} up -d --build --wait

port="$(env_value ODIN_PORT)"
port="${port:-8080}"
user="$(env_value ODIN_ADMIN_USERNAME)"
user="${user:-admin}"

if ! curl -fsS "http://localhost:$port/health" >/dev/null 2>&1; then
  die "Odin did not answer on http://localhost:$port/health; see: docker compose logs odin"
fi

cat <<EOF

$(say "Odin is running.")

  Admin:     http://localhost:$port/admin
             user: $user
             password: $(env_value ODIN_ADMIN_PASSWORD)   (from .env)

  Try it:    curl http://localhost:$port/api/products
             curl http://localhost:$port/api/users
             curl -i http://localhost:$port/api/orders          # 401: needs a JWT
  Health:    curl http://localhost:$port/health
  Metrics:   curl http://localhost:$port/metrics
EOF
if [ ${#profiles[@]} -gt 0 ]; then
  cat <<EOF
  Grafana:   http://localhost:3000   (admin / GRAFANA_ADMIN_PASSWORD in .env)
  Prometheus http://localhost:9090
EOF
fi
cat <<EOF

  Routes are in config/compose.yaml; edit it and run this script again to apply changes.
  Logs:      docker compose logs -f odin
  Stop:      scripts/quickstart.sh --down      (add --purge to delete the data)
EOF
