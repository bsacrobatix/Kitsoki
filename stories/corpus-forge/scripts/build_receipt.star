def main(ctx):
    candidates = ctx.inputs["candidates"]
    proofs = ctx.inputs["proofs"]
    if len(candidates) != len(proofs):
        fail("receipt requires one proof for every selected candidate")
    return {
        "receipt": {
            "kind": "corpus-receipt.v1",
            "selection_id": ctx.inputs["selection_id"],
            "candidate_count": len(candidates),
            "candidates": candidates,
            "proofs": proofs,
        },
    }
