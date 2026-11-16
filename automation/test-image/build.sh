#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Builds the Linux test image (automation/test-image/Containerfile) with podman or docker. Every version comes from
# the file that owns it; the build context holds only the pin files and install scripts the image uses.
#   automation/test-image/build.sh [TAG]     default tag: pig-test:local
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tag="${1:-pig-test:local}"
engine="${CONTAINER_ENGINE:-$(command -v podman || command -v docker)}"

go_version="$(tr -d '\r\n' < "$root/.go-version")"
go_row="$(awk -F'\t' -v v="$go_version" '!/^#/ && $1=="go" && $2==v && $3=="linux" && $4=="amd64" {print; exit}' "$root/automation/dev/toolchains.lock")"
[ -n "$go_row" ] || { echo "build.sh: toolchains.lock has no go $go_version linux/amd64 row" >&2; exit 1; }
node_version="$(tr -d '\r\n' < "$root/.node-version")"
rust_version="$(awk '/^  RUST_VERSION:/ { print $2; exit }' "$root/.github/workflows/ci.yml")"
[ -n "$rust_version" ] || { echo "build.sh: no RUST_VERSION in .github/workflows/ci.yml" >&2; exit 1; }

context="$(mktemp -d)"; trap 'rm -rf "$context"' EXIT
mkdir -p "$context/automation/ci" "$context/automation/dev"
cp "$root/.go-version" "$root/.node-version" "$context/"
cp "$root/automation/dev/toolchains.lock" "$context/automation/dev/"
cp "$root"/automation/ci/install-native-test-packages.sh "$root"/automation/ci/install-apt-packages.sh \
   "$root"/automation/ci/build-libxcb.sh "$root"/automation/ci/install-sbcl.sh "$context/automation/ci/"

exec "$engine" build -t "$tag" -f "$root/automation/test-image/Containerfile" \
  --build-arg GO_VERSION="$go_version" \
  --build-arg GO_URL="$(cut -f6 <<<"$go_row")" \
  --build-arg GO_SHA256="$(cut -f7 <<<"$go_row")" \
  --build-arg NODE_VERSION="$node_version" \
  --build-arg RUST_VERSION="$rust_version" \
  "$context"
