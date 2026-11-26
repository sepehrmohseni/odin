#!/usr/bin/env bash
# Runs Odin's CI checks in Docker, so they work the same on any Linux or macOS machine with Docker,
# on x86_64 or arm64, without installing Go or the linters.
#
#   scripts/docker-check.sh            mod verify, gofmt, vet, build, race tests, lint, govulncheck
#   scripts/docker-check.sh build      go build ./... and the gateway binaries
#   scripts/docker-check.sh test       go test -race ./...
#   scripts/docker-check.sh cover      tests with a coverage summary for ./pkg/...
#   scripts/docker-check.sh lint       golangci-lint run
#   scripts/docker-check.sh vuln       govulncheck ./...
#   scripts/docker-check.sh fmt        list files that need gofmt
#   scripts/docker-check.sh fix        gofmt -w and go mod tidy (writes to the checkout)
#   scripts/docker-check.sh upgrade    go get -u ./... and go mod tidy (writes to the checkout)
#   scripts/docker-check.sh shell      a shell in the prepared container
#   scripts/docker-check.sh -- CMD     any command, for example: -- go test ./test/unit/pkg/auth/
#
# The checkout is mounted read-only and copied inside the container, so nothing is written to it;
# only `fix` and `upgrade` mount it read-write, because changing it is their job.
#
# The image (docker/ci/Dockerfile) holds only the Go toolchain and linters, not Odin's code, so it
# is tagged by tool versions and other Go projects can use the same image and cache volumes:
# go-ci-gomod (module cache), go-ci-gobuild (build cache) and go-ci-golangci (lint cache). Remove
# them with: docker volume rm go-ci-gomod go-ci-gobuild go-ci-golangci
#
# Settings (environment variables):
#   GO_CI_CPUS      CPU cores to use (default: half of them, so the machine stays usable)
#   GO_CI_IMAGE     image tag (default: go-ci:<go version>-lint<golangci-lint version>)
#   GO_CI_GO_IMAGE  Go base image, for checking with another Go release (e.g. golang:1.26.11-trixie)
#   GO_CI_VOLUMES   prefix of the cache volume names (default: go-ci)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dockerfile_dir="$root/docker/ci"
cores="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)"
cpus="${GO_CI_CPUS:-$(( cores > 1 ? cores / 2 : 1 ))}"
volumes="${GO_CI_VOLUMES:-go-ci}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is not installed or not on PATH" >&2
  exit 1
fi

# The default tag names the tool versions pinned in the Dockerfile, so projects that pin the same
# versions share one image.
arg_default() {
  sed -n "s/^ARG $1=//p" "$dockerfile_dir/Dockerfile" | head -n 1
}
go_image="${GO_CI_GO_IMAGE:-$(arg_default GO_IMAGE)}"
go_version="${go_image#golang:}"
go_version="${go_version%%-*}"
image="${GO_CI_IMAGE:-go-ci:go${go_version}-lint$(arg_default GOLANGCI_LINT_VERSION)}"

build=(docker build --tag "$image" --build-arg "GO_IMAGE=$go_image")
if docker image inspect "$image" >/dev/null 2>&1; then
  # Already built: a quick, quiet rebuild that only redoes layers whose inputs changed.
  "${build[@]}" --quiet "$dockerfile_dir" >/dev/null
else
  echo "Building the $image image once (downloads Go, golangci-lint and govulncheck)..." >&2
  "${build[@]}" "$dockerfile_dir" >&2
fi

tty=()
if [ -t 0 ] && [ -t 1 ]; then
  tty=(--interactive --tty)
fi

src_mode=ro
case "${1:-}" in
  fix | upgrade) src_mode=rw ;;
esac

# Running as your own user id keeps the read-only checkout readable even with a private umask,
# and files written by `fix`/`upgrade` owned by you.
# The ",z" label lets SELinux hosts (Fedora, RHEL) share the mount; elsewhere it is ignored.
# GOFLAGS=-p and GOMAXPROCS keep the compiler and tests within the CPU share given to Docker.
exec docker run --rm ${tty[@]+"${tty[@]}"} \
  --cpus "$cpus" \
  --env "GOFLAGS=-p=$cpus" \
  --env "GOMAXPROCS=$cpus" \
  --user "$(id -u):$(id -g)" \
  --volume "$root:/src:$src_mode,z" \
  --volume "$volumes-gomod:/cache/gomod" \
  --volume "$volumes-gobuild:/cache/gobuild" \
  --volume "$volumes-golangci:/cache/golangci" \
  "$image" sh /src/docker/ci/check.sh "$@"
