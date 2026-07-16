#!/usr/bin/env bash
#
# open-pr.sh — gate PR creation on a green test run, then open the PR.
#
# Why: you can push half-finished / non-building branches freely, but there is
# no point opening a PR that CI will immediately fail. This script is the
# checkpoint that runs ONLY when you choose to open a PR.
#
# Two modes:
#   (default)  LOCAL gate — run low-contention `make test` here, then
#              `gh pr create`. It is fast/offline and forbids browser launches.
#   --ci       PUSH gate  — run `make push-gate` here, push the branch, trigger
#              the CI workflow, wait for it to finish, and open the PR only if CI
#              is green. This is intentionally heavier and includes browser
#              validation before spending remote capacity.
#
# Any args after the optional mode flag pass through to `gh pr create`
# (e.g. --fill, --draft, --base main, --title "...", --body "...").
#
# Invoked via `make pr` (local) and `make pr-ci` (CI).
# See docs/guide/development/developer-guide.md (§3.3).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WORKFLOW="ci.yml"

mode="local"
if [ "${1:-}" = "--ci" ]; then
	mode="ci"
	shift
elif [ "${1:-}" = "--local" ]; then
	shift
fi

# gh is required for every path (we always end in `gh pr create`).
if ! command -v gh >/dev/null 2>&1; then
	echo "error: gh CLI not found — install it (https://cli.github.com) and run 'gh auth login'." >&2
	exit 1
fi
if ! gh auth status >/dev/null 2>&1; then
	echo "error: gh is not authenticated — run 'gh auth login' first." >&2
	exit 1
fi

branch="$(git rev-parse --abbrev-ref HEAD)"
if [ "$branch" = "main" ]; then
	echo "error: you are on 'main' — switch to a feature branch before opening a PR." >&2
	exit 1
fi

if [ "$mode" = "local" ]; then
	upstream="$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null || true)"
	if [ -z "$upstream" ]; then
		echo "error: local PR mode would need to publish '$branch' first." >&2
		echo "       use 'make pr-ci' so the push gate, including browser validation, runs before pushing." >&2
		exit 1
	fi
	if [ "$(git rev-list --count "$upstream"..HEAD)" != "0" ]; then
		echo "error: '$branch' has commits not pushed to $upstream." >&2
		echo "       use 'make pr-ci' so the push gate, including browser validation, runs before pushing." >&2
		exit 1
	fi
	echo "open-pr: running low-contention 'make test' as a local pre-PR gate…"
	if ! make test; then
		echo "" >&2
		echo "open-pr: tests FAILED — not opening a PR. Fix the failures (or use 'make pr-ci'" >&2
		echo "         to gate on the real CI run instead) and try again." >&2
		exit 1
	fi
	echo "open-pr: tests passed ✓ — opening PR…"
	exec gh pr create "$@"
fi

# ── CI gate ──────────────────────────────────────────────────────────────────
echo "open-pr: running 'make push-gate' before pushing '$branch'…"
if ! make push-gate; then
	echo "" >&2
	echo "open-pr: push gate FAILED — not pushing or opening a PR." >&2
	exit 1
fi

echo "open-pr: pushing '$branch' to origin…"
git push -u origin HEAD

echo "open-pr: triggering the CI workflow ($WORKFLOW) on '$branch'…"
gh workflow run "$WORKFLOW" --ref "$branch"

# Find the run we just dispatched. `gh workflow run` is async and returns no id,
# so poll the run list for a freshly-created run on this branch+workflow.
echo "open-pr: waiting for the run to appear…"
run_id=""
for _ in $(seq 1 30); do
	run_id="$(gh run list --workflow "$WORKFLOW" --branch "$branch" \
		--event workflow_dispatch --limit 1 --json databaseId \
		--jq '.[0].databaseId' 2>/dev/null || true)"
	[ -n "$run_id" ] && break
	sleep 2
done
if [ -z "$run_id" ]; then
	echo "error: could not find the dispatched CI run for '$branch'." >&2
	echo "       check 'gh run list --workflow $WORKFLOW' and retry." >&2
	exit 1
fi

echo "open-pr: watching CI run $run_id (Ctrl-C to stop watching; the run keeps going)…"
if ! gh run watch "$run_id" --exit-status; then
	echo "" >&2
	echo "open-pr: CI FAILED — not opening a PR. Inspect it with:" >&2
	echo "         gh run view $run_id --log-failed" >&2
	exit 1
fi

echo "open-pr: CI passed ✓ — opening PR…"
exec gh pr create "$@"
