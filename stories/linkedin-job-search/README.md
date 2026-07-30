# Attended LinkedIn job search

This v0 story deterministically constructs
`https://www.linkedin.com/jobs/search-results/?keywords=<urlencoded>&geoId=103644278`
for a base US search, then waits for `confirm_search`. The only browser effects
are one Chrome-confirmed navigation, one Chrome-confirmed constrained
`select_remote` action, and one safe snapshot that verifies the Remote filter.
Completion requires bridge evidence that `remote_selected` is true and that the
only action sequence was `navigate`, `select_remote`, `snapshot`.
It does not fill fields, press controls, open job details, apply, message, save,
follow, paginate, or bulk-scrape.

## Sassfully bridge binding

The story starts the local bridge with this stdio MCP configuration:

```yaml
agents.linkedin_searcher.mcp.servers.sassfully_browser:
  command: node
  args:
    - /Users/brad/code/studio-sassfully/.capsules/workspaces/extension-local-test-20260730/packages/feedback-extension/story-bridge/stdio-server.mjs
    - --pairing-code
    - ${SASSFULLY_LINKEDIN_PAIRING_CODE}
```

The bridge exposes one tool under the `sassfully_browser` MCP server namespace:
`linkedin_story`. This v0 agent may use only `navigate`, `select_remote`, and
`snapshot`. The bridge must accept a same-origin LinkedIn
`/jobs/search-results/` page after the approved navigation (including
LinkedIn's harmless query rewrites such as `currentJobId`), require a visible
Chrome confirmation for each state-changing action, confine `select_remote` to
the Remote filter/option, and return remote-selection evidence for the snapshot.
The story agent receives no native filesystem, shell, web, or editor tools.

Set `SASSFULLY_LINKEDIN_PAIRING_CODE` in the environment that launches Kitsoki.
The `${...}` token is intentionally passed as MCP configuration interpolation;
the pairing code itself is never committed, rendered into the story, or put in
world state.

## Deterministic bridge-contract lane

The bridge does not currently advertise an atomic `search_us_remote` action, so
the Story intentionally orchestrates the three supported actions. Do not add
that action to the Story allowlist or prompt until the bridge advertises it in
its MCP tool schema and supplies an atomic contract test. If it is added later,
replace the three-action instruction with that single declared action and keep
the same no-application/no-message/no-scrape boundary.

`flows/confirmed_search_captures_url.yaml` is the bridge-success fake: it
returns the contract payload the task must produce after the three bridge calls.
It proves, without an LLM or LinkedIn, that the generated base URL is preserved,
the Story accepts only `remote_selected: true`, and the exact three-action trace
is required. `flows/bridge_failure_stops_without_snapshot.yaml` is the
readiness/failure fake: the Remote action is unavailable, the Story reaches
`failed`, and no success URL can be recorded.

Run the fast lane from the Kitsoki checkout:

```sh
go run ./cmd/kitsoki validate stories/linkedin-job-search/app.yaml
go run ./cmd/kitsoki test flows stories/linkedin-job-search/app.yaml --v
```

The Sassfully bridge remains the authority for a real MCP fake/test server,
because it owns Chrome confirmation and the visible Remote control. Its contract
suite should drive one paired-tab fixture through `navigate` → `select_remote`
→ `snapshot`, assert `remote_selected: true`, and record the three-action trace;
it should also cover a query-rewritten search URL and an unavailable Remote
control. That suite must never call LinkedIn or an LLM.

## Live preflight (doctor expectation)

Before one attended run, treat these as the Story's operator-facing doctor
checks:

1. The local bridge process starts with a process-local pairing code; do not
   store the code in the Story, trace, or shell history.
2. The connected Chrome tab is an authenticated LinkedIn page and can receive
   the visible Allow confirmations.
3. MCP discovery returns exactly `linkedin_story` with only `navigate`,
   `select_remote`, and `snapshot`; `search_us_remote` is not assumed.
4. The starting or post-navigation tab is same-origin LinkedIn
   `/jobs/search-results/`. LinkedIn query rewrites are acceptable; a different
   route is not.
5. The visible Remote filter is available. If it is not, stop after that single
   failed selection: do not snapshot, retry, open a result, or use another UI
   control.

Validate the bridge tool listing in the client that runs Kitsoki before a live
session. Automated tests use the supplied fakes and never contact LinkedIn.
