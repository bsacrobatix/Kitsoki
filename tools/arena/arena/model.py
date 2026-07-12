"""Core data model for the arena comparison-job runner.

A *job* is a big comparison sweep (bug-fixing, onboarding, persona QA, …). It is
declared as a `JobSpec` and enumerates into `Cell`s — the unit of work. Each cell
runs in a container (placed on a Docker host) and produces a job-type-agnostic
`CellResult`. The `candidate` of bugfix-bakeoff and the `persona/scenario` of
product-journey both collapse here into a cell's **variant + axis coords**.

Pure stdlib + dataclasses; no LLM, no docker — those live behind interfaces in
executor.py so this layer stays deterministic and trivially testable.
"""

from __future__ import annotations

import itertools
import hashlib
import json
import subprocess
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Any

# tools/arena/arena/model.py -> parents[3] is the repo root. `targets_from` /
# `persona_axis_from` / `target_proof_from` paths in a spec are resolved
# relative to this so specs stay portable regardless of cwd.
REPO_ROOT = Path(__file__).resolve().parents[3]


def _resolve_repo_path(path: str | Path) -> Path:
    p = Path(path)
    return p if p.is_absolute() else REPO_ROOT / p


@dataclass(frozen=True)
class Target:
    """One thing being compared *across* — e.g. a repo. (github-targets shape.)"""

    id: str
    label: str = ""
    repo: str = ""
    stack: str = ""
    meta: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Variant:
    """One competitor in the comparison — a model/tool/treatment (candidate shape)."""

    id: str
    backend: str = ""          # claude | codex | synthetic | …
    model: str = ""
    effort: str = ""
    meta: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Cell:
    """The unit of work: target × variant × axis-coords, for one job_type."""

    id: str
    job_type: str
    target: Target
    variant: Variant
    axis: dict[str, str] = field(default_factory=dict)   # e.g. {"bug": "qs1"}
    status: str = "planned"
    options: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        out = {
            "id": self.id,
            "job_type": self.job_type,
            "target": asdict(self.target),
            "variant": asdict(self.variant),
            "axis": dict(self.axis),
            "status": self.status,
        }
        if self.options:
            out["options"] = dict(self.options)
        return out


# Job-type-agnostic verdicts. Plugins map their native grade into these so the
# rollup can aggregate across any job type. (bugfix oracle, persona review gate,
# onboarding assertions all land in this vocabulary.)
VERDICTS = ("solved", "partial", "failed", "armed", "blocked", "pending", "unsupported")

# A task-optimization cell moves through these durable scheduler states. They
# deliberately are not verdicts: a valid scored attempt can be failed, while a
# pending/unsupported candidate has not supplied a model result at all.
TASK_OPTIMIZATION_CELL_STATES = (
    "planned", "armed", "ready", "running", "scored", "blocked", "pending", "unsupported",
)
TASK_OPTIMIZATION_SCHEMA = "task-optimization/v1"
TASK_OPTIMIZATION_PREFLIGHT_SCHEMA = "task-optimization/preflight/v1"
TASK_OPTIMIZATION_ATTEMPT_SCHEMA = "task-optimization/attempt/v1"
TASK_OPTIMIZATION_CHAMPION_SCHEMA = "task-optimization/champion/v1"
TASK_OPTIMIZATION_TERMINAL_STATES = ("scored", "blocked", "unsupported")


def _sha256(value: bytes | str) -> str:
    if isinstance(value, str):
        value = value.encode("utf-8")
    return hashlib.sha256(value).hexdigest()


def canonical_json(value: Any) -> bytes:
    """Return the canonical bytes used by task-optimization receipts."""
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False) + "\n").encode("utf-8")


def receipt_sha256(value: Any) -> str:
    return _sha256(canonical_json(value))


BUGSWARM_CORPUS_LOCK_SCHEMA = "arena_bugswarm_corpus_lock/v1"


def bugswarm_lock_sha256(value: dict[str, Any]) -> str:
    """Return the locker's canonical digest, excluding its self-reference."""
    payload = dict(value)
    payload.pop("lock_sha256", None)
    return _sha256(json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("utf-8"))


