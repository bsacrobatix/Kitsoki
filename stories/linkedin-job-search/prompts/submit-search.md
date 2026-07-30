Perform the one confirmed LinkedIn Jobs search described below.

Keywords: {{ args.keywords }}
Location: {{ args.location|default:"(any location)" }}
Confirmation: {{ args.confirmation }}

The confirmation must be true. Use only `sassfully_browser.linkedin_story` and
its documented `action` arguments (`navigate`, `snapshot`, `fill`, `press`):
navigate to LinkedIn Jobs, snapshot, fill the supplied keywords and location,
then press the Jobs search submission control exactly once. The confirmation is
the visible Chrome modal supplied by the bridge, not a tool call. Snapshot the
results page and submit only the final results URL and the exact parameters
used. Do not open any job, inspect result cards, apply, message, save, follow,
paginate, export, scrape, or make any other LinkedIn change.
