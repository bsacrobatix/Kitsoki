# LinkedIn paired-tab search

Provide `prepare keywords=…` and the Story dispatches one paired-tab agent
session. Its deterministic first action is the generated US LinkedIn Jobs URL:
`/jobs/search-results/?keywords=<urlencoded>&geoId=103644278`. It snapshots the
Jobs page, semantically selects Remote when needed, waits with another snapshot,
then stores visible job-result cards, the final Jobs URL, and the action audit.
It must not use the LinkedIn People route or add USA/remote to the keyword query.

The fast no-LLM fake flow is:

```sh
go run ./cmd/kitsoki validate stories/linkedin-job-search/app.yaml
go run ./cmd/kitsoki test flows stories/linkedin-job-search/app.yaml --v
```

`flows/paired_tab_visible_output.yaml` fakes the task result and pins the exact
Jobs URL plus `navigate → snapshot → click Remote → snapshot → extract` audit.
The result schema rejects a LinkedIn People URL. It does not launch Chrome, use
an LLM, or contact LinkedIn.

For replay-backed integration, provide Sassfully's self-contained replay-fixture
bundle (rrweb chunks, reconstructed controls/timing, and expected MCP request /
response transcript). The feedback manifest alone is not sufficient; the bundle
must be consumed by the Sassfully bridge harness so no live profile or LinkedIn
network is used.