@dataclass(frozen=True)
class TaskOptimizationStudy:
    """Immutable-input declaration for a task x model x treatment campaign.

    The study file is intentionally separate from the generic ``JobSpec``:
    it owns split discipline, pinned corpus/profile inputs and repeat policy,
    while Arena remains the scheduler and report surface.
    """

    study_id: str
    boundary: str
    corpus_lock: str
    splits: dict[str, str]
    candidates: list[dict[str, Any]]
    treatments: list[str]
    repeats: dict[str, int]
    stop: dict[str, Any]
    live_gate_env: str
    blocked_by: list[str] = field(default_factory=list)
    path: str = ""

    @staticmethod
    def load(path: str | Path) -> "TaskOptimizationStudy":
        p = Path(path).resolve()
        text = p.read_text(encoding="utf-8")
        try:
            import yaml  # type: ignore
            data = yaml.safe_load(text)
        except ModuleNotFoundError:
            data = json.loads(text)
        if not isinstance(data, dict):
            raise ValueError("study manifest must be a mapping")
        if data.get("schema") != TASK_OPTIMIZATION_SCHEMA:
            raise ValueError(f"unsupported study schema {data.get('schema')!r}")
        blocked_by = [str(reason) for reason in data.get("blocked_by", []) or []]
        is_blocked = data.get("live_status") == "blocked"
        required = ("study_id", "boundary", "corpus_lock", "splits", "candidates", "treatments", "repeats", "stop")
        missing = [key for key in required if not data.get(key)]
        if missing and not is_blocked:
            raise ValueError(f"study manifest missing required fields: {', '.join(missing)}")
        if missing:
            if not blocked_by:
                raise ValueError("blocked study manifests require non-empty blocked_by")
            return TaskOptimizationStudy(
                study_id=str(data["study_id"]), boundary=str(data["boundary"]), corpus_lock=str(data.get("corpus_lock") or ""),
                splits={}, candidates=[], treatments=[], repeats={}, stop={},
                live_gate_env=str(data.get("live_gate_env") or "KITSOKI_TASK_OPT_LIVE"), blocked_by=blocked_by, path=str(p),
            )
        candidates = data["candidates"]
        if not isinstance(candidates, list) or not all(isinstance(c, dict) for c in candidates):
            raise ValueError("candidates must be a non-empty list of mappings")
        # The checked-in Stage-0 declaration uses the more explicit
        # key/requested_* spelling.  Normalize both it and the compact test
        # spelling here, so a corpus unlock cannot silently change candidate
        # identity or discard the requested model/effort contract.
        normalized_candidates: list[dict[str, Any]] = []
        for raw in candidates:
            candidate = dict(raw)
            candidate["id"] = str(candidate.get("id") or candidate.get("key") or "")
            candidate["model"] = str(candidate.get("model") or candidate.get("requested_model") or "")
            candidate["effort"] = str(candidate.get("effort") or candidate.get("requested_effort") or "")
            candidate["requested_model"] = str(candidate.get("requested_model") or candidate["model"])
            candidate["requested_effort"] = str(candidate.get("requested_effort") or candidate["effort"])
            normalized_candidates.append(candidate)
        ids = [str(c.get("id") or "") for c in normalized_candidates]
        if not all(ids) or len(ids) != len(set(ids)):
            raise ValueError("candidates require unique non-empty ids")
        for candidate in normalized_candidates:
            if not candidate.get("profile") or not candidate.get("model"):
                raise ValueError(f"candidate {candidate.get('id')!r} requires profile and model")
            if "effort" not in candidate:
                raise ValueError(f"candidate {candidate.get('id')!r} requires effort (use 'n/a' when unsupported)")
        treatments = [str(t) for t in data["treatments"]]
        if not all(treatments) or len(treatments) != len(set(treatments)):
            raise ValueError("treatments require unique non-empty ids")
        repeats = dict(data["repeats"])
        if any(not isinstance(value, int) or value < 1 for value in repeats.values()):
            raise ValueError("repeat values must be positive integers")
        lock_path = Path(str(data["corpus_lock"]))
        if not lock_path.is_absolute():
            lock_path = (p.parent / lock_path).resolve()
        return TaskOptimizationStudy(
            study_id=str(data["study_id"]), boundary=str(data["boundary"]), corpus_lock=str(lock_path),
            splits={str(k): str(v) for k, v in dict(data["splits"]).items()},
            candidates=normalized_candidates, treatments=treatments, repeats=repeats,
            stop=dict(data["stop"]), live_gate_env=str(data.get("live_gate_env") or "KITSOKI_TASK_OPT_LIVE"),
            blocked_by=blocked_by, path=str(p),
        )

    def corpus(self) -> dict[str, Any]:
        path = Path(self.corpus_lock)
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            raise ValueError(f"corpus lock must be JSON: {exc}") from exc
        if not isinstance(data, dict):
            raise ValueError("corpus lock must be a JSON object")
        if data.get("schema") != BUGSWARM_CORPUS_LOCK_SCHEMA:
            raise ValueError("corpus lock has an unsupported schema")
        if data.get("status") != "ready":
            raise ValueError("corpus lock is not ready")
        actual_digest = bugswarm_lock_sha256(data)
        if data.get("lock_sha256") != actual_digest:
            raise ValueError("corpus lock_sha256 does not match its contents")
        source_sha = str(data.get("source_sha256") or "")
        if len(source_sha) != 64 or any(char not in "0123456789abcdef" for char in source_sha.lower()):
            raise ValueError("corpus lock requires a SHA-256 source_sha256")
        selection = data.get("selection")
        if not isinstance(selection, dict) or selection.get("repository_separated") is not True:
            raise ValueError("corpus lock requires repository_separated selection")
        expected = {"learning": selection.get("learning_count"), "confirmation": selection.get("confirmation_count")}
        if any(not isinstance(count, int) or count < 1 for count in expected.values()):
            raise ValueError("corpus lock selection requires positive learning_count and confirmation_count")
        tasks = data.get("tasks")
        if not isinstance(tasks, list) or not tasks:
            raise ValueError("corpus lock requires a non-empty tasks list")
        if len(tasks) != sum(expected.values()):
            raise ValueError("corpus lock task count does not match its selection")
        seen: set[str] = set()
        repositories: set[str] = set()
        split_counts = {"learning": 0, "confirmation": 0}
        for task in tasks:
            if not isinstance(task, dict) or not task.get("id") or not task.get("split"):
                raise ValueError("every corpus task requires id and split")
            if task["id"] in seen:
                raise ValueError(f"duplicate corpus task id {task['id']!r}")
            seen.add(task["id"])
            if task["split"] not in self.splits:
                raise ValueError(f"corpus task {task['id']!r} has unknown split {task['split']!r}")
            if task["split"] not in split_counts:
                raise ValueError(f"corpus task {task['id']!r} has unsupported lock split {task['split']!r}")
            split_counts[task["split"]] += 1
            repository = str(task.get("repository") or "")
            if not repository or repository in repositories:
                raise ValueError(f"corpus task {task['id']!r} lacks a repository-distinct provenance record")
            repositories.add(repository)
            provenance = ("image_digest", "commits", "verification", "public_task", "hidden_oracle")
            if any(not task.get(key) for key in provenance):
                raise ValueError(f"corpus task {task['id']!r} lacks required provenance")
        if split_counts != expected:
            raise ValueError("corpus lock split counts do not match its selection")
        return data

    def plan(self, *, repeat_phase: str = "screening") -> dict[str, Any]:
        if repeat_phase not in self.repeats:
            raise ValueError(f"unknown repeat phase {repeat_phase!r}; known: {sorted(self.repeats)}")
        corpus = self.corpus()
        cells: list[dict[str, Any]] = []
        for task in sorted(corpus["tasks"], key=lambda t: str(t["id"])):
            for candidate in sorted(self.candidates, key=lambda c: str(c["id"])):
                for treatment in sorted(self.treatments):
                    for repeat in range(1, self.repeats[repeat_phase] + 1):
                        cell_id = f"{task['id']}--{candidate['id']}--{treatment}--r{repeat}"
                        cells.append({"id": cell_id, "task_id": task["id"], "split": task["split"],
                                      "candidate_id": candidate["id"], "treatment": treatment,
                                      "repeat": repeat, "status": "planned"})
        return {"schema": TASK_OPTIMIZATION_SCHEMA, "study_id": self.study_id, "repeat_phase": repeat_phase,
                "cell_count": len(cells), "cells": cells}

    def lock(self) -> dict[str, Any]:
        corpus_bytes = Path(self.corpus_lock).read_bytes()
        study_bytes = Path(self.path).read_bytes()
        runner_commit = ""
        try:
            runner_commit = subprocess.run(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, text=True,
                                           capture_output=True, check=True).stdout.strip()
        except (OSError, subprocess.CalledProcessError):
            runner_commit = "unknown"
        return {"schema": TASK_OPTIMIZATION_SCHEMA, "study_id": self.study_id, "boundary": self.boundary,
                "study_manifest": self.path, "study_manifest_sha256": _sha256(study_bytes),
                "corpus_lock": self.corpus_lock, "corpus_lock_sha256": _sha256(corpus_bytes),
                "splits": self.splits, "candidates": self.candidates, "treatments": sorted(self.treatments),
                "repeats": self.repeats, "stop": self.stop, "live_gate_env": self.live_gate_env,
                "runner_commit": runner_commit}

    def preflight(self, resolutions: dict[str, Any]) -> dict[str, Any]:
        """Validate local profile resolution without dispatching a provider.

        ``resolutions`` is deliberately an operator-produced receipt rather
        than a profile lookup.  This makes requested versus effective settings
        auditable and forbids hidden aliases/fallbacks before a live cell can
        be armed.
        """
        required = ("effective_model", "effective_effort", "provider", "backend", "profile_hash", "launch_plan_hash")
        records: list[dict[str, Any]] = []
        for candidate in self.candidates:
            configured = resolutions.get(candidate["id"], {})
            if not isinstance(configured, dict):
                configured = {}
            record = {
                "candidate_id": candidate["id"],
                "profile": candidate["profile"],
                "requested_model": candidate["requested_model"],
                "requested_effort": candidate["requested_effort"],
                "effective_model": configured.get("effective_model"),
                "effective_effort": configured.get("effective_effort"),
                "provider": configured.get("provider"),
                "backend": configured.get("backend"),
                "profile_hash": configured.get("profile_hash"),
                "launch_plan_hash": configured.get("launch_plan_hash"),
            }
            declared = str(configured.get("status") or candidate.get("preflight") or "")
            missing = [key for key in required if not record.get(key)]
            if declared == "unsupported" or declared.startswith("unsupported-"):
                record["status"] = "unsupported"
                record["reason"] = str(configured.get("reason") or declared)
            elif missing:
                record["status"] = "invalid"
                record["reason"] = "missing " + ", ".join(missing)
            elif record["effective_model"] != record["requested_model"]:
                record["status"] = "invalid"
                record["reason"] = "effective_model differs from requested_model; fallback is forbidden"
            elif record["effective_effort"] != record["requested_effort"]:
                record["status"] = "invalid"
                record["reason"] = "effective_effort differs from requested_effort"
            else:
                record["status"] = "ready"
            records.append(record)
        result = {"schema": TASK_OPTIMIZATION_PREFLIGHT_SCHEMA, "study_id": self.study_id,
                  "study_lock_sha256": receipt_sha256(self.lock()), "candidates": records}
        result["preflight_sha256"] = receipt_sha256(result)
        return result


