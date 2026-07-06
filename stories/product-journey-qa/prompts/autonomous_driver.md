Drive the product-journey QA autonomous marathon bundle.

Inputs:

- run_dir: `{{ args.run_dir }}`
- project: `{{ args.project }}`
- persona: `{{ args.persona }}`
- scenarios: `{{ args.scenario_scope }}`
- driver mode: `{{ args.autonomous_driver_mode }}`
- live budget minutes: `{{ args.live_budget_minutes }}`
- ticket repo: `{{ args.ticket_repo }}`
- gh-agent public URL: `{{ args.gh_agent_public_base_url }}`
- control artifact: `{{ args.autonomous_control_path }}`
- report artifact: `{{ args.autonomous_marathon_report_path }}`

Use `.agents/agents/product-journey-qa-driver.md` as the operating contract.
Open or attach a product-journey story session for `stories/product-journey-qa/app.yaml`,
then submit `load run_dir={{ args.run_dir }}` before recording evidence.

Capture each scenario's proof evidence or record an honest blocker. Record every
attempt through `driver_event`, attach all evidence refs through `attach`, and
record concrete findings through `record` or `blocker`.

Before issue-to-fix spend, submit `autonomous_watchdog`. If credible issue
findings exist and both `ticket_repo` and `gh_agent_public_base_url` are
configured, submit `autonomous_fix ticket_repo={{ args.ticket_repo }}
gh_agent_public_base_url={{ args.gh_agent_public_base_url }}` so the native
product-journey gitops/gh-agent path files issues, drains fixes, captures
reviewable artifacts, independently verifies, and updates issue state. Do not
run `gh` or file/fix issues outside that native story path.

Finish by submitting `review`, `validate`, and `stats` when the run has enough
state for stats. Return a concise JSON object matching the schema with the
driver status, captured evidence count, issue count, summary, and any trace or
blocker references.
