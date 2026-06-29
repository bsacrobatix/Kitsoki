Group these commits into pull-request buckets by concern.

Source branch: `{{ world.source_branch }}` → integration: `{{ world.integration_branch }}`

Commits (oldest first), with their changed files and a deterministic concern
guess from file paths:

{{ world.commits.rendered }}

Rules:
- Every sha above must appear in exactly ONE bucket. Never split or drop a commit.
- Keep each commit's deterministic concern unless `ambiguous=true`, in which case
  place it in the single most appropriate bucket so each PR stays coherent.
- Within a bucket, keep commits in their original (oldest-first) order so a
  cherry-pick applies cleanly.
- One bucket per concern. Prefer fewer, coherent PRs over many tiny ones, but do
  not mix a core code change with unrelated tooling or docs.
- Write a clear PR `title` and a `body` (markdown) for each bucket summarizing
  what it changes and why it is independently reviewable.

Return the buckets via the submit tool against the pr_buckets schema.
