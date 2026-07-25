# Validate one authority response before the integration train advances.
#
# Cryptographic seal creation and effect reconciliation belong to the bound
# authority host. This script fail-closes on structural drift, cross-phase
# identity mismatch, unstable item ordering, incomplete dispositions, queue
# target/SHA mismatch, and deployment mismatch.

PHASES = {
    "validate": 1,
    "integrate": 2,
    "gate": 3,
    "staging": 4,
    "main": 5,
    "deploy": 6,
}

HEX = "0123456789abcdef"

def _dict(v):
    return v if type(v) == "dict" else {}

def _list(v):
    return v if type(v) == "list" else []

def _str(v):
    return "" if v == None else str(v)

def _int(v, default = -1):
    if v == None or v == "":
        return default
    return int(v)

def _valid_hex(value, size):
    s = _str(value)
    if len(s) != size:
        return False
    for ch in s.elems():
        if ch not in HEX:
            return False
    return True

def _valid_sha(value):
    return _valid_hex(value, 40)

def _valid_digest(value):
    s = _str(value)
    return s.startswith("sha256:") and _valid_hex(s[7:], 64)

def _base(ctx):
    return {
        "route": "needs_input",
        "checkpoint": _dict(ctx.inputs.get("prior_checkpoint")),
        "ordered_candidate_ids": [],
        "item_dispositions": _list(ctx.inputs.get("item_dispositions")),
        "integrated_sha": _str(ctx.inputs.get("integrated_sha")),
        "gated_sha": _str(ctx.inputs.get("gated_sha")),
        "staging_sha": _str(ctx.inputs.get("staging_sha")),
        "main_sha": _str(ctx.inputs.get("main_sha")),
        "staging_receipt": _dict(ctx.inputs.get("staging_receipt")),
        "main_receipt": _dict(ctx.inputs.get("main_receipt")),
        "deployment_attestation": _dict(ctx.inputs.get("deployment_attestation")),
        "partial": ctx.inputs.get("partial") == True,
        "last_error": "",
        "resume_phase": _str(ctx.inputs.get("phase")),
        "status": "needs-input",
    }

def _fail(out, message):
    out["route"] = "needs_input"
    out["last_error"] = message
    out["status"] = "needs-input"
    return out

def _candidate_ids(job, out):
    if job.get("schema") != "kitsoki/integration-train-job/v1":
        return None, "job schema must be kitsoki/integration-train-job/v1"
    train_id = _str(job.get("train_id")).strip()
    if train_id == "":
        return None, "job train_id is required"

    manifest = _dict(job.get("manifest"))
    if manifest.get("schema") != "kitsoki/integration-train-manifest/v1":
        return None, "manifest schema must be kitsoki/integration-train-manifest/v1"
    if not _valid_digest(manifest.get("manifest_digest")):
        return None, "manifest_digest must be a sha256 seal"
    if manifest.get("stable_order") != "candidate_id-asc":
        return None, "manifest stable_order must be candidate_id-asc"

    candidates = _list(manifest.get("candidates"))
    max_items = _int(manifest.get("max_items"))
    if max_items < 1 or max_items > 100:
        return None, "manifest max_items must be between 1 and 100"
    if len(candidates) < 1 or len(candidates) > max_items:
        return None, "manifest candidate count must be non-zero and within max_items"

    ids = []
    seen = {}
    seen_provenance = {}
    previous = ""
    required = [
        "candidate_id",
        "report_ref",
        "report_digest",
        "execution_id",
        "shipped_sha",
        "base_sha",
        "bundle_digest",
    ]
    for candidate in candidates:
        row = _dict(candidate)
        cid = _str(row.get("candidate_id")).strip()
        if cid == "":
            return None, "candidate_id is required"
        if seen.get(cid) == True:
            return None, "duplicate candidate_id: " + cid
        if previous != "" and cid < previous:
            return None, "candidate order is not candidate_id-asc at " + cid
        for field in required:
            if _str(row.get(field)).strip() == "":
                return None, "candidate " + cid + " is missing " + field
        if not _valid_digest(row.get("report_digest")):
            return None, "candidate " + cid + " has invalid report_digest"
        if not _valid_digest(row.get("bundle_digest")):
            return None, "candidate " + cid + " has invalid bundle_digest"
        if not _valid_sha(row.get("shipped_sha")) or not _valid_sha(row.get("base_sha")):
            return None, "candidate " + cid + " has invalid shipped/base SHA"
        provenance_key = "|".join([
            _str(row.get("report_ref")),
            _str(row.get("report_digest")),
            _str(row.get("execution_id")),
            _str(row.get("shipped_sha")),
            _str(row.get("base_sha")),
            _str(row.get("bundle_digest")),
        ])
        if seen_provenance.get(provenance_key) == True:
            return None, "duplicate candidate provenance under distinct id: " + cid
        seen[cid] = True
        seen_provenance[provenance_key] = True
        ids.append(cid)
        previous = cid
    out["ordered_candidate_ids"] = ids
    return ids, ""

