# plan_legs.star — resolve the scenario-qa run bundle's driver-plan.json into
# the per-transport legs this story loops over.
#
# Catalog mode: driver-plan.json's "scenarios" list already IS the per-leg
# list (tools/product-journey/run.py's --transport axis expands one entry per
# scenario x transport leg, each carrying transport/leg_id/visual_surface/
# evidence_dir/transport_evidence_contract) — just read it back.
#
# Ad-hoc mode (a free-text description, no catalog scenario): run.py was
# still invoked (against a generic carrier scenario, "bugfix", which allows
# all three transports) purely to get real project/persona resolution and a
# transport-shaped leg skeleton. This drafts the actual scenario (id/label/
# task/success_criteria) from the description and splices it onto those
# carrier legs, replacing every carrier-specific field but keeping the
# transport-mechanical ones (transport, visual_surface, transport_evidence_
# contract, required_mcp) — those came from the SAME --transport flag this
# story already passed to run.py, so they already match what was requested.
#
# Takes `run_id` (a plain scalar, already fresh at machine-time from this
# room's FIRST invoke) rather than a pre-computed run_dir string -- deriving
# the repo-relative run dir HERE, inside this invoke's own script, is what
# lets the engine's dispatch-time re-render supply the fresh run_id even
# though this invoke is queued right after the one that binds it. A `set:`
# in the calling room computing the same string would bake in a stale value
# instead (see plan.yaml's header comment).
#
# Interface (authoritative in plan_legs.star.yaml):
#   inputs:  mode (string: catalog|adhoc), run_id (string),
#            description (string), primary_story (string)
#   outputs: legs (object {items:[...]}), leg_count (int),
#            driver_agent (string), run_dir_rel (string, repo-relative)

_ALPHANUM = "abcdefghijklmnopqrstuvwxyz0123456789"

def _slug(text):
    lowered = text.lower()
    out = ""
    last_dash = False
    for ch in lowered.elems():
        if ch in _ALPHANUM:
            out += ch
            last_dash = False
        elif len(out) > 0 and not last_dash:
            out += "-"
            last_dash = True
    if len(out) > 0 and out[-1] == "-":
        out = out[:-1]
    if len(out) > 40:
        out = out[:40]
    if out == "":
        out = "adhoc"
    return out


def _label(description):
    if len(description) <= 64:
        return description
    return description[:61] + "..."


def _draft_scenario(description, primary_story):
    scenario_id = "adhoc-" + _slug(description)
    return {
        "id": scenario_id,
        "label": _label(description),
        "primary_story": primary_story if primary_story != "" else "stories/dev-story/app.yaml",
        "task": description,
        "success_criteria": [
            "The described behavior is observable on the requested transport(s).",
            "Any broken or confusing behavior is captured as a concrete finding, not papered over.",
        ],
    }


def main(ctx):
    mode = ctx.inputs.get("mode", "catalog")
    run_id = ctx.inputs.get("run_id", "")
    # ARTIFACT_ROOT (tools/product-journey/run.py) is always the repo-relative
    # .artifacts/product-journey/<run_id> -- derive it here rather than
    # trusting a pre-computed value from the calling room (see this file's
    # header comment).
    run_dir_rel = ".artifacts/product-journey/" + run_id

    content = ctx.fs.read(run_dir_rel + "/driver-plan.json")
    plan = json.decode(content)
    carrier_legs = plan.get("scenarios", [])
    driver_agent = plan.get("driver_agent", ".agents/agents/product-journey-qa-driver.md")

    if mode != "adhoc":
        return {
            "legs": {"items": carrier_legs},
            "leg_count": len(carrier_legs),
            "driver_agent": driver_agent,
            "run_dir_rel": run_dir_rel,
            "plan_status": {"resolved": True},
        }

    description = ctx.inputs.get("description", "")
    primary_story = ctx.inputs.get("primary_story", "")
    scenario = _draft_scenario(description, primary_story)

    legs = []
    for leg in carrier_legs:
        drafted = dict(leg)
        transport = leg.get("transport", "tui")
        drafted["scenario"] = scenario["id"]
        drafted["label"] = scenario["label"]
        drafted["primary_story"] = scenario["primary_story"]
        drafted["task_prompt"] = scenario["task"]
        drafted["success_criteria"] = scenario["success_criteria"]
        drafted["evidence_dir"] = "evidence/" + scenario["id"] + "/" + transport
        drafted["leg_id"] = scenario["id"] + "::" + transport
        legs.append(drafted)

    return {
        "legs": {"items": legs},
        "leg_count": len(legs),
        "driver_agent": driver_agent,
        "run_dir_rel": run_dir_rel,
        "plan_status": {"resolved": True},
    }