def load_task_optimization_receipts(path: str | Path, *, plan: dict[str, Any], preflight: dict[str, Any]) -> list[dict[str, Any]]:
    """Load immutable result receipts and reject mismatched/corrupt evidence."""
    root = Path(path)
    files = [root] if root.is_file() else sorted(root.rglob("*.json")) if root.exists() else []
    plan_ids = {str(cell["id"]) for cell in plan.get("cells", [])}
    plan_cells = {str(cell["id"]): cell for cell in plan.get("cells", [])}
    preflight_candidates = {str(record.get("candidate_id")): record for record in preflight.get("candidates", []) if isinstance(record, dict)}
    plan_digest = receipt_sha256(plan)
    preflight_digest = str(preflight.get("preflight_sha256") or receipt_sha256(preflight))
    receipts: list[dict[str, Any]] = []
    seen: set[str] = set()
    for file in files:
        data = json.loads(file.read_text(encoding="utf-8"))
        if data.get("schema") != TASK_OPTIMIZATION_ATTEMPT_SCHEMA:
            raise ValueError(f"{file}: unsupported attempt receipt schema")
        attempt_id = str(data.get("attempt_id") or "")
        cell_id = str(data.get("cell_id") or "")
        if not attempt_id or attempt_id in seen:
            raise ValueError(f"{file}: attempt_id must be unique and non-empty")
        if cell_id not in plan_ids:
            raise ValueError(f"{file}: cell_id {cell_id!r} is absent from the plan")
        candidate_id = str(data.get("candidate_id") or "")
        if candidate_id != str(plan_cells[cell_id].get("candidate_id") or ""):
            raise ValueError(f"{file}: candidate_id does not match the planned cell")
        if preflight_candidates.get(candidate_id, {}).get("status") != "ready":
            raise ValueError(f"{file}: candidate {candidate_id!r} lacks a ready preflight receipt")
        if data.get("plan_sha256") != plan_digest:
            raise ValueError(f"{file}: plan_sha256 does not match immutable plan")
        if data.get("preflight_sha256") != preflight_digest:
            raise ValueError(f"{file}: preflight_sha256 does not match immutable preflight")
        status = str(data.get("status") or "")
        if status not in TASK_OPTIMIZATION_CELL_STATES:
            raise ValueError(f"{file}: invalid task-optimization status {status!r}")
        seen.add(attempt_id)
        receipts.append(data)
    return receipts


