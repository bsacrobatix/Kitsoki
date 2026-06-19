#!/usr/bin/env python3
"""Tests for the REAL-cost path: pricing.py + cost_extract.py. NO LLM, no network.

Unlike cost_estimate (a model), this reads recorded `message.usage` and the
arithmetic is exact, so we pin exact dollars on hand-built usage blocks, plus the
turn-attribution and fallback-flagging behaviour on a tiny synthetic transcript.

Run:  python3 tools/session-mining/tests/test_cost_extract.py
"""
import json
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
TOOL = os.path.dirname(HERE)
sys.path.insert(0, TOOL)

import pricing
import cost_extract as ce


def run():
    failures = []

    def check(cond, msg):
        if not cond:
            failures.append(msg)

    def approx(a, b, msg, tol=1e-9):
        check(abs(a - b) < tol, "%s: %r vs %r" % (msg, a, b))

    # --- pricing: exact dot product of recorded buckets with rates ----------
    sonnet = pricing.PRICING["claude-sonnet-4"]
    usage = {"input_tokens": 1000, "output_tokens": 100,
             "cache_read_input_tokens": 10000,
             "cache_creation_input_tokens": 2000,
             "cache_creation": {"ephemeral_5m_input_tokens": 2000,
                                "ephemeral_1h_input_tokens": 0}}
    usd, exact = pricing.message_cost(usage, "claude-sonnet-4-6")
    want = (1000 * sonnet.input + 100 * sonnet.output
            + 10000 * sonnet.cache_read + 2000 * sonnet.cache_write_5m) / 1e6
    approx(usd, want, "sonnet message cost")
    check(exact, "sonnet model must be exact-priced")

    # opus is pricier than sonnet for identical usage (the real-corpus default)
    opus_usd, _ = pricing.message_cost(usage, "claude-opus-4-8")
    check(opus_usd > usd, "opus must cost more than sonnet for same usage")

    # 1h cache write is billed higher than 5m
    u5 = {"cache_creation_input_tokens": 1000,
          "cache_creation": {"ephemeral_5m_input_tokens": 1000, "ephemeral_1h_input_tokens": 0}}
    u1 = {"cache_creation_input_tokens": 1000,
          "cache_creation": {"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 1000}}
    c5, _ = pricing.message_cost(u5, "claude-sonnet-4-6")
    c1, _ = pricing.message_cost(u1, "claude-sonnet-4-6")
    check(c1 > c5, "1h cache write must cost more than 5m")

    # missing split -> whole cache write treated as 5m (no crash, sane value)
    uns = {"cache_creation_input_tokens": 1000}
    cns, _ = pricing.message_cost(uns, "claude-sonnet-4-6")
    approx(cns, c5, "unsplit cache write defaults to 5m rate")

    # unknown model -> fallback tier, flagged inexact
    _, ex = pricing.message_cost(usage, "claude-fable-5")
    check(not ex, "unknown model must be flagged inexact (fallback-priced)")
    _, ex2 = pricing.message_cost(usage, "")
    check(not ex2, "empty model must be flagged inexact")

    # --- cost_extract: turn attribution from a tiny synthetic transcript ----
    # two user turns; second runs a `git commit`. assistant usage is exact-priced.
    def amsg(out_tok, cmd=None):
        content = [{"type": "text", "text": "ok"}]
        if cmd:
            content.append({"type": "tool_use", "name": "Bash", "input": {"command": cmd}})
        return {"type": "assistant",
                "message": {"model": "claude-sonnet-4-6", "content": content,
                            "usage": {"input_tokens": 100, "output_tokens": out_tok,
                                      "cache_read_input_tokens": 0,
                                      "cache_creation_input_tokens": 0}}}

    lines = [
        {"type": "user", "message": {"content": "look around"}},
        amsg(10),
        {"type": "user", "isMeta": True, "message": {"content": "<system-reminder>x</system-reminder>"}},
        {"type": "user", "message": {"content": [{"type": "tool_result", "content": "done"}]}},
        amsg(20),
        {"type": "user", "message": {"content": "commit this"}},
        amsg(30, cmd="git commit -m wip"),
    ]
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "s.jsonl")
        with open(p, "w") as fh:
            for r in lines:
                fh.write(json.dumps(r) + "\n")
        sc = ce.extract(p)

    # two real user turns survive (the isMeta + tool_result turns do NOT start turns)
    check(len(sc.turns) == 2, "expected 2 attributed turns, got %d" % len(sc.turns))
    t0, t1 = sc.turns
    check(t0.user_text == "look around", "turn0 text: %r" % t0.user_text)
    check(t0.calls == 2, "turn0 should absorb both pre-commit calls, got %d" % t0.calls)
    check(t1.user_text == "commit this", "turn1 text: %r" % t1.user_text)
    check(t1.calls == 1, "turn1 one call, got %d" % t1.calls)
    check(any("git commit" in c for c in t1.commands),
          "turn1 must record the git commit command: %r" % t1.commands)

    # session cost == sum of message costs (exact arithmetic)
    per_msg = sum(pricing.message_cost(
        {"input_tokens": 100, "output_tokens": o, "cache_read_input_tokens": 0,
         "cache_creation_input_tokens": 0}, "claude-sonnet-4-6")[0]
        for o in (10, 20, 30))
    approx(sc.total(), per_msg, "session total == sum of message costs")
    check(sc.exact(), "all-sonnet session must be exact")

    if failures:
        print("FAIL (%d):" % len(failures))
        for f in failures:
            print("  -", f)
        return 1
    print("PASS: pricing + cost_extract (no LLM): exact arithmetic, turn "
          "attribution, fallback flagging")
    return 0


if __name__ == "__main__":
    sys.exit(run())
