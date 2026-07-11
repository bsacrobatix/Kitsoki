# Story-native CI

Capsule CI treats a Kitsoki story as the pipeline. `.kitsoki/ci.yaml` selects
the story, environment, executor, trigger policy, agent budget, cleanup policy,
and typed result contract; the story owns the actual phases and gates.

The important boundary is that `.kitsoki/ci.yaml` is not a shell-step DAG. A
project can compose deterministic checks, schema-bounded review, bounded writer
loops, human checkpoints, and adjudication as normal rooms. The runner only
accepts a `capsule-ci-verdict/v1` that matches the sealed execution envelope.

## Contract

A CI story receives normalized world values from the runner:

- `ci_job_id`
- `ci_pipeline`
- `ci_trigger`
- `ci_source`
- `ci_workspace`
- `ci_environment`
- `ci_policy`

It must emit `world.ci_verdict` with:

```json
{
  "schema": "capsule-ci-verdict/v1",
  "pipeline": "change",
  "outcome": "passed",
  "checks": [
    {
      "id": "deterministic",
      "kind": "deterministic",
      "outcome": "passed",
      "evidence": ["artifact:test-log"]
    }
  ],
  "promotion_eligible": true,
  "source_digest": "sha256:...",
  "story_digest": "sha256:...",
  "environment_digest": "sha256:...",
  "envelope_digest": "sha256:..."
}
```

Promotion eligibility is derived by the runner. A story cannot make a failed,
missing-evidence, digest-mismatched, or parked run promotion eligible by setting
the boolean manually.

## Reference story

`stories/capsule-ci` is the reference composition. It demonstrates:

- `prepare`
- `deterministic_checks`
- `llm_review`
- `refine_change`
- `adjudicate`

The default `run` path parks with `needs_input` until a project wrapper composes
real checks. This is intentional: the reference story must never fabricate a
green CI verdict for an unknown project.

No-LLM flow fixtures cover pass, fail, park, review pass, and budget exhaustion.
Runtime tests run the same story contract through host, fake remote, and fake
container placements.

## Agent authority

Reviewer and writer rooms should receive explicit toolboxes. A writer that
modifies code should receive only the Capsule MCP workspace handle for the
leased workspace unless the story deliberately grants more authority. Remote
publish, PR creation, and protected-branch promotion are separate grants and
are not implied by a green CI verdict.

## GitHub ingress

GitHub is an adapter, not the CI engine. Pull-request webhook data normalizes
to the standard `ci.Trigger` shape, and the check-run publisher projects the
typed Capsule verdict to a GitHub check conclusion. Automated tests use the pure
adapter contract and do not contact GitHub.