def task_optimization_status(plan: dict[str, Any], receipts: list[dict[str, Any]], *, champion: dict[str, Any] | None = None) -> dict[str, Any]:
    """Aggregate append-only attempts into resume and campaign phase state."""
    by_cell: dict[str, list[dict[str, Any]]] = {str(cell["id"]): [] for cell in plan["cells"]}
    for receipt in receipts:
        by_cell[receipt["cell_id"]].append(receipt)
    cells: list[dict[str, Any]] = []
    for cell in plan["cells"]:
        attempts = by_cell[cell["id"]]
        statuses = {str(item["status"]) for item in attempts}
        if statuses & set(TASK_OPTIMIZATION_TERMINAL_STATES):
            state = next(state for state in TASK_OPTIMIZATION_TERMINAL_STATES if state in statuses)
        elif "running" in statuses or "armed" in statuses:
            state = "running"
        else:
            state = "planned"
        cells.append({**cell, "status": state, "attempt_count": len(attempts),
                      "attempt_ids": sorted(str(item["attempt_id"]) for item in attempts)})
    learning = [cell for cell in cells if cell["split"] == "learning"]
    confirmation = [cell for cell in cells if cell["split"] == "confirmation"]
    learning_complete = bool(learning) and all(cell["status"] in TASK_OPTIMIZATION_TERMINAL_STATES for cell in learning)
    confirmation_complete = bool(confirmation) and all(cell["status"] in TASK_OPTIMIZATION_TERMINAL_STATES for cell in confirmation)
    phase = "learning"
    if learning_complete:
        phase = "champion_selection"
    if champion is not None:
        phase = "complete" if confirmation_complete else "confirmation"
    return {"schema": "task-optimization/status/v1", "study_id": plan["study_id"], "plan_sha256": receipt_sha256(plan),
            "phase": phase, "learning_complete": learning_complete, "confirmation_complete": confirmation_complete,
            "resume_cell_ids": [cell["id"] for cell in cells if cell["status"] in {"planned", "running"}], "cells": cells}


