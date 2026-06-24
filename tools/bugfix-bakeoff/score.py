#!/usr/bin/env python3
"""Score ONE bake-off grid cell into results/cells/<bug>-<candidate>-<treatment>.json.

The deterministic, offline grader for the bugfix bake-off. It NEVER calls an LLM
or a provider — every signal is read off the produced worktree (the tree the cell
candidate left behind) and the run's recorded transcript:

  OUTCOME    — run the bug's hidden `oracle_test` (the regression test the REAL fix
               added) against the candidate's worktree. The oracle is NOT in the
               candidate tree, so we copy it in from the fix commit, run it, then
               remove it so it never pollutes the tree. Also `go build ./...` (go
               bugs) and the bug's affected_test_pkgs for a no-regression suite gate.
               Mapped to outcome.quality per results/SCHEMA.md.
  COMPLIANCE — five best-effort heuristics over `git status/diff` + the transcript:
               reproduced_red, added_regression_test, suite_green, in_scope,
               stage_order. rate = mean of the five.
  METRICS    — exact tokens + cost from the transcript via the session-mining
               cost_extract module (reused, not reimplemented). Robust to a
               missing/empty transcript (everything zeroed, noted).

The oracle runner and the cost extractor are dependency-injected (see
`score_cell`) so score_test.py can stub them with zero process/network cost.

Usage:
  score.py --manifest tools/bugfix-bakeoff/bakeoff.yaml \
           --bug bug1 --candidate opus-4.8 --treatment kitsoki \
           --worktree .worktrees/bakeoff-bug1-opus-4.8-kitsoki \
           --transcript .artifacts/bugfix-bakeoff/bug1/opus-4.8-kitsoki.jsonl \
           --wall-time-s 412.0 --guidance-turns 2 \
           --out tools/bugfix-bakeoff/results/cells/bug1-opus-4.8-kitsoki.json
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys

import yaml

# Reuse the session-mining cost/pricing logic — do NOT duplicate pricing here.
_MINING = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                       "..", "session-mining")
sys.path.insert(0, os.path.abspath(_MINING))
import cost_extract  # noqa: E402
import pricing  # noqa: E402  (shared price table — no duplication)

# Top-level dirs a fix for a given component is plausibly allowed to touch. Used
# by the in_scope heuristic; anything outside this (per component) is "unrelated".
SCOPE_DIRS = {
    "tui": {"internal", "tools"},
    "runtime": {"internal"},
    "web": {"internal", "tools"},
}


# --------------------------------------------------------------------------- #
# OUTCOME — the oracle/build/suite runner (injectable).
# --------------------------------------------------------------------------- #

def _run(cmd, cwd, timeout=600):
    """Run a command, returning (returncode, combined_output). rc=-1 on launch
    failure or timeout."""
    try:
        proc = subprocess.run(cmd, cwd=cwd, timeout=timeout,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                              text=True)
        return proc.returncode, proc.stdout
    except (subprocess.TimeoutExpired, OSError) as exc:
        return -1, f"{type(exc).__name__}: {exc}"


def _go_test_func(oracle_path):
    """The first `func TestXxx(` name declared in a Go test file, or None."""
    try:
        with open(oracle_path) as fh:
            src = fh.read()
    except OSError:
        return None
    m = re.search(r"func\s+(Test\w+)\s*\(", src)
    return m.group(1) if m else None


def _go_pkg(oracle_test):
    """`./dir/...`-free package path for a go oracle file (its directory)."""
    return "./" + os.path.dirname(oracle_test)


class OracleRunner:
    """Runs the oracle test + build + suite against a worktree using the real
    toolchain. Stubbed in tests; all process work is confined here (DI seam).

    `run_oracle` copies the hidden oracle test in from the fix commit, runs it,
    and removes it so the candidate tree is never polluted (the contract in
    SCHEMA.md). Returns one of: pass / fail / noncompile / absent.
    """

    def __init__(self, runner=_run):
        self._run = runner

    def run_oracle(self, bug, worktree):
        kind = bug.get("oracle_kind")
        oracle = bug["oracle_test"]
        fix_sha = bug["fix_sha"]
        if kind == "go":
            return self._oracle_go(oracle, fix_sha, worktree)
        if kind == "vitest":
            return self._oracle_vitest(oracle, fix_sha, worktree)
        return "absent", f"unknown oracle_kind {kind!r}"

    def _copy_oracle(self, oracle, fix_sha, worktree):
        """git show <fix_sha>:<oracle> > <worktree>/<oracle>. Returns dest path
        or None if the blob can't be read."""
        rc, out = self._run(["git", "show", f"{fix_sha}:{oracle}"], worktree)
        if rc != 0:
            return None
        dest = os.path.join(worktree, oracle)
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        with open(dest, "w") as fh:
            fh.write(out)
        return dest

    def _oracle_go(self, oracle, fix_sha, worktree):
        func = None
        dest = self._copy_oracle(oracle, fix_sha, worktree)
        if dest is None:
            return "absent", "oracle blob not found at fix commit"
        try:
            func = _go_test_func(dest)
            if not func:
                return "absent", "no Test func parsed from oracle"
            rc, out = self._run(
                ["go", "test", "-run", f"^{func}$", _go_pkg(oracle)], worktree)
        finally:
            try:
                os.remove(dest)
            except OSError:
                pass
        return _classify_go(rc, out), f"oracle {func}: rc={rc}"

    def _oracle_vitest(self, oracle, fix_sha, worktree):
        dest = self._copy_oracle(oracle, fix_sha, worktree)
        if dest is None:
            return "absent", "oracle blob not found at fix commit"
        try:
            # runstatus vitest: `pnpm --filter kitsoki-runstatus test -- <file>`.
            rc, out = self._run(
                ["pnpm", "--filter", "kitsoki-runstatus", "test", "--", oracle],
                worktree)
        finally:
            try:
                os.remove(dest)
            except OSError:
                pass
        return _classify_vitest(rc, out), f"oracle vitest: rc={rc}"

    def run_build(self, worktree):
        """`go build ./...`; returns True/False, or None (n/a)."""
        rc, _ = self._run(["go", "build", "./..."], worktree)
        return rc == 0

    def run_suite(self, pkgs, worktree, kind):
        """Affected-package suite. True iff all packages green."""
        if kind == "go":
            rc, _ = self._run(["go", "test"] + list(pkgs), worktree)
            return rc == 0
        if kind == "vitest":
            rc, _ = self._run(
                ["pnpm", "--filter", "kitsoki-runstatus", "test"], worktree)
            return rc == 0
        return None


