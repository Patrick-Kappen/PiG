#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Fork only (Patrick-Kappen/PiG): brings a source branch into a target branch of this repository through a pull request.
#
#   sync.sh <name> <source-repo> <source-branch> <target-branch>
#
# <name> names the sync: its branch is sync/<name>, its pull request carries the labels "sync" and "sync:<name>", and a
# conflict opens one issue with the label "sync-conflict:<name>".
#
# One run:
#   1. The target already contains the source: nothing to bring. A sync branch without an open pull request is deleted,
#      and an open conflict issue is closed.
#   2. Otherwise the sync branch (created from the target when missing) gets the target and the source merged in with
#      GitHub's merge API, so every merge commit is GitHub's own and Verified. A sync branch with an open pull request or
#      an open conflict issue keeps its commits: a conflict resolved by hand on it stays.
#   3. A conflict (HTTP 409) opens or updates the conflict issue with the commands to resolve it on the sync branch;
#      the next run continues once the sync branch contains the source.
#   4. A sync branch that contains the source gets its pull request, opened or updated, with every incoming commit and
#      every incoming changelog.d release note on its own line, and auto-merge (merge commit) when the repository allows it.
#
# Needs GH_TOKEN with contents, pull requests, issues and workflows write (the release App), and a checkout of this
# repository with full history (git is used only to push the upstream source commit into this repository).
set -euo pipefail

name=$1 source_repo=$2 source_branch=$3 target=$4
repo=${GITHUB_REPOSITORY:?}
sync_branch="sync/$name"
mirror_branch="sync/$name-source"
conflict_label="sync-conflict:$name"
source_owner=${source_repo%%/*}

log() { printf '%s\n' "$*" >&2; }

ensure_label() {
	gh label create "$1" --repo "$repo" --color "$2" --description "$3" >/dev/null 2>&1 || true
}

# ref_sha <repo> <branch>: the commit a branch points at, or empty when it does not exist (gh prints the error body
# of a 404 on stdout, so only a full SHA counts).
ref_sha() {
	local sha
	sha=$(gh api "repos/$1/git/ref/heads/$2" --jq .object.sha 2>/dev/null || true)
	[[ $sha =~ ^[0-9a-f]{40}$ ]] && printf '%s\n' "$sha"
	return 0
}

# compare_status <base> <head>: GitHub's compare status of head against base in this repository's network
# ("identical", "ahead", "behind", "diverged").
compare_status() { gh api "repos/$repo/compare/$1...$2" --jq .status; }

# contains <branch> <commit-spec>: the branch already has the commit.
contains() {
	local status
	status=$(compare_status "$2" "$1")
	[[ $status == identical || $status == ahead ]]
}

source_sha=$(ref_sha "$source_repo" "$source_branch")
target_sha=$(ref_sha "$repo" "$target")
[[ -n $source_sha && -n $target_sha ]] || {
	log "missing branch: $source_repo $source_branch=$source_sha, $repo $target=$target_sha"
	exit 1
}
if [[ $source_repo == "$repo" ]]; then
	source_spec=$source_sha
else
	source_spec="$source_owner:$source_branch"
fi

open_pr=$(gh pr list --repo "$repo" --head "$sync_branch" --base "$target" --state open --json number --jq '.[0].number // empty')
conflict_issue=$(gh issue list --repo "$repo" --label "$conflict_label" --state open --json number --jq '.[0].number // empty')

# 1. Nothing to bring.
if contains "$target_sha" "$source_spec"; then
	log "$target already contains $source_repo $source_branch ($source_sha)"
	if [[ -n $conflict_issue ]]; then
		gh issue close "$conflict_issue" --repo "$repo" --comment "$target contains $source_repo@$source_sha now." >/dev/null
	fi
	if [[ -z $open_pr ]]; then
		gh api -X DELETE "repos/$repo/git/refs/heads/$sync_branch" >/dev/null 2>&1 || true
	fi
	gh api -X DELETE "repos/$repo/git/refs/heads/$mirror_branch" >/dev/null 2>&1 || true
	exit 0
fi

# The merge API merges branches of this repository: a source in another repository of the network is pushed to a
# mirror branch here first. Only objects move; no commit is made.
merge_head=$source_sha
if [[ $source_repo != "$repo" ]]; then
	git fetch --quiet --no-tags "https://github.com/$source_repo.git" "$source_sha"
	git push --quiet --no-verify --force origin "$source_sha:refs/heads/$mirror_branch"
	merge_head=$mirror_branch
else
	git fetch --quiet --no-tags origin "$source_sha"
fi

# 2. The sync branch: kept while a pull request or a conflict issue is open, else started again from the target.
sync_sha=$(ref_sha "$repo" "$sync_branch")
if [[ -z $sync_sha ]]; then
	gh api -X POST "repos/$repo/git/refs" -f ref="refs/heads/$sync_branch" -f sha="$target_sha" >/dev/null
elif [[ -z $open_pr && -z $conflict_issue ]]; then
	gh api -X PATCH "repos/$repo/git/refs/heads/$sync_branch" -f sha="$target_sha" -F force=true >/dev/null
fi

# merge <head> <message>: merges head into the sync branch; returns 1 on a conflict.
merge() {
	local out code
	out=$(gh api -X POST "repos/$repo/merges" -f base="$sync_branch" -f head="$1" -f commit_message="$2" -i 2>&1) || true
	code=$(printf '%s\n' "$out" | sed -n '1s/^HTTP\/[0-9.]* \([0-9]*\).*/\1/p')
	case $code in
	201 | 204) return 0 ;;
	409) return 1 ;;
	*)
		log "merge of $1 into $sync_branch failed: $out"
		exit 1
		;;
	esac
}

