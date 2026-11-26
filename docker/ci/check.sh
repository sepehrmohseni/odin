#!/bin/sh
# Odin's checks, run inside the Go CI image (docker/ci/Dockerfile) from the read-only checkout at
# /src. The steps are the same as .github/workflows/ci.yml. See scripts/docker-check.sh.
set -eu

usage() {
  cat <<'EOF'
usage: check.sh [all|build|fmt|vet|test|cover|lint|vuln|fix|upgrade|shell|-- command...]
  all     mod verify, gofmt, vet, build, race tests, golangci-lint, govulncheck (what CI runs)
  build   go build ./... and the gateway binaries
  fmt     list files gofmt would change (fails if any)
  vet     go vet ./...
  test    go test -race ./...
  cover   tests with coverage of ./pkg/... and a per-function summary
  lint    golangci-lint run
  vuln    govulncheck ./... (needs network for the vulnerability database)
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
    printf '\n\033[1;32mAll checks passed.\033[0m\n'
    ;;
  build) build ;;
  fmt) fmtcheck ;;
  vet) vet ;;
  test) test_race ;;
  cover) cover ;;
  lint) lint ;;
  vuln) vuln ;;
  shell) exec bash ;;
  --) exec "$@" ;;
  -h | --help | help) usage ;;
  *)
    usage >&2
    exit 2
    ;;
esac