def select_task_optimization_champion(plan: dict[str, Any], receipts: list[dict[str, Any]]) -> dict[str, Any]:
    """Freeze a deterministic champion after all learning cells terminate."""
    status = task_optimization_status(plan, receipts)
    if not status["learning_complete"]:
        raise ValueError("cannot select champion until every learning cell has a terminal receipt")
    scores: dict[str, dict[str, int]] = {}
    for cell in status["cells"]:
        if cell["split"] != "learning":
            continue
        score = scores.setdefault(cell["candidate_id"], {"solved": 0, "scored": 0, "tokens": 0})
        terminal = [item for item in receipts if item["cell_id"] == cell["id"] and item["status"] in TASK_OPTIMIZATION_TERMINAL_STATES]
        latest = terminal[-1]
        score["scored"] += 1
        if latest.get("verdict") == "solved":
            score["solved"] += 1
        score["tokens"] += int((latest.get("metrics") or {}).get("total_tokens") or 0)
    if not scores:
        raise ValueError("learning plan contains no candidate cells")
    winner = min(scores, key=lambda candidate_id: (-scores[candidate_id]["solved"], -scores[candidate_id]["scored"], scores[candidate_id]["tokens"], candidate_id))
    champion = {"schema": TASK_OPTIMIZATION_CHAMPION_SCHEMA, "study_id": plan["study_id"],
                "plan_sha256": receipt_sha256(plan), "candidate_id": winner, "scores": scores}
    champion["champion_sha256"] = receipt_sha256(champion)
    return champion

