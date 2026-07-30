# Attended LinkedIn job search

After the user supplies keywords and confirms, this Story makes exactly one
Sassfully bridge call: `linkedin_story.search_jobs`. The bridge owns the bounded
navigation, Remote selection, and results-readiness wait behind one visible
Chrome Allow confirmation. The Story records only the ready results URL and
whether Remote was already selected; it never extracts job rows or opens job
details.

The atomic bridge request is:

```json
{"action":"search_jobs","keywords":"<user keywords>","geoId":"103644278","remote":true}
```

The bridge must return:

```json
{"finalUrl":"https://www.linkedin.com/jobs/search-results/...","remoteSelected":true,"alreadySelected":false}
```

`remoteSelected: true` is required before the Story can complete. A false value,
missing ready URL, or bridge error goes to the visible failed state with no
automatic retry. The agent cannot fill fields, press controls, open a job,
apply, message, save, follow, paginate, export, scrape, or enumerate results.

## Sassfully bridge binding

The Story starts the local bridge with the checked-in stdio MCP configuration in
`app.yaml`. Set `SASSFULLY_LINKEDIN_PAIRING_CODE` only in the process that
launches Kitsoki. It is an environment interpolation token, never story state,
source, or a trace payload.

The only allowlisted bridge tool is `linkedin_story`; its only Story action is
`search_jobs`. Do not reintroduce client-side `navigate`, `select_remote`, or
`snapshot` choreography. Any new atomic contract must be added deliberately to
the schema and deterministic test lane first.

## Deterministic lane and doctor expectation

Run the no-LLM lane:

```sh
go run ./cmd/kitsoki validate stories/linkedin-job-search/app.yaml
go run ./cmd/kitsoki test flows stories/linkedin-job-search/app.yaml --v
```

The success fixture proves that a valid atomic result reaches `complete`.
`bridge_failure_stops_without_snapshot.yaml` proves bridge errors stop in the
failed state, while `remote_false_never_completes.yaml` proves a result with
`remoteSelected: false` cannot complete. These fakes never launch Chrome,
contact LinkedIn, or use an LLM.

Before an attended run, the operator-facing doctor expectation is: the paired
receiver is available, the bridge advertises `search_jobs` with the atomic
arguments above, Chrome can present its one confirmation, and the returned
`finalUrl` is a LinkedIn `/jobs/search-results/` route with
`remoteSelected: true`. If any check fails, stop—do not fall back to individual
browser actions.
