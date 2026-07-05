"""usable-kitsoki-gate — the three gate conditions + the parity threshold, as
CONSTANTS (not prose), per docs/proposals/usable-kitsoki-release-gate.md
Task 1.2.

This is the single source of truth S1 (workbench), S4 (scenario foundry), and
S6 (this arena plugin, `usable_kitsoki_gate.py`, Task 2) all read the same
number from. S1/S4 are not yet built as of this commit — until they exist in
their own language/runtime, treat this module as the canonical value and
mirror it verbatim rather than re-deriving it; docs/tracing/usable-kitsoki-gate.md
points back here for the producer-contract side.

Do not inline `90` (or any of these) anywhere else in the gate's rollup logic
— import from here so a threshold change is a one-line diff with a single
blast radius.
"""

from __future__ import annotations

from typing import Final

# ---------------------------------------------------------------------------
# Open question 1 (parity threshold X): resolved as one global floor, not a
# per-persona floor, per the proposal's own lean — "start with one global
# floor calibrated against the 20-scenario calibration set; split per-persona
# only if the calibration run shows a persona systematically dragging the
# number down for reasons unrelated to workbench quality."
#
# 90% is a PLACEHOLDER pinned to that lean, not a measured number — no
# calibration set exists yet (S4 Task 4.2, gated on S4 landing). Whoever runs
# the first calibration pass (epic §5's "20 mined Kitsoki scenarios, hand-
# checked") must revisit this constant, cite the run that justified it in the
# comment right here, and get Brad's sign-off before it gates a real release
# decision instead of a placeholder for wiring the plugin.
# ---------------------------------------------------------------------------
PARITY_THRESHOLD_PERCENT: Final[float] = 90.0

# Open question 2 (surface parity vs. worst-surface gating): resolved as
# worst-surface gating, per the proposal's lean — "a product that's
# productive on web but silently bounces on MCP is not released-ready, and
# averaging would hide exactly that." The rollup computes the parity
# percentage PER SURFACE and the cell's overall parity is the MINIMUM across
# surfaces, never an average.
WORST_SURFACE_GATING: Final[bool] = True

# The three gate conditions (Event/format model, "The three gate conditions").
# All three must hold for the rollup to pass; each maps directly to an epic
# release-blocker. Encoded as machine-checkable predicate names (not prose)
# so a rollup implementation and a test can both enumerate
# `GATE_CONDITIONS` and know they covered all three.
GATE_CONDITIONS: Final[tuple[str, ...]] = (
    "zero_silent_bounce",     # kills R2 regressions: no record has silent_bounce == true
    "zero_misroute_adjacent",  # kills R3 regressions: no record has misroute_adjacent == true
    "parity_at_or_above_threshold",  # see PARITY_THRESHOLD_PERCENT / WORST_SURFACE_GATING
)


def parity_percent(candidate_and_source_completed: int, source_completed: int) -> float:
    """count(candidate_completed AND source_completed) / count(source_completed) * 100.

    Binary completion, not a scored rubric (epic shared decision 6). Returns
    100.0 for an empty denominator (no scenarios where the source completed
    means there is nothing to have regressed on for this surface) so an
    empty-corpus cell does not spuriously fail the parity condition; callers
    that care about "no scenarios ran at all" should gate on that separately
    (health, not this metric).
    """
    if source_completed <= 0:
        return 100.0
    return 100.0 * candidate_and_source_completed / source_completed


def gate_passes(
    *,
    silent_bounce_count: int,
    misroute_adjacent_count: int,
    worst_surface_parity_percent: float,
) -> bool:
    """The rollup predicate: all three GATE_CONDITIONS must hold.

    `worst_surface_parity_percent` is expected to already be the MINIMUM
    parity percentage across surfaces when WORST_SURFACE_GATING is True (the
    caller computes per-surface `parity_percent()` values and reduces with
    `min()`; this function does not re-derive per-surface data because it has
    no access to the per-record list — see `usable_kitsoki_gate.py`'s
    rollup for that reduction).
    """
    return (
        silent_bounce_count == 0
        and misroute_adjacent_count == 0
        and worst_surface_parity_percent >= PARITY_THRESHOLD_PERCENT
    )
