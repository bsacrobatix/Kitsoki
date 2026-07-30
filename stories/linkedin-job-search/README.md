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