# Check-type discriminator (WS-G G1): a cell declares a check SUITE, not one
# verdict shape, and every check emits a completion-state verdict tagged with
# its type (schemas/completion-state.schema.json `check_type`; absent == the
# default "replay"). `replay` is the container/plugin path below. `journey-
# verdict`/`ux-heuristic` (WS-G G6) are FILE-ADAPTER checks: they read an
# already-written kitsoki-ui-qa / kitsoki-ui-review `verdict.json` off disk via
# tools.persona_qa.ui_verdict — no container spawn, no LLM call of their own
# (see arena/checks.py's `run_ui_verdict_check`). `docs-fidelity` remains
# declared-but-not-implemented: specs may declare it (validation accepts it)
# and execution reports an honest `pending` verdict, never a fake green.
CHECK_TYPES = ("replay", "docs-fidelity", "ux-heuristic", "journey-verdict")
DEFAULT_CHECK_TYPE = "replay"
# Check types execution never falls back to `unimplemented_check_result` for.
# `replay` runs a container; `journey-verdict`/`ux-heuristic` run the file
# adapter (arena/checks.py FILE_ADAPTER_CHECK_TYPES) — both can still yield an
# honest `pending` verdict at the CellResult level (e.g. no verdict.json
# configured/found yet), but the check_type itself is implemented.
IMPLEMENTED_CHECK_TYPES = ("replay", "journey-verdict", "ux-heuristic")


@dataclass(frozen=True)
class CheckSpec:
    """One declared check in a cell's check suite: a type + how to run/collect.

    `options` is the check-type-specific "how" (e.g. a future docs-fidelity
    check's docs path or persona id); the replay check needs none — its "how"
    is the job-type plugin itself.
    """

    check_type: str = DEFAULT_CHECK_TYPE
    options: dict[str, Any] = field(default_factory=dict)

    def __post_init__(self) -> None:
        if self.check_type not in CHECK_TYPES:
            raise ValueError(
                f"unknown check_type {self.check_type!r}; known: {list(CHECK_TYPES)}"
            )


def _check_spec_from(entry: Any) -> CheckSpec:
    """Parse one spec-file `checks:` entry — a bare type string or a mapping."""
    if isinstance(entry, str):
        return CheckSpec(check_type=entry)
    if isinstance(entry, dict):
        data = dict(entry)
        check_type = data.pop("check_type", data.pop("type", DEFAULT_CHECK_TYPE))
        options = data.pop("options", None)
        if options is None:
            options = data  # remaining keys fold into options (least surprise)
        return CheckSpec(check_type=check_type, options=dict(options))
    raise ValueError(f"checks entry must be a string or mapping, got {entry!r}")


@dataclass
class CellResult:
    """A graded cell. `verdict` is from VERDICTS; `health` separates INFRA from MODEL."""

    cell_id: str
    job_type: str
    target_id: str
    variant_id: str
    axis: dict[str, str] = field(default_factory=dict)
    verdict: str = "pending"
    health: str = "incomplete"          # infra:* | model:result | incomplete
    metrics: dict[str, Any] = field(default_factory=dict)   # cost_usd, tokens, wall_s
    evidence_refs: list[str] = field(default_factory=list)
    trace_ref: str = ""
    notes: str = ""
    # Which proof class this verdict grades (WS-G G1). Default "replay" — the
    # only value pre-check-suite consumers ever meant.
    check_type: str = DEFAULT_CHECK_TYPE

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        # Schema semantics: absent check_type == "replay". Omitting the default
        # keeps every pre-check-suite consumer (and the golden rollup bytes)
        # unchanged; only non-replay checks carry the discriminator explicitly.
        if self.check_type == DEFAULT_CHECK_TYPE:
            d.pop("check_type")
        return d