def _validate_checkpoint(job, phase, evidence, prior):
    cp = _dict(evidence.get("checkpoint"))
    if cp.get("schema") != "kitsoki/integration-train-checkpoint/v1":
        return None, "authority checkpoint schema mismatch"
    if cp.get("train_id") != job.get("train_id"):
        return None, "authority checkpoint train_id mismatch"
    manifest = _dict(job.get("manifest"))
    if cp.get("manifest_digest") != manifest.get("manifest_digest"):
        return None, "authority checkpoint manifest_digest mismatch"
    if cp.get("phase") != phase:
        return None, "authority checkpoint phase mismatch"
    seq = _int(cp.get("sequence"))
    if seq < 1:
        return None, "authority checkpoint sequence must be positive"
    if not _valid_digest(cp.get("checkpoint_digest")):
        return None, "authority checkpoint_digest must be sha256"
    if type(cp.get("evidence")) != "dict":
        return None, "authority checkpoint evidence must be an object"

    if len(prior) == 0:
        if _str(cp.get("previous_digest")) != "":
            return None, "first checkpoint previous_digest must be empty"
        return cp, ""

    prior_seq = _int(prior.get("sequence"))
    if prior_seq < 1:
        return None, "prior checkpoint sequence is invalid"
    if seq < prior_seq:
        return None, "authority checkpoint sequence regressed"
    if seq == prior_seq:
        if cp.get("checkpoint_digest") != prior.get("checkpoint_digest"):
            return None, "same-sequence checkpoint digest changed"
        if cp.get("phase") != prior.get("phase"):
            return None, "same-sequence checkpoint phase changed"
    else:
        if cp.get("previous_digest") != prior.get("checkpoint_digest"):
            return None, "authority checkpoint chain does not extend prior checkpoint"
        prior_phase = _str(prior.get("phase"))
        if PHASES.get(prior_phase, 0) > PHASES.get(phase, 0):
            return None, "authority checkpoint phase regressed"
    return cp, ""

def _queue_receipt(evidence, target, expected_sha, manifest_digest):
    receipt = _dict(evidence.get("queue_receipt"))
    if receipt.get("schema") != "kitsoki/queue-landing/v1":
        return None, target + " queue receipt schema mismatch"
    if receipt.get("target") != target:
        return None, "queue receipt target mismatch: wanted " + target
    if receipt.get("status") != "landed":
        return None, target + " queue receipt is not landed"
    if receipt.get("candidate_sha") != expected_sha:
        return None, target + " queue candidate SHA does not match exact input SHA"
    if receipt.get("manifest_digest") != manifest_digest:
        return None, target + " queue receipt manifest_digest mismatch"
    if not _valid_sha(receipt.get("landed_sha")):
        return None, target + " queue receipt landed_sha is invalid"
    if not _valid_sha(receipt.get("target_base_sha")):
        return None, target + " queue receipt target_base_sha is invalid"
    if not _valid_digest(receipt.get("receipt_digest")):
        return None, target + " queue receipt_digest is invalid"
    if _str(receipt.get("candidate_id")).strip() == "":
        return None, target + " queue receipt candidate_id is required"
    return receipt, ""