def _classify_go(rc, out):
    """pass / fail / noncompile from a `go test` run."""
    if rc == 0:
        return "pass"
    low = (out or "").lower()
    if "build failed" in low or "cannot find" in low or "undefined:" in low \
            or "does not compile" in low or "syntax error" in low:
        return "noncompile"
    return "fail"


def _classify_vitest(rc, out):
    if rc == 0:
        return "pass"
    low = (out or "").lower()
    if "transform failed" in low or "cannot find module" in low \
            or "is not defined" in low or "syntaxerror" in low:
        return "noncompile"
    return "fail"


# --------------------------------------------------------------------------- #
# COMPLIANCE — heuristics over git + transcript.
# --------------------------------------------------------------------------- #

def changed_files(worktree, baseline_sha, runner=_run):
    """The candidate's change set vs the bug's hermetic baseline, as a set of
    (added, path) tuples where `added` is True for an ADDED path.

    Candidates often COMMIT their work (e.g. `claude -p` commits the fix + its
    own regression test), leaving `git status` CLEAN — so a status-only scan
    misses everything. We take the UNION of:
      - committed:   git diff --name-status <baseline_sha>..HEAD
      - uncommitted: git status --porcelain
    An empty/invalid/missing baseline_sha (e.g. the test fixture's commit-less
    worktree, where baseline..HEAD is empty or errors) gracefully degrades to
    the uncommitted set alone. `runner` is injectable for tests."""
    files = {}
    # committed changes relative to the hermetic baseline.
    if baseline_sha:
        rc, out = runner(
            ["git", "-C", worktree, "diff", "--name-status",
             f"{baseline_sha}..HEAD"], None)
        if rc == 0:
            for line in out.splitlines():
                parts = line.split("\t")
                if len(parts) < 2:
                    continue
                code, path = parts[0], parts[-1]
                files[path] = files.get(path, False) or code.startswith("A")
    # uncommitted (working tree + index) changes.
    rc, out = runner(["git", "-C", worktree, "status", "--porcelain"], None)
    if rc == 0:
        for line in out.splitlines():
            if not line.strip():
                continue
            flag, path = line[:2], line[3:].strip()
            if "->" in path:  # rename
                path = path.split("->")[-1].strip()
            if not path:
                continue
            added = "A" in flag or "?" in flag
            files[path] = files.get(path, False) or added
    return {(added, path) for path, added in files.items()}


