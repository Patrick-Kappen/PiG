<!-- SPDX-License-Identifier: MIT -->

# Fork automation (Patrick-Kappen/PiG only)

The fork keeps two lines; they move only by hand. Every workflow here starts with `if: github.repository == 'Patrick-Kappen/PiG'`, so it does nothing in another repository, and every file is new, so an upstream sync never conflicts with it.

| Line | Follows | Built by MyPiglets as |
|---|---|---|
| `main` | upstream `MichaelKinsy/PiG` main, plus the fixes meant for upstream | `pig` |
| `performance` | the fork's `main`, plus the performance patches | nothing |

## Workflows

- `fork-sync.yml` (only by hand: Actions → Fork sync → Run workflow) runs `sync.sh` twice: upstream main into `main` (branch `sync/upstream`), then `main` into `performance` (branch `sync/performance`). Each opens a pull request that merges itself, as a merge commit, once the required checks pass, with every incoming `changelog.d` release note and every commit on its own line. GitHub's merge API makes the merge commits, so they are Verified. A conflict only in derived files (`automation/ci/autofix-paths.txt`) is resolved by keeping the target's version, and CI's autofix regenerates them on the pull request; that merge is made in the workflow and signed with `FORK_SIGNING_KEY`, and a failure in it stops the run. Any other conflict opens one issue (label `sync-conflict:<name>`) with the files and the commands; resolve it on the sync branch, and the next run opens the pull request.
- MyPiglets moves its `pig` to the fork's `main` by hand (`nix flake update pig` there); nothing here tells it.
- `fork-forward.yml` (by hand: pull request number, target `main` or `upstream`) cherry-picks a pull request's commits 1:1 onto the fork's `main` or upstream main, signs them with the fork signing key, and opens the pull request there (`forward.sh`).

## Settings and secrets

- Release App (`patrick-kappen-release`): variable `RELEASE_APP_CLIENT_ID`, secret `RELEASE_APP_PRIVATE_KEY`; permissions contents, pull requests, issues and workflows write.
- `UPSTREAM_PR_TOKEN`: a classic token with `public_repo`, for pull requests in `MichaelKinsy/PiG`.
- `FORK_SIGNING_KEY`: the private SSH key whose public half is a signing key of the fork owner's account (`fork-forward.yml`, and `fork-sync.yml` for a merge it makes itself).
- Auto-merge allowed; rulesets on `main` and `performance` require `CI result` and `Security result`.
- Three inherited places are changed, by a few lines each, so that a sync seldom conflicts with them:
  - `ci.yml`'s commit sign-off check skips a pull request from a `sync/` branch. Upstream squash-merges through GitHub, and most of those commits carry no sign-off of their own; upstream checks the sign-offs on its pull requests before the squash. A forward pull request still gets the check.
  - The pre-flight (`automation/ci/preflight`) passes six checks on a sync pull request without deciding them: `template`, `signoff`, `depends-on`, `bug-ledger`, `changelog` and `test-expectation`. They judge a contribution by the description and the own commits of its pull request, and a sync has neither: its commits were judged on the pull requests that made them, a squash merge carries no sign-off of its own, and a changelog fragment names commits the squash replaced. Every other check is decided as on any pull request, the scans included. `exempt.yaml` declares the exemption and `exempt.go` applies it (both new files): it holds only for a pull request that the release App opened from a `sync/` branch of this repository, and the pre-flight's comment names it on each exempt check. Four inherited files call it: `main.go` loads `exempt.yaml`, `evaluate.go` passes an exempt check, `config.go` makes the file part of the run version, and `sources.go` reads the head branch and head repository of a pull request.
  - `.gitleaks.toml` allows `tok-SECRET-123`, the made-up API token of the pre-flight's status retry test (`automation/ci/preflight/record_status_retry_test.go`). The pre-flight scans the lines a pull request adds, and gitleaks reads that constant as a `generic-api-key`. Upstream's `main` has the file, so no pull request there adds the line again. The sync of `main` into `performance` adds every line that `performance` does not have yet, the whole test file among them, and its `gitleaks` check fails until `main` allows the value. The entry is the first of `regexes`; upstream has added its own entries further down the list.
- Disabled inherited workflows (no file is changed): `release-candidate`, `npm-publish`, `mirror-ci-tools`, `ci-images`, `live-providers`, `discord-notify`, `scorecard`, `nightly`, `startup-performance`.
