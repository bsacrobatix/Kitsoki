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
import json
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Any


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

    def to_dict(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "job_type": self.job_type,
            "target": asdict(self.target),
            "variant": asdict(self.variant),
            "axis": dict(self.axis),
            "status": self.status,
        }


# Job-type-agnostic verdicts. Plugins map their native grade into these so the
# rollup can aggregate across any job type. (bugfix oracle, persona review gate,
# onboarding assertions all land in this vocabulary.)
VERDICTS = ("solved", "partial", "failed", "armed", "blocked", "pending")


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

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


@dataclass
class Placement:
    """Where cell containers run. A host is a docker context; `local` = local daemon."""

    hosts: list[str] = field(default_factory=lambda: ["local"])
    concurrency: int = 1
    retry: int = 0


@dataclass
class JobSpec:
    """Declarative comparison job. Enumerates into cells (target × variant × axes)."""

    job_type: str
    targets: list[Target]
    variants: list[Variant]
    axes: dict[str, list[str]] = field(default_factory=dict)   # extra per-job axes
    placement: Placement = field(default_factory=Placement)
    options: dict[str, Any] = field(default_factory=dict)      # job-type-specific knobs

    # ---- loading -------------------------------------------------------------
    @staticmethod
    def from_dict(data: dict[str, Any]) -> "JobSpec":
        targets = [
            Target(
                id=t["id"],
                label=t.get("label", t["id"]),
                repo=t.get("repo", ""),
                stack=t.get("stack", ""),
                meta={k: v for k, v in t.items() if k not in {"id", "label", "repo", "stack"}},
            )
            for t in data.get("targets", [])
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
        place = data.get("placement", {}) or {}
        return JobSpec(
            job_type=data["job_type"],
            targets=targets,
            variants=variants,
            axes=data.get("axes", {}) or {},
            placement=Placement(
                hosts=place.get("hosts", ["local"]),
                concurrency=int(place.get("concurrency", 1)),
                retry=int(place.get("retry", 0)),
            ),
            options=data.get("options", {}) or {},
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
                        )
                    )
        return out
