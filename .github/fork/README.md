<!-- SPDX-License-Identifier: MIT -->

# Fork automation (Patrick-Kappen/PiG only)

The fork keeps two lines, and these files keep them current. Every workflow here starts with `if: github.repository == 'Patrick-Kappen/PiG'`, so it does nothing in another repository, and every file is new, so an upstream sync never conflicts with it.

| Line | Follows | Built by MyPiglets as |
|---|---|---|
| `main` | upstream `MichaelKinsy/PiG` main, plus the fixes meant for upstream | `pig` |
| `performance` | the fork's `main`, plus the performance patches | `pig-test` |

## Workflows

- `fork-sync.yml` (every 30 minutes, and by hand) runs `sync.sh` twice: upstream main into `main` (branch `sync/upstream`), then `main` into `performance` (branch `sync/performance`). Each opens a pull request that merges itself, as a merge commit, once the required checks pass, with every incoming `changelog.d` release note and every commit on its own line. GitHub's merge API makes the merge commits, so they are Verified. A conflict only in derived files (`automation/ci/autofix-paths.txt`) is resolved by keeping the target's version, and CI's autofix regenerates them on the pull request. Any other conflict opens one issue (label `sync-conflict:<name>`) with the files and the commands; resolve it on the sync branch, and the next run opens the pull request.
- `fork-notify.yml` (a push to `main` or `performance`) sends `pig-updated` to MyPiglets, whose `update-pig` moves `pig` and `pig-test`.
- `fork-forward.yml` (by hand: pull request number, target `main` or `upstream`) cherry-picks a pull request's commits 1:1 onto the fork's `main` or upstream main, signs them with the fork signing key, and opens the pull request there (`forward.sh`).

## Settings and secrets

- Release App (`patrick-kappen-release`): variable `RELEASE_APP_CLIENT_ID`, secret `RELEASE_APP_PRIVATE_KEY`; permissions contents, pull requests, issues and workflows write.
- `UPSTREAM_PR_TOKEN`: a classic token with `public_repo`, for pull requests in `MichaelKinsy/PiG`.
- `FORK_SIGNING_KEY`: the private SSH key whose public half is a signing key of the fork owner's account.
- Auto-merge allowed; rulesets on `main` and `performance` require `CI result` and `Security result`.
- One inherited file is changed: `ci.yml`'s commit sign-off check skips a pull request from a `sync/` branch. Upstream squash-merges through GitHub, and most of those commits carry no sign-off of their own; upstream checks the sign-offs on its pull requests before the squash. A forward pull request still gets the check.
- Disabled inherited workflows (no file is changed): `release-candidate`, `npm-publish`, `mirror-ci-tools`, `ci-images`, `live-providers`, `discord-notify`, `scorecard`, `nightly`, `startup-performance`.