# derived <path>: the file is one that CI's Bookkeeping regenerates and autofix.yml commits
# (automation/ci/autofix-paths.txt, shell patterns).
derived() {
	local pattern
	while read -r pattern; do
		[[ -z $pattern || $pattern == \#* ]] && continue
		# shellcheck disable=SC2053 # the pattern is a glob on purpose
		[[ $1 == $pattern ]] && return 0
	done <automation/ci/autofix-paths.txt
	return 1
}

# merge_derived <commit> <message>: merges commit into the sync branch in this checkout when every conflict is in a
# derived file, keeping the sync branch's version of those (CI's autofix regenerates them on the pull request), and
# pushes the merge. Signed when git has a signing key. Sets conflict_files and returns 1 when a conflict needs a human.
conflict_files=
merge_derived() {
	git fetch --quiet --no-tags origin "$sync_branch"
	git switch --quiet --detach FETCH_HEAD
	git merge --quiet --no-ff --no-commit "$1" >/dev/null 2>&1 || true
	local file unresolved=()
	mapfile -t conflicted < <(git diff --name-only --diff-filter=U)
	for file in "${conflicted[@]}"; do
		derived "$file" || unresolved+=("$file")
	done
	if [[ ${#unresolved[@]} -gt 0 ]]; then
		# shellcheck disable=SC2016 # Markdown backticks, not expansions
		conflict_files=$(printf -- '- `%s`\n' "${unresolved[@]}")
		git merge --abort
		return 1
	fi
	if [[ ${#conflicted[@]} -gt 0 ]]; then
		git checkout --quiet --ours -- "${conflicted[@]}"
		git add -- "${conflicted[@]}"
	fi
	local sign=()
	[[ -n $(git config user.signingkey || true) ]] && sign=(-S)
	git commit --quiet "${sign[@]}" -m "$2" -m "Derived files kept from $sync_branch; CI's autofix regenerates them: $(printf '%s ' "${conflicted[@]}")"
	git push --quiet --no-verify origin "HEAD:refs/heads/$sync_branch"
	log "merged with derived-file conflicts resolved: ${conflicted[*]}"
}

short=${source_sha:0:9}
conflict=
if ! contains "$sync_branch" "$target_sha"; then
	merge "$target_sha" "Merge $target into $sync_branch" || merge_derived "$target_sha" "Merge $target into $sync_branch" || conflict="$target"
fi
if [[ -z $conflict ]] && ! contains "$sync_branch" "$source_spec"; then
	message="Merge $source_repo $source_branch $short into $target"
	merge "$merge_head" "$message" || merge_derived "$source_sha" "$message" || conflict="$source_repo $source_branch"
fi

# 3. A conflict: one issue, the commands to resolve it.
if [[ -n $conflict ]]; then
	ensure_label "$conflict_label" d93f0b "The $name sync has a conflict to resolve by hand"
	body=$(
		cat <<EOF
The $name sync cannot merge **$conflict** into \`$sync_branch\` automatically ($source_repo@$source_sha into \`$target\`).

Conflicts that need a hand (conflicts in derived files alone are resolved by the sync, and CI's autofix regenerates them):
$conflict_files

Resolve it on the sync branch; the next sync run (every 30 minutes, or Actions → Fork sync → Run workflow) opens the pull request once \`$sync_branch\` contains $source_repo@$source_sha:

\`\`\`sh
git fetch origin && git switch $sync_branch && git pull --ff-only
git fetch https://github.com/$source_repo.git $source_branch
git merge --no-ff -S --signoff FETCH_HEAD   # resolve, then: git commit -S --signoff
git push origin $sync_branch
\`\`\`
EOF
	)
	if [[ -n $conflict_issue ]]; then
		gh issue edit "$conflict_issue" --repo "$repo" --body "$body" >/dev/null
	else
		gh issue create --repo "$repo" --title "Sync conflict: $source_repo $source_branch into $target" \
			--label "$conflict_label" --body "$body" >/dev/null
	fi
	log "conflict: $conflict into $sync_branch"
	exit 0
fi

# 4. The pull request, with the release notes per line.
notes=$(mktemp)
{
	echo "Brings $source_repo \`$source_branch\` @ [\`$short\`](https://github.com/$source_repo/commit/$source_sha) into \`$target\`."
	echo
	echo "Opened by the fork sync (\`.github/workflows/fork-sync.yml\`). It merges itself (merge commit) once the required checks pass; merge it by hand at any time."
	echo
	echo "## Release notes"
	echo
	# Every changelog.d fragment the source adds, one line per bullet, under its section.
	gh api "repos/$repo/compare/$target_sha...$source_spec" --paginate \
		--jq '.files[]? | select(.status == "added" and (.filename | startswith("changelog.d/"))) | .filename' |
		sort -u | while read -r fragment; do
		gh api -H "Accept: application/vnd.github.raw" "repos/$source_repo/contents/$fragment?ref=$source_sha"
		echo
	done | awk '
		/^### / { section = substr($0, 5); next }
		/^- / {
			line = $0; sub(/[[:space:]]*<!--.*-->[[:space:]]*$/, "", line)
			lines[section] = lines[section] line "\n"; if (!(section in seen)) { seen[section] = 1; order[++n] = section }
		}
		END {
			if (n == 0) { print "No changelog.d release note in these commits." }
			for (i = 1; i <= n; i++) { printf "### %s\n\n%s\n", order[i], lines[order[i]] }
		}'
	echo
	echo "## Commits"
	echo
	# shellcheck disable=SC2016 # Markdown backticks, not expansions
	gh api "repos/$repo/compare/$target_sha...$source_spec" --paginate \
		--jq '.commits[] | select((.parents | length) == 1) | "- \(.commit.message | split("\n")[0]) ([`\(.sha[0:9])`](\(.html_url)))"'
} >"$notes"

ensure_label sync 0e8a16 "A fork sync pull request"
ensure_label "sync:$name" 0e8a16 "The $name fork sync"
title="sync($name): $source_repo $source_branch $short into $target"
if [[ -n $open_pr ]]; then
	gh pr edit "$open_pr" --repo "$repo" --title "$title" --body-file "$notes" >/dev/null
	pr=$open_pr
else
	pr=$(gh pr create --repo "$repo" --head "$sync_branch" --base "$target" --title "$title" --body-file "$notes" \
		--label sync --label "sync:$name" | sed -n 's|.*/pull/\([0-9]*\)$|\1|p')
fi
rm -f "$notes"
if [[ -n $conflict_issue ]]; then
	gh issue close "$conflict_issue" --repo "$repo" --comment "Resolved: #$pr" >/dev/null
fi
# Auto-merge only onto a branch that requires checks: on one that requires none, `gh pr merge --auto` merges at once.
required=$(gh api "repos/$repo/rules/branches/$target" --jq '[.[] | select(.type == "required_status_checks")] | length' 2>/dev/null || echo 0)
if [[ $required -gt 0 ]]; then
	gh pr merge "$pr" --repo "$repo" --auto --merge >/dev/null 2>&1 ||
		log "auto-merge not enabled on #$pr (the repository does not allow it, or it is already set)"
else
	log "$target requires no checks: auto-merge not set on #$pr; merge it by hand"
fi
log "sync $name: #$pr ($source_repo $source_branch $short into $target)"
