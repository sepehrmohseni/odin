#!/usr/bin/env bash
# Builds the example middleware plugins into build/plugins (or the directory given as $1).
#
# They are built inside this checkout's module, so they use exactly the gateway's versions of
# Echo and the other shared packages: a plugin built with any other version fails to load
# ("plugin was built with a different version of package"). Build the gateway from the same
# checkout with CGO_ENABLED=1.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
out="${1:-$root/build/plugins}"
mkdir -p "$out"
cd "$root"

for dir in examples/middleware-plugins/*/; do
	name="$(basename "$dir")"
	CGO_ENABLED=1 go build -buildmode=plugin -o "$out/$name.so" "./$dir"
	echo "built $out/$name.so"
done
