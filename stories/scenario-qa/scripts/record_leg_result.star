# record_leg_result.star — append the current leg's driver+judge outcome to
# world.leg_results (read from ctx.world, not templated in as an input —
# mirrors stories/dogfood-marathon's record_case.star — so a stale templated
# copy can never race the real accumulator), then recompute the running
# pass/fail/degraded tallies. Nothing is fabricated: every field comes from
# the driver's own report and the judge's own verdict.
#
# Issue group B (silent live-authorization fallback / bug #105 "replay-miss
# silently goes live") deterministic backstops live here, mirroring the
# existing _lacks_evidence/_vscode_missing_post_drive pattern -- a prompt
# instruction alone is never trusted as enforcement:
#   - `cause`: every leg whose verdict is not "pass" carries a machine- and
#     human-readable reason (missing evidence, a reported blocker, no live
#     authorization, an unauthorized live harness, or the judge's own
#     grounding) instead of a bare verdict with no stated why.
#   - `_unauthorized_live`: if the driver reports it opened this leg's
#     primary_story session with `harness_used: "live"` while this check
#     never supplied `profile=` (world.live_profile empty), that is a policy
#     violation regardless of what the judge said -- the verdict is forced to
#     "degraded-evidence" with an explicit cause. The MCP session runtime
#     already hard-fails a REPLAY session that hits an uncovered
#     host.agent.* call instead of silently falling through to live
#     (internal/mcp/studio/session_runtime.go's replayAgentMissHandler); this
#     is the story-level backstop for the other direction -- a driver that
#     decides on its own initiative to open a brand new LIVE session without
#     real authorization.
#
# Interface (authoritative in record_leg_result.star.yaml):
#   inputs:  leg (object), drive_result (object), judge_result (object),
#            live_profile (string, optional)
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


def _unauthorized_live(drive, live_profile):
    # A driver only has a legitimate `profile` to pass to a LIVE session.new
    # call when this check itself was authorized (world.live_profile
    # non-empty, threaded to the driver as args.live_profile -- see
    # drive_leg.md). If the driver reports it used a live harness anyway,
    # that is a policy violation the deterministic backstop must catch, not
    # a judgment call left to the driver's own prose.
    if live_profile != "":
        return False
    return str(drive.get("harness_used", "")) == "live"


def _cause(leg, drive, judge, verdict, unauthorized_live, live_profile):
    if verdict == "pass" or verdict == "unjudged":
        return ""

    reasons = []
    if unauthorized_live:
        reasons.append("policy violation: live harness used without profile= authorization")
    elif leg.get("needs_live_hint", False) and live_profile == "":
        reasons.append("no live authorization (profile= not supplied)")

    blockers = drive.get("blockers", [])
    if type(blockers) == "list":
        for b in blockers:
            text = str(b).strip()
            if text != "":
                reasons.append("blocker: " + text)

    if len(reasons) == 0:
        evidence_refs = drive.get("evidence_refs", [])
        if type(evidence_refs) != "list" or len(evidence_refs) == 0:
            reasons.append("no evidence captured for this leg")
        elif _vscode_missing_post_drive(leg, drive):
            reasons.append("vscode leg missing post-drive capture (preflight only proves bridge reachability, not the driven-forward state)")

    if len(reasons) == 0:
        judge_summary = str(judge.get("summary", "")).strip()
        if judge_summary != "":
            reasons.append(judge_summary)

    if len(reasons) == 0:
        reasons.append("driver reported '" + str(drive.get("status", "unattempted")) + "' with no further detail")

    return "; ".join(reasons)


def _playback_path(leg, drive):
    for source in [leg, drive]:
        for key in ["playback_path", "playback_ref", "rrweb_path", "video_path"]:
            value = source.get(key, "")
            if value != "":
                return value
    refs = drive.get("evidence_refs", [])
    if type(refs) != "list":
        return ""
    for ref in refs:
        if type(ref) == "dict":
            value = ref.get("path", ref.get("ref", ""))
        else:
            value = ref
        text = str(value)
        lower = text.lower()
        if lower.endswith(".rrweb.json") or lower.endswith(".mp4") or lower.endswith(".webm") or lower.endswith(".mov"):
            return text
    return ""


def main(ctx):
    leg = _d(ctx.inputs.get("leg"))
    drive = _d(ctx.inputs.get("drive_result"))
    judge = _d(ctx.inputs.get("judge_result"))
    live_profile = str(ctx.inputs.get("live_profile", "") or "")

    leg_results = ctx.world.get("leg_results") or {"items": []}
    if type(leg_results) != "dict":
        leg_results = {"items": []}
    items = list(leg_results.get("items", []))

    verdict = judge.get("verdict", "unjudged")
    unauthorized_live = _unauthorized_live(drive, live_profile)
    # Verdicts the judge itself never rendered ("unjudged", e.g. a dead judge
    # dispatch via judge.yaml's on_error: recording route) are left alone --
    # that is a different, already-honest failure mode, not a fabricated
    # pass. Anything the judge DID render (pass/fail/unsupported/
    # degraded-evidence) is downgraded to "degraded-evidence" when the
    # driver's own report proves there was nothing real to judge, when a
    # vscode leg never re-captured post-drive, or when the driver reports it
    # used a live harness this check never authorized (issue group B / bug
    # #105's story-level backstop -- see this file's header comment).
    if verdict != "unjudged" and _lacks_evidence(drive):
        verdict = "degraded-evidence"
    if verdict != "unjudged" and _vscode_missing_post_drive(leg, drive):
        verdict = "degraded-evidence"
    if verdict != "unjudged" and unauthorized_live:
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
        "harness_used":            drive.get("harness_used", ""),
        "playback_path":           _playback_path(leg, drive),
        "playback_caption":        leg.get("playback_caption", drive.get("playback_caption", "")),
        "verdict":                 verdict,
        "verdict_summary":         judge.get("summary", ""),
        "cause":                   _cause(leg, drive, judge, verdict, unauthorized_live, live_profile),
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
