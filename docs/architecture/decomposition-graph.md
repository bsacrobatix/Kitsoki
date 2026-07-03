# Decomposition Graph — The Two-Graph Model

This document describes the shape and semantics of kitsoki's work decomposition, which powers goal-tracking, proposal planning, and iterative delivery. It establishes the **unified change-node contract** (`schemas/change-node.schema.json`) as the single source of truth for decomposition structure across multiple consumers and contexts.

---

## Overview

Decomposition in kitsoki is dual-graph: an **acyclic depends_on DAG** (dependency order) and a **cyclic process graph** (workflow state machine). A third dimension—**goal_ref traceability**—links each change node to declared goals (GOAL.md criteria). This architecture enables:

- **Deterministic sequencing** — the depends_on DAG orders concurrent work
- **Deterministic validation** — structural gate (goal.py lint) enforces invariants independent of LLM
- **Traceability** — goal_ref ensures every change advances a declared criterion
- **Reusability** — the same node shape serves goal.py, work-decomposition, and stories/deliver

---

## The Unified Change-Node Shape

A **change-node** is a single unit of work in a decomposition (variously called "change," "brief," or "task" across different tools). The shape is defined in `schemas/change-node.schema.json` (versioned v1.0.0) and is validated by:

- **goal.py** — the deterministic backbone of the goal-seeker (docs/goals/*/decomposition.yaml)
- **work-decomposition validator** — validates agent brief manifests (.agents/skills/work-decomposition/scripts/validate_decomposition.py)
- **deliver story schema** — lightweight decompositions for incremental delivery (stories/deliver/schemas/decomposition.json)

### Required Fields (Shared Contract)

```json
{
  "id": "string",                    // unique lowercase hyphenated identifier
  "title": "string",                 // human-readable label (≥4 chars)
  "goal": "string",                  // what 'done' looks like, reviewer's terms (≥20 chars)
  "scope": ["string", ...],          // file/dir globs — the write boundary (non-empty)
  "acceptance": ["string", ...],     // verifiable acceptance criteria (non-empty)
  "depends_on": ["string", ...]      // ids of nodes that must land first (may be empty)
}
```

### Tool-Specific Extensions

**goal.py** (docs/goals/*/decomposition.yaml):
- `wall` (required) — logical merge wall group for batching
- `goal_ref` (required) — array of goal criterion ids (G1, G2, ...) from GOAL.md
- `gate` (required) — object with `class` (G-FLOW, G-GOTEST, G-SMOKE, G-LINT, G-LIVE+REPLAY) and `cmd`/`replay_cmd` (deterministic check)
- `pipeline` (optional) — pipeline stage identifier

**work-decomposition**:
- `kind` (required) — enum: story, runtime, tui, tracing, test, docs
- `test_plan` (required) — how this change is verified (≥20 chars)
- `agent_brief` (required) — self-contained implementer prompt (≥80 chars)
- `risk` (optional) — enum: low, medium, high

**deliver story**:
- `brief` (required) — scoped task description for maker agent (replaces agent_brief)
- `gate_command` (required) — deterministic shell exit-0 check (replaces gate object)
- `deps` or `depends_on` (optional) — either form accepted

---

## The Two Graphs

### Graph 1: Acyclic Depends-On DAG

The **dependency DAG** defines **hard sequencing**: if change B depends_on change A, then A must land (complete and merge) before B can land. The DAG is:

