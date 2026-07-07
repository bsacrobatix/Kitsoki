# Bugfix Oracle Capsules

Bugfix oracle capsules are reusable historical bug cases for the dev-story
onboarding and repo-bakeoff path. Each capsule is one manifest bug under
`tools/bugfix-bakeoff/external/projects/<project>/manifest.yaml` plus its hidden
oracle test. The harness keeps the test separate from the fix:

1. GREEN: the target checkout and normal test/dependency setup are usable.
2. RED: at `baseline_sha`, overlay only the hidden oracle and require failure.
3. GREEN: from that same baseline, overlay only the real maintainer fix source
   from `fix_sha` and require the oracle to pass.
4. GREEN: score a candidate worktree with the same hidden oracle.

The promoted corpus currently has ten capsules:

- `query-string`: `qs1`, `qs2`, `qs3` from public GitHub issues/PRs.
- `gears-rust`: `bug1`, `bug4`, `bug5`, `bug9` from the private Rust reference
  repo. These can run from a standalone checkout or from a cyber-repo meta
  checkout with the submodule initialized.
- `kitsoki`: `bug9`, `bug12`, `bug14` from Kitsoki dogfood bugs.

List the catalog:

```sh
make oracle-capsules
python3 tools/bugfix-bakeoff/external/bench.py oracle-capsules
```

Verify a public project:

```sh
python3 tools/bugfix-bakeoff/external/bench.py verify --project query-string
```

Verify gears-rust as a standalone checkout:

```sh
GEARS_RUST_REPO=~/code/gears-rust make gears-bakeoff
```

Verify gears-rust through cyber-repo meta checkout:

```sh
python3 tools/bugfix-bakeoff/external/bench.py verify \
  --project gears-rust \
  --bug bug1,bug4,bug5,bug9 \
  --repo-dir ~/code/cyber-repo
```

In the meta-repo form, the manifest resolves
`~/code/cyber-repo/src/cyberfabric/gears-rust`. If that submodule is not
initialized, or if the checkout does not have the benchmark baseline/fix commits
reachable, preflight fails before any model spend. A standalone gears-rust
checkout with the benchmark history works through the same manifest.

The onboarding story’s `cyber-repo` starter pack includes `repo-bakeoff`, so a
newly onboarded project sees this oracle-capsule workflow alongside bugfix, PR
refinement, setup, and git operations.