def main(ctx):
    out = _base(ctx)
    phase = _str(ctx.inputs.get("phase"))
    job = _dict(ctx.inputs.get("job"))
    evidence = _dict(ctx.inputs.get("evidence"))
    prior = _dict(ctx.inputs.get("prior_checkpoint"))

    ids, err = _candidate_ids(job, out)
    if err != "":
        return _fail(out, err)
    manifest = _dict(job.get("manifest"))
    manifest_digest = manifest.get("manifest_digest")

    if evidence.get("schema") != "kitsoki/integration-train-authority/v1":
        return _fail(out, "authority evidence schema mismatch")
    if evidence.get("phase") != phase:
        return _fail(out, "authority phase mismatch: wanted " + phase)
    if evidence.get("train_id") != job.get("train_id"):
        return _fail(out, "authority train_id mismatch")
    if evidence.get("manifest_digest") != manifest_digest:
        return _fail(out, "authority manifest_digest mismatch")

    checkpoint, err = _validate_checkpoint(job, phase, evidence, prior)
    if err != "":
        return _fail(out, err)
    out["checkpoint"] = checkpoint

    authority_status = _str(evidence.get("status"))
    if authority_status in ["needs_input", "target_moved", "mismatch"]:
        message = _str(evidence.get("error")).strip()
        if message == "":
            message = phase + " authority returned " + authority_status
        return _fail(out, message)

    if phase == "validate":
        if authority_status not in ["ready", "complete"]:
            return _fail(out, "validate authority did not accept the sealed manifest")
        if _list(evidence.get("ordered_candidate_ids")) != ids:
            return _fail(out, "authority candidate order does not match sealed stable order")
        out["route"] = "next"
        out["resume_phase"] = "integrate"
        return out

    if phase == "integrate":
        dispositions = _list(evidence.get("item_dispositions"))
        if len(dispositions) != len(ids):
            return _fail(out, "integrate authority must return one disposition per candidate")
        accepted = 0
        excluded = 0
        seen = {}
        for index in range(len(dispositions)):
            row = _dict(dispositions[index])
            cid = _str(row.get("candidate_id"))
            status = _str(row.get("status"))
            if cid != ids[index] or seen.get(cid) == True:
                return _fail(out, "integrate dispositions must preserve sealed candidate order")
            if status not in ["accepted", "conflict", "invalid", "needs_input"]:
                return _fail(out, "invalid disposition for " + cid + ": " + status)
            if status == "needs_input":
                return _fail(out, "candidate " + cid + " requires input: " + _str(row.get("reason")))
            if status == "accepted":
                accepted += 1
            else:
                excluded += 1
            seen[cid] = True
        if accepted == 0:
            return _fail(out, "integration train accepted no candidates")
        sha = _str(evidence.get("integrated_sha"))
        if not _valid_sha(sha):
            return _fail(out, "integrate authority did not return an exact integrated SHA")
        if authority_status not in ["complete", "partial"]:
            return _fail(out, "integrate authority status must be complete or partial")
        if excluded > 0 and authority_status != "partial":
            return _fail(out, "excluded candidates require partial authority status")
        out["item_dispositions"] = dispositions
        out["integrated_sha"] = sha
        out["partial"] = excluded > 0
        out["route"] = "next"
        out["resume_phase"] = "gate"
        return out

    if phase == "gate":
        integrated_sha = _str(ctx.inputs.get("integrated_sha"))
        if not _valid_sha(integrated_sha):
            return _fail(out, "gate phase has no valid integrated SHA")
        receipt = _dict(evidence.get("gate_receipt"))
        gated_sha = _str(evidence.get("gated_sha"))
        if receipt.get("schema") != "kitsoki/integration-train-gate-receipt/v1":
            return _fail(out, "aggregate gate receipt schema mismatch")
        if receipt.get("manifest_digest") != manifest_digest:
            return _fail(out, "aggregate gate receipt manifest_digest mismatch")
        if receipt.get("validated_sha") != gated_sha or not _valid_sha(gated_sha):
            return _fail(out, "aggregate gate did not bind an exact validated SHA")
        if not _valid_digest(receipt.get("receipt_digest")):
            return _fail(out, "aggregate gate receipt_digest is invalid")
        aggregate = _str(receipt.get("aggregate_status"))
        if aggregate == "green":
            if gated_sha != integrated_sha:
                return _fail(out, "green aggregate gate SHA differs from integrated SHA")
        elif aggregate == "red":
            if receipt.get("bisect_status") != "complete":
                return _fail(out, "red aggregate gate has no completed bisect")
            isolated = _list(receipt.get("isolated_candidate_ids"))
            if len(isolated) == 0:
                return _fail(out, "red aggregate gate bisect isolated no candidates")
            dispositions = _list(evidence.get("item_dispositions"))
            if len(dispositions) != len(ids):
                return _fail(out, "completed bisect must return one updated disposition per candidate")
            isolated_seen = {}
            accepted = 0
            for index in range(len(dispositions)):
                row = _dict(dispositions[index])
                cid = _str(row.get("candidate_id"))
                status = _str(row.get("status"))
                if cid != ids[index]:
                    return _fail(out, "bisect dispositions must preserve sealed candidate order")
                if status not in ["accepted", "conflict", "invalid"]:
                    return _fail(out, "completed bisect has unresolved disposition for " + cid)
                if status == "accepted":
                    accepted += 1
                elif cid in isolated:
                    isolated_seen[cid] = True
            if accepted == 0:
                return _fail(out, "completed bisect accepted no candidates")
            for cid in isolated:
                if isolated_seen.get(cid) != True:
                    return _fail(out, "isolated candidate lacks excluded disposition: " + cid)
            out["item_dispositions"] = dispositions
            out["partial"] = True
        else:
            return _fail(out, "aggregate gate status must be green or red")
        if authority_status not in ["green", "partial"]:
            return _fail(out, "gate authority status must be green or partial")
        out["gated_sha"] = gated_sha
        out["route"] = "next"
        out["resume_phase"] = "staging"
        return out

    if phase == "staging":
        gated_sha = _str(ctx.inputs.get("gated_sha"))
        receipt, err = _queue_receipt(evidence, "staging", gated_sha, manifest_digest)
        if err != "":
            return _fail(out, err)
        if authority_status != "landed":
            return _fail(out, "staging authority is not landed")
        out["staging_receipt"] = receipt
        out["staging_sha"] = receipt.get("landed_sha")
        out["route"] = "next"
        out["resume_phase"] = "main"
        return out

    if phase == "main":
        staging_sha = _str(ctx.inputs.get("staging_sha"))
        receipt, err = _queue_receipt(evidence, "main", staging_sha, manifest_digest)
        if err != "":
            return _fail(out, err)
        if authority_status != "landed":
            return _fail(out, "main authority is not landed")
        staging_receipt = _dict(ctx.inputs.get("staging_receipt"))
        if receipt.get("receipt_digest") == staging_receipt.get("receipt_digest"):
            return _fail(out, "main and staging must have distinct queue receipts")
        out["main_receipt"] = receipt
        out["main_sha"] = receipt.get("landed_sha")
        out["route"] = "next"
        out["resume_phase"] = "deploy"
        return out

    if phase == "deploy":
        if authority_status != "attested":
            return _fail(out, "deploy authority is not attested")
        att = _dict(evidence.get("deployment_attestation"))
        main_sha = _str(ctx.inputs.get("main_sha"))
        if att.get("schema") != "kitsoki/integration-train-deployment-attestation/v1":
            return _fail(out, "deployment attestation schema mismatch")
        if att.get("main_sha") != main_sha or not _valid_sha(main_sha):
            return _fail(out, "deployment attestation main SHA mismatch")
        if not _valid_sha(att.get("main_tree")):
            return _fail(out, "deployment attestation main tree is invalid")
        if not _valid_digest(att.get("release_digest")) or not _valid_digest(att.get("image_digest")):
            return _fail(out, "deployment attestation release/image digest is invalid")
        if _str(att.get("environment")).strip() == "" or _str(att.get("verifier")).strip() == "":
            return _fail(out, "deployment attestation environment and verifier are required")
        if att.get("health") != "healthy":
            return _fail(out, "deployment health is not healthy")
        if _str(att.get("verified_at")).strip() == "":
            return _fail(out, "deployment verified_at is required")
        out["deployment_attestation"] = att
        out["route"] = "partial" if out["partial"] else "released"
        out["resume_phase"] = ""
        out["status"] = "partial" if out["partial"] else "released"
        return out

    return _fail(out, "unknown integration-train phase: " + phase)
