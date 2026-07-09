# Arena Treatment Catalog

Arena comparisons vary tasks, candidates, and treatments. A treatment is the
action surface under test: raw agent prompting, direct CodeAct, Studio MCP
orchestration, or a workflow that delegates one step to CodeAct.

The reusable library lives in `tools/arena/arena/treatments/`:

| Module | Responsibility |
|---|---|
| `base.py` | Driver result, injected runner services, and public catalog entry contracts. |
| `capabilities.py` | CodeAct capability presets, canonical hashes, and launch-plan assertions. |
| `registry.py` | Stable treatment IDs, aliases, validation, and catalog metadata. |
| `raw_codex.py` | Raw one-shot prompt treatment. |
| `codex_codeact.py` | Direct `kitsoki-codeact-driver` treatment. |
| `kitsoki_mcp.py` | Studio MCP and Studio MCP plus CodeAct treatments. |

## Current Treatments

| Treatment ID | Alias | Action surface | Required fields | Notes |
|---|---|---|---|---|
| `raw-codex` | `single-briefed`, `single-naive` | `raw-agent` | none | One raw prompt through the configured backend. |
| `codex-codeact` | none | `kitsoki-codeact-mcp` | `variant.agent` | Launches `kitsoki-codeact-driver` with Codex shell/apps disabled and only `mcp__kitsoki-codeact__codeact_eval` exposed. |
| `kitsoki-mcp` | `kitsoki` | `kitsoki-studio-mcp` | none | `kitsoki-mcp-driver` orchestrates the normal Studio MCP workflow. |
| `kitsoki-mcp-codeact` | none | `kitsoki-studio-mcp+codeact` | none | Studio MCP drives the workflow, while the story implementation step runs through `host.agent.codeact`. |

Direct CodeAct treatments should use a named capability preset. The default
`repo_patch` preset grants repository file read/write plus read-only git probes;
the runner records the canonical JSON hash in the cell metrics so reports can
prove which surface was used.

## Adding a Treatment

1. Add a focused driver module under `tools/arena/arena/treatments/`.
2. Register the stable ID, aliases, and public fields in `registry.py`.
3. Keep runner-specific operations behind `DriverServices`; treatment modules
   should not import `tools/arena/lib/paired_task_runner.py`.
4. Document the treatment in this catalog and in the package README.
5. Add a no-LLM test that imports `arena.treatments` directly and validates the
   registry, validation rules, and any capability/permission assertions.