def _added_regression_test(change_set, kind):
    """added_regression_test heuristic: did the candidate ADD a NEW *_test.go
    (go) or *.test.ts (vitest) file, committed OR uncommitted? Best-effort — a
    renamed or amended-in-place test can slip by."""
    suffix = "_test.go" if kind == "go" else ".test.ts"
    return any(added and path.endswith(suffix) for added, path in change_set)


def _changed_top_dirs(change_set):
    """Top-level directories the candidate touched (committed + uncommitted)."""
    return {path.split("/")[0] for _added, path in change_set if path}


def _block_text(content):
    """All human-readable text in a message content (str or block list):
    user/assistant `text`, tool_use command/file args, and tool_result output
    (where a failing-test signal lands)."""
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""
    out = []
    for b in content:
        if not isinstance(b, dict):
            continue
        t = b.get("type")
        if t in ("text",):
            out.append(b.get("text", ""))
        elif t == "tool_use":
            inp = b.get("input", {}) or {}
            out.append(str(inp.get("command") or inp.get("file_path") or ""))
        elif t == "tool_result":
            c = b.get("content")
            out.append(c if isinstance(c, str) else _block_text(c))
    return " ".join(out)


def _transcript_text(transcript):
    """Lowercased concatenation of EVERY message's readable text (assistant
    reasoning, tool commands, and tool_result output), for the reproduce/
    stage-order scans. Reads the raw JSONL directly because the cost extractor
    intentionally drops assistant text + tool results. '' if unreadable."""
    if not transcript or not os.path.exists(transcript):
        return ""
    parts = []
    try:
        with open(transcript) as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    continue
                msg = rec.get("message") or {}
                parts.append(_block_text(msg.get("content")))
    except OSError:
        return ""
    return " ".join(parts).lower()


def _scan_reproduced_red(text):
    """reproduced_red heuristic: transcript shows the bug demonstrated RED before
    the fix — a test run that failed, or explicit reproduce language."""
    if not text:
        return False
    cues = ["--- fail", "fail:", "reproduc", "red before", "failing test",
            "confirm the bug", "demonstrate the bug"]
    return any(c in text for c in cues)


def _scan_stage_order(text):
    """stage_order heuristic: a failing/repro test appears in the transcript
    BEFORE language about implementing the fix (reproduce -> implement -> test)."""
    if not text:
        return False
    repro = -1
    for c in ("reproduc", "failing test", "--- fail", "red"):
        i = text.find(c)
        if i != -1:
            repro = i if repro == -1 else min(repro, i)
    impl = -1
    for c in ("implement the fix", "apply the fix", "now fix", "fix the bug",
              "the fix is"):
        i = text.find(c)
        if i != -1:
            impl = i if impl == -1 else min(impl, i)
    return repro != -1 and impl != -1 and repro < impl


def compute_compliance(bug, worktree, transcript, suite_pass, *,
                       change_set=None):
    """Derive the five compliance booleans + their mean. Each is documented on
    its scanner above; all are best-effort and offline.

    The changed-file heuristics (added_regression_test, in_scope) run over the
    UNION of committed (vs the bug's baseline_sha) and uncommitted changes, so a
    candidate that committed its work isn't scored as having changed nothing.
    `change_set` is injectable for tests; otherwise derived from the worktree."""
    kind = bug.get("oracle_kind")
    text = _transcript_text(transcript)
    component = bug.get("component", "")
    if change_set is None:
        change_set = changed_files(worktree, bug.get("baseline_sha", ""))
    changed = _changed_top_dirs(change_set)
    allowed = SCOPE_DIRS.get(component, {"internal", "tools"})
    in_scope = changed.issubset(allowed) if changed else True

    bools = {
        "reproduced_red": _scan_reproduced_red(text),
        "added_regression_test": _added_regression_test(change_set, kind),
        "suite_green": bool(suite_pass),
        "in_scope": in_scope,
        "stage_order": _scan_stage_order(text),
    }
    rate = sum(1 for v in bools.values() if v) / len(bools)
    out = dict(bools)
    out["rate"] = round(rate, 4)
    return out


