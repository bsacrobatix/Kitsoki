# LinkedIn current results reader

Run `inspect_current` to read the already-open paired LinkedIn Jobs results tab. It makes
exactly two bridge calls: a bare `snapshot`, then one `extract`. It never
navigates, selects filters, opens jobs, fills fields, presses keys, or paginates.

The captured result is structured: current Jobs URL, visible Remote indicator,
visible cards (`title`, `company`, `location`, `posted`), and the two-step audit.
Non-Jobs pages and pages without Remote are failures.

Run the no-LLM gate:

```sh
go run ./cmd/kitsoki validate stories/linkedin-job-search/app.yaml
go run ./cmd/kitsoki test flows stories/linkedin-job-search/app.yaml --v
```

`current_remote_jobs_success.yaml` verifies two structured cards and the exact
`snapshot`, `extract` audit. `current_tab_not_ready.yaml` verifies a clear
failure for a non-Jobs or missing-Remote page. These fakes do not launch Chrome,
use an LLM, or contact LinkedIn.

For a policy-approved one-shot live launch from a managed workspace, inject the
source checkout's machine-local config and explicitly scope the agent to the
workspace root. The copied local config cannot supply this policy: its relative
allowed roots would be resolved relative to the clone instead of the source
checkout.

```sh
project_root="$(node -p \"require('./capsule-manifest.json').source.repo\")"
go run ./cmd/kitsoki agent launch --exec \
  --config "$project_root/.kitsoki.local.yaml" \
  --working-dir "$PWD" \
  --app stories/linkedin-job-search/app.yaml \
  --agent linkedin_results_reader \
  --task 'Read only the current paired Jobs results: snapshot once, extract once, then return the structured result.'
```

This resolves the project policy at its declared root without hardcoding a
workspace path. The pairing-code environment variable must be present in the
launching process; do not put it in the story or command history.
