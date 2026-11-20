#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Runs a command in the light image (light/Containerfile) against this checkout: the checkout mounted at its own path,
# the image's user, no host git or shell configuration. It builds the image first when this host does not have it.
#   light/run.sh [COMMAND...]     default: make -f light/light.mk -k -j3, the light run
#   light/run.sh --tag            print the tag of this Containerfile for this user (a hash of the file, and the uid)
#   light/run.sh --build [IMAGE]  build the image (default name: pig-light:<tag>)
# Environment:
#   CONTAINER_ENGINE        podman or docker (default: whichever is installed, podman first)
#   PIG_LIGHT_IMAGE         the image to run (default pig-light:<tag>; CI names the one it pulled)
#   PIG_LIGHT_CHECKOUT      the path the checkout is mounted at (default: its own path). Go and cargo key their caches
#                           on source paths, so checkouts mounted at one path share warm caches.
#   PIG_LIGHT_CACHE_ROOT    a host directory that holds the caches (Go, npm, cargo, the Pi the tests compare with)
#                           instead of the named volumes pig-light-*; CI restores and saves that directory
#   PIG_LIGHT_ENV           names of host environment variables to pass into the container (space-separated)
#   PIG_LIGHT_ENGINE_ARGS   further arguments for the engine's run; "--cpus 4 --memory 16g" is a GitHub-hosted runner's size
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
engine="${CONTAINER_ENGINE:-$(command -v podman || command -v docker)}"
tag="$(sha256sum < "$here/Containerfile" | cut -c1-16)-u$(id -u)"

case "${1:-}" in
  --tag) echo "$tag"; exit 0 ;;
  --build) exec "$engine" build -t "${2:-pig-light:$tag}" --build-arg RUNNER_UID="$(id -u)" -f "$here/Containerfile" "$here" ;;
esac

image="${PIG_LIGHT_IMAGE:-pig-light:$tag}"
"$engine" image inspect "$image" >/dev/null 2>&1 || "$0" --build "$image"
[ "$#" -gt 0 ] || set -- make -f light/light.mk -k -j3

# A linked worktree's .git names the main repository's git directory; mount that too, at its own path.
checkout="${PIG_LIGHT_CHECKOUT:-$root}"
mounts=(-v "$root:$checkout")
common="$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)"
case "$common" in "$root"/*) ;; *) mounts+=(-v "$common:$common") ;; esac

user=()
case "$engine" in *podman) user=(--userns=keep-id) ;; *) user=(--user "$(id -u):$(id -g)") ;; esac

tty=()
[ -t 0 ] && [ -t 1 ] && tty=(-it)

caches=()
if [ -n "${PIG_LIGHT_CACHE_ROOT:-}" ]; then
  for dir in .cache go .cargo/registry .cargo/git .npm; do
    mkdir -p "$PIG_LIGHT_CACHE_ROOT/$dir"
    caches+=(-v "$PIG_LIGHT_CACHE_ROOT/$dir:/home/runner/$dir")
  done
else
  caches=(-v pig-light-cache:/home/runner/.cache -v pig-light-go:/home/runner/go
    -v pig-light-cargo-registry:/home/runner/.cargo/registry -v pig-light-cargo-git:/home/runner/.cargo/git
    -v pig-light-npm:/home/runner/.npm)
fi

envs=(-e CI=1 -e TERM=xterm-256color)
for name in ${PIG_LIGHT_ENV:-}; do envs+=(-e "$name"); done
read -r -a extra <<<"${PIG_LIGHT_ENGINE_ARGS:-}"

exec "$engine" run --rm "${tty[@]}" "${user[@]}" --init \
  --shm-size=2g \
  --pids-limit=-1 \
  "${mounts[@]}" \
  "${caches[@]}" \
  "${envs[@]}" \
  "${extra[@]}" \
  -w "$checkout" \
  "$image" "$@"