# --------------------------------------------------------------------------- #
# METRICS — tokens + cost via the reused pricing, format-agnostic.
# --------------------------------------------------------------------------- #
#
# Two transcript formats land in the bake-off:
#   * Claude Code      — JSONL, each assistant line carries `message.usage`.
#                        Parsed by the session-mining cost_extract.extract.
#   * kitsoki trace    — JSONL of store.Events, top-level {turn,seq,ts,kind,
#                        payload,...}; agent-call-completion events carry
#                        payload.meta.usage. This is the format for the
#                        kitsoki-pipeline cells + the MCP-driven single cells
#                        (GLM / gpt-5.5), i.e. most of the grid. cost_extract
#                        returns ALL ZEROS on these, so we extract them here.
# default_cost_fn sniffs the format and dispatches.

_ZERO_METRICS = dict(input_tokens=0, output_tokens=0, cache_read_tokens=0,
                     cache_write_tokens=0, cost_usd=0.0, cost_exact=True,
                     note="no transcript")

# Kitsoki agent models are recorded by their short alias; map to the pricing.py
# prefix so opus/sonnet/haiku price exactly. Unmapped ids (hf:..., gpt-5.5) fall
# through to pricing.price_for's fallback, which flags cost_exact=False.
_MODEL_ALIAS = {
    "opus": "claude-opus-4",
    "sonnet": "claude-sonnet-4",
    "haiku": "claude-haiku-4",
}


def _first_parseable(transcript):
    """The first JSON-decodable line of a transcript, or None."""
    with open(transcript) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                return json.loads(line)
            except json.JSONDecodeError:
                continue
    return None


def sniff_format(transcript):
    """Return 'kitsoki' | 'claude_code' | 'unknown' by inspecting the first
    parseable line:
      - a top-level "kind" field          => kitsoki store.Event trace
      - a "message" key (with usage)       => Claude Code transcript
    """
    rec = _first_parseable(transcript)
    if not isinstance(rec, dict):
        return "unknown"
    if "kind" in rec:
        return "kitsoki"
    if "message" in rec:
        return "claude_code"
    return "unknown"


def _kitsoki_usage_events(transcript):
    """Yield (model, usage_dict) for every kitsoki event carrying agent usage.
    Usage lives at payload.meta.usage (preferred) or payload.usage; the model at
    payload.model."""
    with open(transcript) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            payload = rec.get("payload")
            if not isinstance(payload, dict):
                continue
            meta = payload.get("meta") or {}
            usage = meta.get("usage") if isinstance(meta, dict) else None
            if usage is None:
                usage = payload.get("usage")
            if isinstance(usage, dict) and usage:
                yield payload.get("model", ""), usage


def extract_kitsoki(transcript):
    """Sum usage across a kitsoki trace's agent-call events, priced per model via
    the shared pricing table. Returns the metrics shape score.py consumes;
    cache_read_input_tokens->cache_read, cache_creation_input_tokens->cache_write.
    is_exact is False if ANY priced model used the fallback rate (GLM/gpt-5.5)."""
    tot = dict(input_tokens=0, output_tokens=0, cache_read_tokens=0,
               cache_write_tokens=0, cost_usd=0.0, cost_exact=True, note="")
    saw = False
    for model, usage in _kitsoki_usage_events(transcript):
        saw = True
        tot["input_tokens"] += usage.get("input_tokens", 0)
        tot["output_tokens"] += usage.get("output_tokens", 0)
        tot["cache_read_tokens"] += usage.get("cache_read_input_tokens", 0)
        tot["cache_write_tokens"] += usage.get("cache_creation_input_tokens", 0)
        priced_model = _MODEL_ALIAS.get(model, model)
        usd, exact = pricing.message_cost(usage, priced_model)
        tot["cost_usd"] += usd
        tot["cost_exact"] = tot["cost_exact"] and exact
    if not saw:
        z = dict(_ZERO_METRICS)
        z["note"] = "kitsoki trace carried no agent usage events"
        return z
    tot["cost_usd"] = round(tot["cost_usd"], 6)
    return tot


