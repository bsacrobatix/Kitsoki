# LinkedIn paired-tab search

Provide `prepare keywords=…` and the Story dispatches one paired-tab agent
session. The agent discovers `linkedin_story`, uses its generic paired-tab
actions as needed, and stores the final visible extraction, current URL, and
action outcome audit.

The fast no-LLM fake flow is:

```sh
go run ./cmd/kitsoki validate stories/linkedin-job-search/app.yaml
go run ./cmd/kitsoki test flows stories/linkedin-job-search/app.yaml --v
```

`flows/paired_tab_visible_output.yaml` fakes the task result and proves a single
agent dispatch records visible output. It does not launch Chrome, use an LLM, or
contact LinkedIn.

For replay-backed integration, provide Sassfully's self-contained replay-fixture
bundle (rrweb chunks, reconstructed controls/timing, and expected MCP request /
response transcript). The feedback manifest alone is not sufficient; the bundle
must be consumed by the Sassfully bridge harness so no live profile or LinkedIn
network is used.
