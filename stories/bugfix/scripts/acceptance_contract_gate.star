def main(ctx):
    requirements = ctx.inputs["requirements"]
    artifact = ctx.inputs["artifact"]
    coverage = artifact.get("acceptance_coverage", [])
    by_id = {}
    for item in coverage:
        requirement_id = item.get("requirement_id", "")
        if requirement_id != "" and item.get("test_path", "") != "" and item.get("assertion", "") != "":
            by_id[requirement_id] = True
    missing = []
    for requirement in requirements:
        requirement_id = requirement.get("id", "")
        if requirement_id != "" and not by_id.get(requirement_id, False):
            missing.append(requirement_id)
    summary = "all public acceptance requirements have direct assertion receipts"
    if missing:
        summary = "missing direct assertion receipts for: " + ", ".join(missing)
    return {"gate": {"ok": len(missing) == 0, "missing": missing, "summary": summary}}