def extract_claude_code(transcript):
    """Sum a Claude Code transcript via the session-mining extractor."""
    sc = cost_extract.extract(transcript)
    if not sc.turns:
        z = dict(_ZERO_METRICS)
        z["note"] = "transcript empty"
        return z
    return dict(
        input_tokens=sum(t.input_tokens for t in sc.turns),
        output_tokens=sum(t.output_tokens for t in sc.turns),
        cache_read_tokens=sum(t.cache_read for t in sc.turns),
        cache_write_tokens=sum(t.cache_write for t in sc.turns),
        cost_usd=round(sc.total(), 6),
        cost_exact=sc.exact(),
        note="",
    )


def default_cost_fn(transcript):
    """Format-agnostic cost extraction: sniff Claude Code vs kitsoki-trace and
    dispatch. Returns the metrics shape (zeroed + noted when absent/empty/
    unreadable). Injectable as `cost_fn` on score_cell for tests."""
    if not transcript or not os.path.exists(transcript):
        return dict(_ZERO_METRICS)
    try:
        fmt = sniff_format(transcript)
        if fmt == "kitsoki":
            return extract_kitsoki(transcript)
        # Anything without a top-level `kind` is a Claude Code transcript: the
        # usage-bearing `message` lines are not necessarily the FIRST parseable
        # line (newer transcripts open with type/operation wrapper records), so
        # claude_code is the correct default and cost_extract finds the usage.
        return extract_claude_code(transcript)
    except Exception as exc:  # noqa: BLE001
        z = dict(_ZERO_METRICS)
        z["note"] = f"transcript unreadable: {exc}"
        return z


# --------------------------------------------------------------------------- #
# quality mapping + assembly.
# --------------------------------------------------------------------------- #

def map_quality(oracle_status, build_pass, suite_pass):
    """Per SCHEMA.md:
      solved  = oracle_pass && build_pass!=false && suite_pass
      partial = oracle_pass but a regression/build issue, OR oracle noncompile
      failed  = otherwise (oracle fail/absent)."""
    oracle_pass = oracle_status == "pass"
    if oracle_pass and build_pass is not False and suite_pass:
        return "solved"
    if oracle_pass:
        return "partial"
    if oracle_status == "noncompile":
        return "partial"
    return "failed"


def find_candidate(manifest, key):
    for c in manifest.get("candidates", []):
        if c.get("key") == key:
            return c
    raise KeyError(f"candidate {key!r} not in manifest")


def find_bug(manifest, bug_id):
    for b in manifest.get("bugs", []):
        if b.get("id") == bug_id:
            return b
    raise KeyError(f"bug {bug_id!r} not in manifest")