- **Acyclic** — validated by goal.py lint and work-decomposition validator (Kahn's algorithm)
- **Scope-disjoint** — concurrent changes (no depends_on chain between them) must not overlap in scope
- **Transitive** — all reachability is precomputed; the ready set excludes changes with unsatisfied transitive deps

**Used by:**
- goal.py — selects the ready worker set (ready = no unsatisfied deps, scope-disjoint from peers)
- work-decomposition validator — enforces acyclic structure, detects dangling deps
- deliver story — ensures order (ordered array where deps reference only earlier ids)

### Graph 2: Cyclic Process Graph (State Machine)

The **process graph** is the cyclic workflow of a decomposition (or one change node) through states:

```
blocked ←→ ready ←→ assigned ←→ in_flight ←→ reviewing ←→ verified ←→ integrated
                                                                         ↓
                                                                      parked
```

State transitions are driven by goal.py's log (append-only JSONL). A state transition is:
- **Deterministic** — only states in `STATES` are legal; later states never regress silently
- **Logged** — each transition is a structured entry in `.artifacts/goal/<slug>/log.jsonl`
- **Bounded** — reachable from exactly the DAG-ready set + current state

**Used by:**
- goal.py — folds the log into a ledger (state per change), prints the state machine
- dogfood runner — reads ledger to decide which changes to invoke next, appends results to log

### Linking the Graphs

- **DAG ⟹ ready set** — changes in the ready set are those with all deps integrated and scope-disjoint from peers
- **Ready set ⟹ assignments** — the goal-seeker evaluator assigns ready changes to workers (e.g., maker agents)
- **Assignments ⟹ state transitions** — each assignment moves a change to `assigned`, then `in_flight`; results move it to `reviewing` or `parked`
- **Reviews ⟹ integration** — a verified change transitions to `integrated` (hard truth of state.json in ledger); now its dependents become ready

### Graph 3: Goal-Ref Traceability

**goal_ref** (goal.py only) is a third dimension: each change node carries an array of criterion ids (G1, G2, ...) from GOAL.md. This creates a **traceability matrix**:

```
GOAL.md criterion G1 ←→ [changes with goal_ref: [G1, ...]]
GOAL.md criterion G2 ←→ [changes with goal_ref: [G2, ...]]
...
```

**Used by:**
- goal.py — verifies goal_refs resolve to declared criteria
- ledger — aggregates which criteria are satisfied (all changes → integrated)
- preamble — projects a bounded summary of progress toward each goal

---

## Validation and Entry Points

### Validation Layers

1. **Schema validation** (JSON-Schema, v7)
   - File: `schemas/change-node.schema.json`
   - Validates shape (required fields, types, patterns, min/max lengths)
   - Optional: run only if jsonschema library is available
   - **Consumers:** goal.py, work-decomposition validator

2. **Cross-node validation** (deterministic logic)
   - **Acyclic DAG:** Kahn's algorithm detects cycles
   - **Unique ids:** no two changes share an id
   - **No dangling deps:** every depends_on id resolves within the decomposition
   - **Scope-disjoint concurrent:** changes with no depends_on chain must not overlap in write boundaries
   - **goal_ref resolve:** (goal.py only) every goal_ref id must be declared in GOAL.md
   - **Runnable gates:** (goal.py only) every gate has a non-trivial deterministic check

3. **Structural gates (entry points)**
   - **goal.py lint** — exit code gates deployment of a goal-driven decomposition; errors halt the goal-seeker
   - **work-decomposition validator** — exit code gates acceptance of an agent-generated brief manifest
   - **deliver story schema** — JSON-Schema gates decomposition emission (soft gate; flow may continue with a fallback)

---

## The Change-Node Contract

The file `schemas/change-node.schema.json` is the authoritative definition. It is:

- **Versioned** — currently v1.0.0 (semantic versioning); breaking changes bump major
- **Single-source-of-truth** — goal.py and work-decomposition both reference and validate against this one schema
- **Extensible** — tool-specific fields are permitted; only the required core fields and key constraints are shared
- **Documented inline** — field descriptions in the schema serve as the spec; no separate requirements

### Updating the Contract

When adding a new required field:
1. Increment the version in `$id` and `version` fields
2. Add the field to `properties` with description and constraints
3. Add it to `required` array
4. Update both consumers (goal.py and work-decomposition validator) to enforce the new constraint
5. Update docs (this file) to reflect the change

---

## Examples

### Example: Goal-Driven Decomposition (goal.py)

File: `docs/goals/example-goal/decomposition.yaml`

```yaml
changes:
  - id: schema-define
    title: Define the unified change-node schema
    scope: [schemas/change-node.schema.json]
    acceptance:
      - Schema file exists with v1.0.0 marker
      - Required fields documented
      - Tool-specific extensions permitted
    depends_on: []
    wall: schema
    goal_ref: [G1]
    gate:
      class: G-LINT
      cmd: python3 -m jsonschema.validator schemas/change-node.schema.json

  - id: impl-goal-py
    title: Update goal.py to validate against change-node schema
    scope: [.agents/skills/goal/goal.py]
    acceptance:
      - Schema loaded and cached during lint
      - Per-change validation reports errors
      - All existing tests pass
    depends_on: [schema-define]
    wall: impl
    goal_ref: [G1, G2]
    gate:
      class: G-GOTEST
      cmd: cd .worktrees/generalized-usage && python -m pytest .agents/skills/goal/goal_test.py -v
```

### Example: Agent-Generated Decomposition (work-decomposition)

File: `docs/proposals/example/decomposition.json` (generated by decompose agent)

```json
{
  "coverage_note": "Three independent briefs cover the proposal's scope: schema definition (1.5h), goal.py updates (3h), docs (2h). No integration risk between briefs.",
  "briefs": [
    {
      "id": "schema-define",
      "title": "Unified change-node schema",
      "kind": "docs",
      "goal": "A versioned JSON-Schema for change-nodes serves as the single source of truth for shape across goal.py and work-decomposition.",
      "scope": ["schemas/change-node.schema.json"],
      "acceptance": ["Schema exists", "Required fields documented", "v1.0.0 marker present"],
      "depends_on": [],
      "test_plan": "Validate schema syntax; run against example changes (goal-driven and work-decomposition styles)",
      "agent_brief": "Create schemas/change-node.schema.json with v1.0.0, documenting required fields (id, title, goal, scope, acceptance, depends_on) and tool-specific extensions.",
      "risk": "low"
    }
  ]
}
```

---

## Deferred Work (Follow-On Proposals)

The following are **explicitly out of scope** for this change-node contract and will be addressed in named follow-on proposals:

### 1. Change-Impact Analysis (CIA) & Dirty-Edge Semantics
**Proposal:** docs/proposals/cia.md (TBD)

Tracks which downstream changes are affected when a prior change regresses or requires rework. Current state: none. When a change enters `parked:conflict`, identifying which dependents must be re-evaluated is manual and expensive.

**Deferred because:** requires a new edge type (transitive impact) and runtime state (dirty edges) not in the current DAG. Belongs in a separate proposal with its own validation layer.

### 2. Artifact Versioning & Provenance
**Proposal:** docs/proposals/artifact-provenance.md (TBD)

Associates artifacts (code, tests, docs) with the change nodes that produced them. Current state: none. Enables rollback and blame tracking at the artifact level.

**Deferred because:** requires a new schema for artifact→change mappings and a provenance store. Orthogonal to the change-node shape contract.

### 3. Decompose/Story-Forge/Dynamic-Workflow Unification
**Proposal:** docs/proposals/workflow-unification.md (TBD)

Merges the three decomposition pipelines (decompose skill, story-forge, kitsoki's dynamic workflow) into one, with shared runtime and cross-stage state.

**Deferred because:** requires runtime changes, new host calls, and state schema beyond the change-node contract. Belongs in a separate architectural proposal.

---

## See Also

- `schemas/change-node.schema.json` — the versioned contract (authoritative)
- `.agents/skills/goal/goal.py` — goal.py lint and state machine (primary consumer)
- `.agents/skills/work-decomposition/scripts/validate_decomposition.py` — schema validator (secondary consumer)
- `internal/basestories/stories/deliver/schemas/decomposition.json` — deliver story schema
- `docs/goals/` — examples of goal-driven decompositions (YAML, goal.py validated)
- `docs/proposals/work-decomposition/` — work-decomposition proposal (foundational)
