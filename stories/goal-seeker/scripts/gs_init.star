# gs_init.star — initialize the goal work_dir (the deterministic `init` step).
#
# Port of goal.py:init's log seeding. Ensures <work_dir>/log.jsonl exists so the
# append-only log has a home. When `reset` is true it TRUNCATES the log first — the
# "start a fresh goal run" affordance (and what makes the flow fixtures hermetic and
# re-runnable). In production reset defaults false, so an existing log (the source of
# truth across sessions) is preserved and only a missing one is created.


def main(ctx):
    work_dir = ctx.inputs["work_dir"].rstrip("/")
    path = work_dir + "/log.jsonl"
    reset = ctx.inputs.get("reset")
    if reset == True or reset == "true" or not ctx.fs.exists(path):
        ctx.fs.write(path, "")
    return {"log_path": path}
