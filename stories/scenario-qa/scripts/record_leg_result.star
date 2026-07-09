# record_leg_result.star — append the current leg's driver+judge outcome to
# world.leg_results (read from ctx.world, not templated in as an input —
# mirrors stories/dogfood-marathon's record_case.star — so a stale templated
# copy can never race the real accumulator), then recompute the running
# pass/fail/degraded tallies. Nothing is fabricated: every field comes from
# the driver's own report and the judge's own verdict.
#
# Interface (authoritative in record_leg_result.star.yaml):
#   inputs:  leg (object), drive_result (object), judge_result (object)
#   world:   leg_results (object {items:[...]})
#   outputs: leg_results (object {items:[...]}), pass_count (int),
#            fail_count (int), degraded_count (int)

def _d(v):
    return v if type(v) == "dict" else {}


def _lacks_evidence(drive):
    # Mirrors judge_leg.md's own instruction verbatim (docs/persona-qa.md:
    # never let a leg's evidence be
    # "false-complete"): the judge is TOLD its verdict must be
    # "degraded-evidence" whenever the driver reported "blocked" /
    # "degraded-evidence", or produced no evidence_refs. That is only a
    # prompt instruction -- a model that slips (or a dead driver dispatch
    # that leaves drive_result at its stale/empty {} default via
    # execute.yaml's on_error: judge route) must not be able to turn a
    # missing capture into a recorded "pass". This is the deterministic
    # backstop for that same rule, evaluated in code rather than trusted to
    # the judge's compliance.
    status = drive.get("status", "")
    if status == "blocked" or status == "degraded-evidence":
        return True
    evidence_refs = drive.get("evidence_refs", [])
    if type(evidence_refs) != "list":
        return True
    return len(evidence_refs) == 0


def _vscode_missing_post_drive(leg, drive):
    # vscode legs are bridge-level proof (see _evidence_level below) and the
    # driver's transport-pin preflight (product-journey-qa-driver.md) only
    # proves the bridge is REACHABLE before the scenario is driven -- it does
    # NOT prove the scenario's operator-visible outcome, because the rest of
    # the leg is usually driven through a different surface (session.submit /
    # a live text turn) that advances the SAME session to a new state (e.g.
    # landing -> s2). Without a second vscode capture taken AFTER that drive,
    # bound to the post-drive session_handle, the only vscode evidence on file
    # is the pre-drive snapshot -- proof the bridge existed, not proof of the
    # scenario outcome. Require drive_result.post_drive_evidence_ref (see
    # drive_leg.md / drive_leg_result.json) before a vscode leg can be scored
    # anything other than degraded-evidence. This is the deterministic
    # backstop for the same rule the driver/judge prompts are told in prose.
    if leg.get("transport", "") != "vscode":
        return False
    return drive.get("post_drive_evidence_ref", "") == ""


def _evidence_level(leg):
    # Every leg carries its own transport_evidence_contract (plan_legs.star
    # read it straight off driver-plan.json / carried it through the ad-hoc
    # draft) -- prefer that. Falls back to a bare transport-id lookup mirroring
    # tools/product-journey/run.py's TRANSPORT_EVIDENCE_CONTRACTS (schema.json's
    # authoritative source) so a leg missing the contract dict (e.g. an older
    # recording) still gets labeled. vscode is always "bridge-level" -- the IDE
    # bridge stub/recording path, never a genuine editor (see
    # docs/persona-qa.md) -- never mistake it for
    # editor-level coverage in the report.
    contract = _d(leg.get("transport_evidence_contract"))
    if contract.get("level", "") != "":
        return contract["level"]
    transport = leg.get("transport", "")
    if transport == "vscode":
        return "bridge-level"
    if transport == "cli":
        return "terminal-level"
    if transport == "tui" or transport == "web":
        return "frame-level"
    return ""


def _natural_utterance_summary(leg):
    utterances = leg.get("natural_utterances", [])
    if type(utterances) != "list" or len(utterances) == 0:
        return {"count": 0, "example": "", "sources": []}

    sources = []
    example = ""
    for item in utterances:
        if type(item) != "dict":
            continue
        if example == "":
            example = item.get("text", "")
        source = item.get("source_ref", "")
        if source != "" and source not in sources:
            sources.append(source)
    return {
        "count": len(utterances),
        "example": example,
        "sources": sources,
    }


def main(ctx):
    leg = _d(ctx.inputs.get("leg"))
    drive = _d(ctx.inputs.get("drive_result"))
    judge = _d(ctx.inputs.get("judge_result"))

    leg_results = ctx.world.get("leg_results") or {"items": []}
    if type(leg_results) != "dict":
        leg_results = {"items": []}
    items = list(leg_results.get("items", []))

    verdict = judge.get("verdict", "unjudged")
    # Verdicts the judge itself never rendered ("unjudged", e.g. a dead judge
    # dispatch via judge.yaml's on_error: recording route) are left alone --
    # that is a different, already-honest failure mode, not a fabricated
    # pass. Anything the judge DID render (pass/fail/unsupported/
    # degraded-evidence) is downgraded to "degraded-evidence" when the
    # driver's own report proves there was nothing real to judge.
    if verdict != "unjudged" and _lacks_evidence(drive):
        verdict = "degraded-evidence"
    if verdict != "unjudged" and _vscode_missing_post_drive(leg, drive):
        verdict = "degraded-evidence"

    natural = _natural_utterance_summary(leg)
    record = {
        "leg_id":                  leg.get("leg_id", ""),
        "scenario":                leg.get("scenario", ""),
        "transport":               leg.get("transport", ""),
        "visual_surface":          leg.get("visual_surface", ""),
        "evidence_level":          _evidence_level(leg),
        "natural_utterance_count": natural["count"],
        "natural_utterance_example": natural["example"],
        "natural_utterance_sources": natural["sources"],
        "driver_status":           drive.get("status", "unattempted"),
        "evidence_refs":           drive.get("evidence_refs", []),
        "post_drive_evidence_ref": drive.get("post_drive_evidence_ref", ""),
        "verdict":                 verdict,
        "verdict_summary":         judge.get("summary", ""),
        "frames_dir":              judge.get("frames_dir", drive.get("frames_dir", "")),
    }
    items.append(record)

    passes = 0
    fails = 0
    degraded = 0
    for item in items:
        v = item.get("verdict", "")
        if v == "pass":
            passes += 1
        elif v == "degraded-evidence":
            degraded += 1
        elif v != "" and v != "unjudged":
            fails += 1

    return {
        "leg_results": {"items": items},
        "pass_count": passes,
        "fail_count": fails,
        "degraded_count": degraded,
    }