@dataclass
class Placement:
    """Where cell containers run. A host is a docker context; `local` = local daemon."""

    hosts: list[str] = field(default_factory=lambda: ["local"])
    concurrency: int = 1
    retry: int = 0
    # Where the kitsoki checkout lives on each host's daemon. A remote docker
    # context resolves `-v` paths on the REMOTE filesystem, so a VM host must
    # point at the checkout ON THE VM. `local` defaults to the local REPO_ROOT
    # (filled in by the CLI); any host absent here falls back to that default.
    host_repo: dict[str, str] = field(default_factory=dict)


def _target_from_dict(t: dict[str, Any]) -> Target:
    return Target(
        id=t["id"],
        label=t.get("label", t["id"]),
        repo=t.get("repo", ""),
        stack=t.get("stack", ""),
        meta={k: v for k, v in t.items() if k not in {"id", "label", "repo", "stack"}},
    )


def load_targets_from_corpus(path: str | Path) -> list[Target]:
    """Materialize Targets from a corpus, in either of two shapes.

    - A single file: the github-targets.json shape,
      `{"targets": [{"id", "label", "repo", "stack", ...}, ...]}` (product-
      journey remains its owner, read-only here).
    - A directory: one JSON document per file, each document itself already
      target-shaped (requires an `"id"` field; everything else — however
      many extra keys — folds into `Target.meta` via `_target_from_dict`,
      same as an inlined target would). This is how `usable-kitsoki-gate`
      (S6) consumes S4's scenario-foundry IR corpus
      (`tools/session-mining/calibration/`, schema
      `tools/session-mining/schema/scenario_ir.schema.json`) as its cell
      enumeration: each `scn-*.json` scenario IR doc becomes one Target
      (`id` = the scenario id, `meta` carries `persona`, `goal`,
      `expected_effects`, `abandoned`, `provenance`, etc. verbatim) without
      this module needing any scenario-IR-specific parsing — it is already
      a generic "directory of target-shaped JSON docs" loader. Non-`.json`
      siblings (e.g. a `MANIFEST.md`) and subdirectories (e.g. a `flows/`
      compiled-fixture dir) are ignored; files are read in sorted order so
      enumeration is deterministic.
    """
    p = _resolve_repo_path(path)
    if p.is_dir():
        docs = [
            json.loads(f.read_text(encoding="utf-8")) for f in sorted(p.glob("*.json"))
        ]
        return [_target_from_dict(t) for t in docs]
    data = json.loads(p.read_text(encoding="utf-8"))
    return [_target_from_dict(t) for t in data.get("targets", [])]


def load_target_proof_checks(path: str | Path) -> dict[str, dict[str, Any]]:
    """Load a product-journey target-proof.json's per-target checks by target id.

    Accepts either the proof file itself or its containing directory (the
    proof dir convention used by `.artifacts/product-journey/target-proofs/<id>/`).
    Returns {} if no proof is found — proof is optional metadata, never required.
    """
    p = _resolve_repo_path(path)
    if p.is_dir():
        p = p / "target-proof.json"
    if not p.exists():
        return {}
    data = json.loads(p.read_text(encoding="utf-8"))
    return {c["target"]: c for c in data.get("checks", []) if "target" in c}


def load_persona_axis_values(path: str | Path) -> list[str]:
    """Load persona ids from a personas.json-shaped file for use as axis values."""
    data = json.loads(_resolve_repo_path(path).read_text(encoding="utf-8"))
    return [p["id"] for p in data.get("personas", [])]


