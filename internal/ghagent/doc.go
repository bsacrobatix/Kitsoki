// Package ghagent implements the @kitsoki "mention -> dispatch -> run -> ack"
// loop: it scans the GitHub issue/PR inbox for @kitsoki mentions, claims each
// as an idempotent job, routes its label to a story, spawns that story no-LLM
// through the real flow engine, and posts a rolling-status ack comment.
//
// # Why
//
// The dogfood loop wants a single seam where an operator (or a teammate) can
// say "@kitsoki please fix this" on a GitHub issue and have kitsoki actually
// pick it up, run the mapped pipeline, and report back — all exercised by the
// real engine, not a bespoke mock. This package is that seam.
//
// # Boundaries / non-goals (round 1)
//
//   - Single-shot poll only (`kitsoki gh-agent poll`); the long-running serve
//     daemon/timer is deferred.
//   - No GitHub-App auth, webhooks, or HMAC; all gh/git I/O flows through the
//     host cliExec seam so tests replay from cassettes, offline and free.
//   - The PR path ships ONE real beat (pr_status read + status comment), not a
//     full pr-autopilot story.
//   - True in-place comment edits are approximated by carrying the comment id
//     forward in a fenced ```kitsoki block and re-posting; a `gh issue comment
//     --edit-last` host verb is deferred.
//
// # Pieces
//
//   - mention.go  — the @kitsoki mention filter over the ingress producer.
//   - router.go   — label -> story classification + the default route table.
//   - comment.go  — the rolling-status/ack comment substrate over host.gh.ticket.
//   - dispatch.go — the Dispatcher: claim a job + spawn the mapped story.
//
// # Concurrency note
//
// testrunner.RunFlows publishes KITSOKI_APP_DIR as a process global, so
// concurrent Dispatch of multiple mentions in one process can cross-contaminate.
// The round-1 single-shot poll dispatches mentions sequentially; per-job
// KITSOKI_APP_DIR isolation is a prerequisite before the serve daemon is built.
package ghagent
