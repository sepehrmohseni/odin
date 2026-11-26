#!/bin/sh
# Odin's checks, run inside the Go CI image (docker/ci/Dockerfile) from the read-only checkout at
# /src. The steps are the same as .github/workflows/ci.yml. See scripts/docker-check.sh.
set -eu

usage() {
  cat <<'EOF'
usage: check.sh [all|build|fmt|vet|test|cover|lint|vuln|docs|fix|upgrade|shell|-- command...]
  all     mod verify, gofmt, vet, build, race tests, golangci-lint, govulncheck (what CI runs)
  build   go build ./... and the gateway binaries
  fmt     list files gofmt would change (fails if any)
  vet     go vet ./...
  test    go test -race ./...
  cover   tests with coverage of ./pkg/... and a per-function summary
  lint    golangci-lint run
  vuln    govulncheck ./... (needs network for the vulnerability database)
  docs    check that docs/reference/configuration.md matches the config schema
  fix     gofmt -w and go mod tidy, written to the checkout (mounted read-write for this)
  upgrade go get -u ./... and go mod tidy, written to the checkout
  shell   an interactive shell in the prepared copy
  -- cmd  run any command in the prepared copy
EOF
}

# The two tasks that change the checkout run in it directly; scripts/docker-check.sh mounts it
# read-write only for them.
case "${1:-}" in
  fix)
    cd /src
    printf '\n\033[1m==> gofmt -w . && go mod tidy\033[0m\n'
    gofmt -w .
    exec go mod tidy
    ;;
  upgrade)
    cd /src
    printf '\n\033[1m==> go get -u ./... && go mod tidy\033[0m\n'
    go get -u ./...
    exec go mod tidy
    ;;
esac

# Work on a private copy, so the checkout stays untouched and build outputs stay in the container.
W=/work/odin
mkdir -p "$W"
tar -C /src \
  --exclude=./.git --exclude=./bin --exclude=./coverage.out --exclude=./coverage.html \
  --exclude=./test/services/node_modules \
  -cf - . | tar -C "$W" -xf -
cd "$W"

step() {
  printf '\n\033[1m==> %s\033[0m\n' "$*"
  "$@"
}

modverify() { step go mod verify; }

fmtcheck() {
  printf '\n\033[1m==> gofmt -l .\033[0m\n'
  out="$(gofmt -l . 2>&1)"
  if [ -n "$out" ]; then
    echo "$out"
    echo "These files need gofmt; run: gofmt -w <file>" >&2
    return 1
  fi
}

vet() { step go vet ./...; }

build() {
  step go build ./...
  mkdir -p bin
  step go build -o bin/odin ./cmd/odin
  step go build -o bin/odin-migrate ./cmd/migrate
}

test_race() { step go test -race -count=1 ./...; }

cover() {
  step go test -count=1 -covermode=atomic -coverpkg=./pkg/... -coverprofile=coverage.out ./...
  go tool cover -func=coverage.out | tail -n 1
}

lint() { step golangci-lint run ./...; }

vuln() { step govulncheck ./...; }

# The configuration reference is generated from pkg/config; it must be committed up to date.
docs() {
  printf '\n\033[1m==> go run ./tools/configref (docs/reference/configuration.md up to date)\033[0m\n'
  go run ./tools/configref >/tmp/configuration.md
  if ! diff -u docs/reference/configuration.md /tmp/configuration.md; then
    echo "docs/reference/configuration.md is stale; run: scripts/docker-check.sh -- go run ./tools/configref > docs/reference/configuration.md" >&2
    return 1
  fi
  # The demo page shows the document the quickstart stack serves; it must match config/compose.yaml.
  printf '\n\033[1m==> odin -openapi (docs/demo/openapi.json up to date)\033[0m\n'
  MONGO_ROOT_USERNAME=x MONGO_ROOT_PASSWORD=x go run ./cmd/odin -config config/compose.yaml -openapi json \
    2>/dev/null >/tmp/openapi.json
  if ! diff -u docs/demo/openapi.json /tmp/openapi.json; then
    echo "docs/demo/openapi.json is stale; run: scripts/docker-check.sh -- sh -c 'MONGO_ROOT_USERNAME=x MONGO_ROOT_PASSWORD=x go run ./cmd/odin -config config/compose.yaml -openapi json' > docs/demo/openapi.json" >&2
    return 1
  fi
}

task="${1:-all}"
[ "$#" -gt 0 ] && shift
case "$task" in
  all)
    modverify
    fmtcheck
    vet
    build
    test_race
    lint
    vuln
    docs
    printf '\n\033[1;32mAll checks passed.\033[0m\n'
    ;;
  build) build ;;
  fmt) fmtcheck ;;
  vet) vet ;;
  test) test_race ;;
  cover) cover ;;
  lint) lint ;;
  vuln) vuln ;;
  docs) docs ;;
  shell) exec bash ;;
  --) exec "$@" ;;
  -h | --help | help) usage ;;
  *)
    usage >&2
    exit 2
    ;;
esac
