#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Runs a command in the Linux test image against this checkout, as a Linux CI job runs it: the checkout mounted at its
# own path, the runner user, no host git or shell configuration. Caches (Go, npm, cargo, Pi oracle builds) live in
# named volumes, so a second run is warm. The first run prepares what CI's setup steps prepare: the pinned npm in the
# tree and the Pi oracle (make upstream-mirror).
#   automation/test-image/run.sh [COMMAND...]      default: make test
# Environment: PIG_TEST_IMAGE (default pig-test:local), CONTAINER_ENGINE (podman or docker), PIG_TEST_CHECKOUT (the
# path the checkout is mounted at, default its own path). Go and cargo key their caches on source paths, so checkouts
# mounted at one PIG_TEST_CHECKOUT share warm caches, as CI's jobs do at /home/runner/work/PiG/PiG.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="${PIG_TEST_IMAGE:-pig-test:local}"
engine="${CONTAINER_ENGINE:-$(command -v podman || command -v docker)}"
[ "$#" -gt 0 ] || set -- make test

# A linked worktree's .git names the main repository's git directory; mount that too, at its own path.
checkout="${PIG_TEST_CHECKOUT:-$root}"
mounts=(-v "$root:$checkout")
common="$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)"
case "$common" in "$root"/*) ;; *) mounts+=(-v "$common:$common") ;; esac

user=()
case "$engine" in *podman) user=(--userns=keep-id) ;; *) user=(--user "$(id -u):$(id -g)") ;; esac

tty=()
[ -t 0 ] && [ -t 1 ] && tty=(-it)

# shellcheck disable=SC2016 # expanded by the container's bash, not here
prepare='set -euo pipefail
log="$HOME/.cache/pig-test-prepare.log"
{ automation/ci/install-npm-toolchain.sh && make -s upstream-mirror; } >"$log" 2>&1 || { tail -40 "$log" >&2; exit 1; }
export PATH="$PWD/automation/ci/npm-toolchain/node_modules/.bin:$PATH"
exec "$@"'

exec "$engine" run --rm "${tty[@]}" "${user[@]}" --init \
  --shm-size=2g \
  --pids-limit=-1 \
  "${mounts[@]}" \
  -v pig-test-cache:/home/runner/.cache \
  -v pig-test-gopath:/home/runner/go \
  -v pig-test-cargo-registry:/home/runner/.cargo/registry \
  -v pig-test-npm:/home/runner/.npm \
  -e CI=1 -e TERM=xterm-256color \
  -w "$checkout" \
  "$image" bash -c "$prepare" bash "$@"