def score_cell(manifest, bug_id, candidate_key, treatment, worktree,
               transcript, wall_time_s, guidance_turns, *,
               oracle_runner=None, cost_fn=None, trace_found=None,
               change_set=None, adjudication=None, adjudication_note=""):
    """Score one cell into the SCHEMA cell dict. `oracle_runner`, `cost_fn` and
    `change_set` are injectable so tests can run with no toolchain/transcript/git
    at all. When `change_set` is None it's derived from the worktree as the union
    of committed (vs baseline_sha) + uncommitted changes."""
    oracle_runner = oracle_runner or OracleRunner()
    cost_fn = cost_fn or default_cost_fn
    bug = find_bug(manifest, bug_id)
    cand = find_candidate(manifest, candidate_key)
    kind = bug.get("oracle_kind")
    notes = []

    oracle_status, onote = oracle_runner.run_oracle(bug, worktree)
    if onote:
        notes.append(onote)
    build_pass = oracle_runner.run_build(worktree) if kind == "go" else None
    suite_pass = oracle_runner.run_suite(
        bug.get("affected_test_pkgs", []), worktree, kind)
    quality = map_quality(oracle_status, build_pass, suite_pass)
    if oracle_status == "noncompile":
        notes.append("oracle noncompiles against candidate's differing "
                     "implementation; bug plausibly fixed (partial)")

    # Human/LLM adjudication override: some oracles are wording/implementation-
    # coupled and false-fail a behaviorally-correct fix. The adjudicated value
    # replaces quality; oracle_status keeps the RAW automated result (no lying).
    adjudicated = adjudication is not None
    if adjudicated:
        if adjudication not in ("solved", "partial", "failed"):
            raise ValueError(f"adjudication must be solved|partial|failed, "
                             f"got {adjudication!r}")
        quality = adjudication

    compliance = compute_compliance(bug, worktree, transcript, suite_pass,
                                    change_set=change_set)
    metrics = cost_fn(transcript)
    if metrics.get("note"):
        notes.append(metrics["note"])

    total_tokens = (metrics["input_tokens"] + metrics["output_tokens"]
                    + metrics["cache_read_tokens"] + metrics["cache_write_tokens"])

    cell = {
        "bug": bug_id,
        "candidate": candidate_key,
        "treatment": treatment,
        "profile": cand.get("profile"),
        "model": cand.get("model"),
        "effort": cand.get("effort"),
        "provider": cand.get("provider"),
        "outcome": {
            "oracle_pass": oracle_status == "pass",
            "oracle_status": oracle_status,
            "build_pass": build_pass,
            "suite_pass": bool(suite_pass) if suite_pass is not None else None,
            "quality": quality,
            "adjudicated": adjudicated,
            "adjudication_note": adjudication_note if adjudicated else "",
        },
        "compliance": compliance,
        "metrics": {
            "input_tokens": metrics["input_tokens"],
            "output_tokens": metrics["output_tokens"],
            "cache_read_tokens": metrics["cache_read_tokens"],
            "cache_write_tokens": metrics["cache_write_tokens"],
            "total_tokens": total_tokens,
            "cost_usd": metrics["cost_usd"],
            "cost_exact": metrics["cost_exact"],
            "wall_time_s": float(wall_time_s),
            "guidance_turns": int(guidance_turns),
        },
        "transcript_path": transcript or "",
        "trace_found": bool(trace_found) if trace_found is not None
        else (treatment == "kitsoki" and bool(transcript)
              and os.path.exists(transcript or "")),
        "notes": "; ".join(n for n in notes if n),
    }
    return cell


def load_manifest(path):
    with open(path) as fh:
        return yaml.safe_load(fh)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--manifest", default=os.path.join(
        os.path.dirname(os.path.abspath(__file__)), "bakeoff.yaml"))
    ap.add_argument("--bug", required=True)
    ap.add_argument("--candidate", required=True)
    ap.add_argument("--treatment", required=True, choices=["kitsoki", "single"])
    ap.add_argument("--worktree", required=True)
    ap.add_argument("--transcript", default="")
    ap.add_argument("--wall-time-s", type=float, default=0.0)
    ap.add_argument("--guidance-turns", type=int, default=0)
    ap.add_argument("--adjudication", choices=["solved", "partial", "failed"],
                    default=None,
                    help="human/LLM override of outcome.quality when the oracle "
                         "is wording/impl-coupled and false-fails a correct fix "
                         "(oracle_status keeps the raw automated result)")
    ap.add_argument("--adjudication-note", default="",
                    help="rationale recorded on outcome.adjudication_note")
    ap.add_argument("--out", required=True)
    args = ap.parse_args(argv)

    manifest = load_manifest(args.manifest)
    cell = score_cell(
        manifest, args.bug, args.candidate, args.treatment, args.worktree,
        args.transcript, args.wall_time_s, args.guidance_turns,
        adjudication=args.adjudication,
        adjudication_note=args.adjudication_note)

    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w") as fh:
        json.dump(cell, fh, indent=2)
        fh.write("\n")
    print(f"wrote {args.out}  quality={cell['outcome']['quality']} "
          f"oracle={cell['outcome']['oracle_status']} "
          f"compliance={cell['compliance']['rate']} "
          f"cost={cell['metrics']['cost_usd']}")


if __name__ == "__main__":
    main()