@dataclass
class JobSpec:
    """Declarative comparison job. Enumerates into cells (target × variant × axes)."""

    job_type: str
    targets: list[Target]
    variants: list[Variant]
    axes: dict[str, list[str]] = field(default_factory=dict)   # extra per-job axes
    placement: Placement = field(default_factory=Placement)
    options: dict[str, Any] = field(default_factory=dict)      # job-type-specific knobs
    # The cell's check suite (WS-G G1). Every cell in the job runs every
    # declared check; aggregation is one verdict per cell per check_type.
    # Default: the single replay check — exactly the pre-check-suite behavior.
    checks: list[CheckSpec] = field(default_factory=lambda: [CheckSpec()])
    # Corpus provenance (informational; the fields above are already resolved).
    targets_from: str = ""
    persona_axis_from: str = ""
    target_proof_from: str = ""

    # ---- loading -------------------------------------------------------------
    @staticmethod
    def from_dict(data: dict[str, Any]) -> "JobSpec":
        targets_from = data.get("targets_from", "") or ""
        if data.get("targets"):
            # Hand-inlined targets take precedence and keep working unchanged.
            targets = [_target_from_dict(t) for t in data["targets"]]
        elif targets_from:
            targets = load_targets_from_corpus(targets_from)
        else:
            targets = []

        target_proof_from = data.get("target_proof_from", "") or ""
        if target_proof_from:
            proof_checks = load_target_proof_checks(target_proof_from)
            if proof_checks:
                targets = [
                    Target(
                        id=t.id,
                        label=t.label,
                        repo=t.repo,
                        stack=t.stack,
                        meta={**t.meta, "target_proof": proof_checks[t.id]}
                        if t.id in proof_checks
                        else t.meta,
                    )
                    for t in targets
                ]

        variants = [
            Variant(
                id=v["id"],
                backend=v.get("backend", ""),
                model=v.get("model", ""),
                effort=v.get("effort", ""),
                meta={k: val for k, val in v.items() if k not in {"id", "backend", "model", "effort"}},
            )
            for v in data.get("variants", [])
        ]

        axes = dict(data.get("axes", {}) or {})
        persona_axis_from = data.get("persona_axis_from", "") or ""
        if persona_axis_from and "persona" not in axes:
            # Hand-inlined `axes.persona` (if any) always wins — this only
            # fills in the axis when the spec didn't hand-copy persona ids.
            axes["persona"] = load_persona_axis_values(persona_axis_from)

        checks_data = data.get("checks") or []
        checks = [_check_spec_from(c) for c in checks_data] or [CheckSpec()]
        # A duplicated check_type would double-report a verdict per type —
        # reject it at load time rather than surprising the rollup.
        seen_types = [c.check_type for c in checks]
        if len(seen_types) != len(set(seen_types)):
            raise ValueError(f"duplicate check_type in checks: {seen_types}")

        place = data.get("placement", {}) or {}
        return JobSpec(
            job_type=data["job_type"],
            targets=targets,
            variants=variants,
            axes=axes,
            placement=Placement(
                hosts=place.get("hosts", ["local"]),
                concurrency=int(place.get("concurrency", 1)),
                retry=int(place.get("retry", 0)),
                host_repo=dict(place.get("host_repo", {}) or {}),
            ),
            options=data.get("options", {}) or {},
            checks=checks,
            targets_from=targets_from,
            persona_axis_from=persona_axis_from,
            target_proof_from=target_proof_from,
        )

    @staticmethod
    def load(path: str | Path) -> "JobSpec":
        text = Path(path).read_text(encoding="utf-8")
        try:
            import yaml  # type: ignore

            data = yaml.safe_load(text)
        except ModuleNotFoundError:
            data = json.loads(text)
        return JobSpec.from_dict(data)

    # ---- enumeration ---------------------------------------------------------
    def cells(self) -> list[Cell]:
        """Cross-product targets × variants × every axis → deterministic cell list."""
        axis_names = sorted(self.axes)
        axis_value_lists = [self.axes[name] for name in axis_names]
        combos = list(itertools.product(*axis_value_lists)) if axis_value_lists else [()]
        out: list[Cell] = []
        for target in self.targets:
            for variant in self.variants:
                for combo in combos:
                    axis = {name: val for name, val in zip(axis_names, combo)}
                    cid = "--".join(
                        [target.id, variant.id] + [f"{n}:{axis[n]}" for n in axis_names]
                    )
                    out.append(
                        Cell(
                            id=cid,
                            job_type=self.job_type,
                            target=target,
                            variant=variant,
                            axis=axis,
                            options=dict(self.options),
                        )
                    )
        return out
