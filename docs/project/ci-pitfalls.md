<!--
SPDX-License-Identifier: MIT
-->

# CI pitfalls

One line per failure that reached CI and could have been caught before the push, or that looks like a code failure but is not one. Each line gives the symptom, the cause, and the local remedy. The linked documents remain the authority for each rule; this list only points at them. Add a line when a new kind of failure costs a CI round.

Before every push, run `make ci-bookkeeping-pr ci-preflight ci-drift ci-contracts` (the contract gate of [Test economy](test-economy.md)). It catches most of the items below in minutes, where each missed one costs a CI round of an hour or more.

## Pre-flight

- `template` ❌: the PR body lacks `Tracking:`, `Depends on:`, `Fix:` or `User-facing:`; start from `.github/pull_request_template.md`.
- `signoff` ❌: a commit, often a quick fixup, was made without `git commit --signoff`.
- `up-to-date` ❌: the branch is behind `main`; rebase on `origin/main` before you push.
- `changelog` ❌: a fix or user-facing change has no fragment; add `changelog.d/<lane>-<sha>.md` ([Contributing](../../.github/CONTRIBUTING.md), step 9).
- A rejected pre-flight skips every heavy job; read the pre-flight comment on the pull request, not the red `CI result`.

## Bookkeeping

- `TestChangelogFragmentsFollowTheRules`: the fragment name or a `<!-- fix: <sha> -->` marker names a commit that a rebase, squash or amend replaced; rename it to the new SHA.
- `bug-ledger-check` (outside a pull request, `make ci-bookkeeping-reports`): a commit titled as a fix has no ledger entry; run `make bug-ledger` on `main` and commit both ledger files.
- `divergence-quality` `missing SCRUTINIZED:approved`: a new `D<n>` record in `docs/additive-features.md` or `docs/parity/DIVERGENCES.md` lacks its approval marker.
- `sdk-bundles-drift`: an SDK source or dependency changed without regenerating its compressed copy; run `make sdk-bundles` (autofix has no generator for it).
- `##[error]note: test/parity/testprune-reviewed.toml: prunable ledger entry`: a note, not the failure; look for the `make: *** [...] Error` line.

## Contracts and test-fast

- `interface-go-drift`: a new exported Go type or field without regenerating `test/parity/interfaces/pig-go.json`; run `make interface-go`.
- `TestJSONTags_NoExtraGoFieldsBeyondUpstream`: a new JSON field that Pi lacks; add it to `allowedExtraJSONFieldDivergences` with its `D<n>` and run `go test ./test/upstream-parity`.
- An optional wire field serialized on every event (`"terminate":false`): emit it only when it is set, so an unchanged event keeps Pi's shape.
- `TestNodeRuntimeArchiveMatchesSources` or `TestVendoredPiDistMatchesThePinnedPackage`: `runtime-node` sources or their npm lock changed without `go generate ./coding/extension/host/subprocess`.
- `TestNpmLocksPinPatchedCopiesInsteadOfBundledOnes`: an npm lock bump pins packages that npm bundles and the install removes.

## Not a code failure

- A macOS or Windows job `cancelled` with no steps after about 15 minutes: GitHub had no hosted runner; after the run ends, `gh run rerun <run> --failed`.
- `CI result` and `Linux verification` are aggregates; open the job they name.
- `chord/next/host` `TestTheDependencyCheckSettlesAWaitingRingAcrossHostsAfterOneRound` reports `dependency cycle` instead of `is not served here`: a known flake; rerun the job.
- A checks watcher started right after a push can see no checks yet; wait until GitHub registers them.

## Local environment

- `make parity-deps` fails with `EINTEGRITY`: a Node build linked against system zlib (for example Nix `nodejs-slim`) packs different tarball bytes; use the official Node binary of `.node-version`.
- Lisp SDK tests need SBCL from `automation/dev/toolchains.lock`; install it with `automation/ci/install-sbcl.sh`.
- `TestChangedPackagesSelectsTheReverseClosure` hangs when the global Git configuration sets `tag.gpgsign = true`; disable it for the test run.
- Tests that use `/bin/bash`, `/bin/sleep` or `PATH=/usr/bin:/bin` fail on hosts without those paths (NixOS); CI's Ubuntu passes them, so a broad local `ci-test-fast` on such a host is not a pass signal either way.
- Rust transport tests fetch crates with cargo; without network they fail before testing anything.
- In a fork clone, `gh` can resolve to the parent repository; run `gh repo set-default <owner>/<repo>` before `gh pr create`.
