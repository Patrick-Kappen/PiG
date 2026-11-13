#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Fork only (Patrick-Kappen/PiG): carries the commits of one pull request of this repository 1:1 to another line.
#
#   forward.sh <pull-request-number> <main|upstream> <draft:true|false>
#
#   main      a pull request in this repository onto main (a performance patch that main takes too)
#   upstream  a pull request in MichaelKinsy/PiG onto its main, from the branch forward/upstream/pr-<n> of this repository
#
# The pull request's own commits (merge commits left out) are cherry-picked in order onto the target's head and
# signed with the fork signing key (FORK_SIGNING_KEY), so every commit is Verified; author, message and Signed-off-by
# stay as they were. The new pull request has the original title and body, and the original gets a comment with its
# link. A conflict stops the run and comments the conflicting files on the original pull request.
#
# Needs GH_TOKEN (the release App, this repository), UPSTREAM_TOKEN for target upstream (a classic token with
# public_repo: an App token cannot open a pull request in another account's repository), and git set up to sign.
set -euo pipefail

pr=$1 target=$2 draft=${3:-false}
repo=${GITHUB_REPOSITORY:?}
upstream=MichaelKinsy/PiG

log() { printf '%s\n' "$*" >&2; }

info=$(gh pr view "$pr" --repo "$repo" --json state,title,body,url,baseRefName,commits)
state=$(jq -r .state <<<"$info")
[[ $state == MERGED || $state == OPEN ]] || {
	log "#$pr is $state"
	exit 1
}
title=$(jq -r .title <<<"$info")
base_ref=$(jq -r .baseRefName <<<"$info")
mapfile -t commits < <(jq -r '.commits[].oid' <<<"$info")
git fetch --quiet --no-tags origin "${commits[@]}"

case $target in
main)
	git fetch --quiet --no-tags origin main
	base=$(git rev-parse FETCH_HEAD)
	target_repo=$repo
	;;
upstream)
	git fetch --quiet --no-tags "https://github.com/$upstream.git" main
	base=$(git rev-parse FETCH_HEAD)
	target_repo=$upstream
	;;
*)
	log "target must be main or upstream, not $target"
	exit 2
	;;
esac
branch="forward/$target/pr-$pr"

git switch --quiet --force-create "$branch" "$base"
picked=0
for commit in "${commits[@]}"; do
	if [[ $(git rev-list --parents -n 1 "$commit" | wc -w) -gt 2 ]]; then
		log "skip merge commit $commit"
		continue
	fi
	if ! git cherry-pick --gpg-sign --allow-empty --keep-redundant-commits "$commit" >/dev/null; then
		# shellcheck disable=SC2016 # Markdown backticks, not expansions
		files=$(git diff --name-only --diff-filter=U | sed 's/^/- `/; s/$/`/')
		git cherry-pick --abort || true
		# shellcheck disable=SC2016 # Markdown backticks, not expansions
		gh pr comment "$pr" --repo "$repo" --body "$(printf 'Forward to **%s** stopped: `%s` does not apply to %s@`%s`.\n\nConflicting files:\n%s\n\nCherry-pick the commits onto `%s` by hand (`git cherry-pick -S`) and open the pull request from `%s`.' \
			"$target" "${commit:0:9}" "$target_repo" "${base:0:9}" "$files" "$target" "$branch")" >/dev/null
		log "conflict at $commit"
		exit 1
	fi
	picked=$((picked + 1))
done
[[ $picked -gt 0 ]] || {
	log "#$pr has no commit to forward"
	exit 1
}
git push --quiet --force origin "$branch"

body=$(mktemp)
{
	jq -r .body <<<"$info"
	echo
	echo "---"
	echo
	echo "Forwarded 1:1 from $(jq -r .url <<<"$info") (base \`$base_ref\`): $picked commit(s) cherry-picked onto $target_repo@\`${base:0:9}\`."
} >"$body"
draft_flag=()
[[ $draft == true ]] && draft_flag=(--draft)
if [[ $target == upstream ]]; then
	existing=$(GH_TOKEN=$UPSTREAM_TOKEN gh pr list --repo "$upstream" --head "$branch" --state open --json url --jq '.[0].url // empty')
	if [[ -n $existing ]]; then
		url=$existing
	else
		url=$(GH_TOKEN=$UPSTREAM_TOKEN gh pr create --repo "$upstream" --base main --head "${repo%%/*}:$branch" \
			--title "$title" --body-file "$body" "${draft_flag[@]}")
	fi
else
	existing=$(gh pr list --repo "$repo" --head "$branch" --base main --state open --json url --jq '.[0].url // empty')
	if [[ -n $existing ]]; then
		url=$existing
	else
		url=$(gh pr create --repo "$repo" --base main --head "$branch" --title "$title" --body-file "$body" "${draft_flag[@]}")
	fi
fi
rm -f "$body"
gh pr comment "$pr" --repo "$repo" --body "Forwarded to **$target**: $url" >/dev/null
log "forwarded #$pr to $target: $url"
