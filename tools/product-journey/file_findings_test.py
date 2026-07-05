#!/usr/bin/env python3
"""Runner-level test for --file-findings: wiring, idempotence, and gates.

Run directly:  python3 tools/product-journey/file_findings_test.py

Body assembly and the real GitHub orchestration live in Go
(host.GitHubFileFindings, unit-tested in internal/host/github_findings_test.go
with a stubbed gh runner). This test covers the runner side with a fake
KITSOKI_BIN so nothing calls go, gh, GitHub, or an LLM:

  1. dry-run leaves the bundle untouched and reports candidates,
  2. filing records issue URLs + the filing block and refreshes derived
     artifacts,
  3. a re-run skips already-filed findings (idempotent),
  4. once filing was requested, review gains native filing and gh-agent gates
     and validate errors on credible-but-unfiled findings or missing fix
     evidence.
"""

import importlib.util
import json
import os
import stat
import sys
import tempfile
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "pj_run", str(Path(__file__).with_name("run.py"))
)
run = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(run)

FAKE_KITSOKI = r'''#!/usr/bin/env python3
"""Fake `kitsoki bug file-findings` / `gh-agent enqueue` used by file_findings_test.py.

Mirrors the Go orchestration's bundle contract: file each credible unfiled
issue finding (record github_issue), stamp findings.filing, print the JSON
result. --dry-run touches nothing.
"""
import argparse, json, os, sys
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("verb1")
parser.add_argument("verb2")
parser.add_argument("--run-dir")
parser.add_argument("--repo")
parser.add_argument("--dry-run", action="store_true")
parser.add_argument("--db")
parser.add_argument("--issue")
parser.add_argument("--kind", default="issue")
parser.add_argument("--story", default="stories/bugfix")
parser.add_argument("--public-base-url", default="")
parser.add_argument("--project-root", default="")
parser.add_argument("--incident-repo", default="")
parser.add_argument("--asset-dir", default="")
parser.add_argument("--comment-mode", default="")
parser.add_argument("--json", action="store_true")
args = parser.parse_args()
if (args.verb1, args.verb2) == ("gh-agent", "drain"):
    assert args.db
    assert args.asset_dir, "product-journey must pass --asset-dir to gh-agent drain"
    assert args.comment_mode == "none", "product-journey drain tests must stay offline with --comment-mode none"
    db_path = Path(args.db)
    rows = json.loads(db_path.read_text()) if db_path.exists() else []
    jobs = []
    for i, row in enumerate(rows, start=1):
        row["state"] = "done"
        row["run_url"] = f"https://agent.example/run/job-{i}"
        row.setdefault("job_id", f"job-{i}")
        jobs.append({
            "job_id": row["job_id"],
            "origin_ref": row["origin_ref"],
            "repo": row["origin_ref"].split("/issue/")[0].removeprefix("github:"),
            "object_kind": "issue",
            "object_number": row["origin_ref"].split("/")[-1],
            "story": row["story"],
            "state": row["state"],
            "run_url": row["run_url"],
            "incident_url": "",
            "err_msg": "",
            "assets": [] if os.environ.get("KITSOKI_FAKE_GH_AGENT_NO_ASSETS") else [
                {
                    "name": "fix-report.md",
                    "mime_type": "text/markdown",
                    "size_bytes": 128,
                    "url": f"https://agent.example/run/job-{i}/artifacts/fix-report.md",
                },
                {
                    "name": "fix.patch",
                    "mime_type": "text/x-diff",
                    "size_bytes": 256,
                    "url": f"https://agent.example/run/job-{i}/artifacts/fix.patch",
                },
            ],
        })
    db_path.write_text(json.dumps(rows, indent=2, sort_keys=True) + "\n")
    print(json.dumps({
        "status": "drained",
        "drained_count": len(jobs),
        "done_count": len(jobs),
        "failed_count": 0,
        "active_count": 0,
        "jobs": jobs,
    }))
    sys.exit(0)
if (args.verb1, args.verb2) == ("gh-agent", "enqueue"):
    assert args.db and args.repo and args.issue
    origin = f"github:{args.repo}/{args.kind}/{args.issue}"
    db_path = Path(args.db)
    db_path.parent.mkdir(parents=True, exist_ok=True)
    rows = []
    if db_path.exists():
        rows = json.loads(db_path.read_text())
    created = origin not in [row["origin_ref"] for row in rows]
    if created:
        rows.append({"job_id": "job-" + args.issue, "origin_ref": origin, "story": args.story, "state": "queued"})
        db_path.write_text(json.dumps(rows, indent=2, sort_keys=True) + "\n")
    print(json.dumps({
        "status": "queued",
        "created": created,
        "job_id": "job-" + args.issue,
        "origin_ref": origin,
        "repo": args.repo,
        "object_kind": args.kind,
        "object_number": args.issue,
        "story": args.story,
        "state": "queued",
    }))
    sys.exit(0)
assert (args.verb1, args.verb2) == ("bug", "file-findings"), (args.verb1, args.verb2)
assert args.run_dir and args.repo

path = Path(args.run_dir) / "findings.json"
findings = json.loads(path.read_text())
outcomes, filed, skipped = [], 0, 0
for i, item in enumerate(findings.get("items", []), start=1):
    if item.get("kind") != "issue" or item.get("origin", "observed") == "seeded":
        continue
    if item.get("github_issue", {}).get("url"):
        skipped += 1
        outcomes.append({"finding_id": item.get("id", ""), "status": "skipped",
                         "issue_url": item["github_issue"]["url"]})
        continue
    if args.dry_run:
        outcomes.append({"finding_id": item.get("id", ""), "status": "dry-run",
                         "body": "## Expected\n...\n## Actual\n...\n## Reproduction\n..."})
        continue
    filed += 1
    url = f"https://github.com/{args.repo}/issues/{100 + i}"
    item["github_issue"] = {"url": url, "number": str(100 + i), "repo": args.repo,
                            "filed_at": "2026-07-05T00:00:00+00:00"}
    outcomes.append({"finding_id": item.get("id", ""), "status": "filed", "issue_url": url})
if not args.dry_run:
    findings["filing"] = {"requested": True, "ticket_repo": args.repo,
                          "updated_at": "2026-07-05T00:00:00+00:00",
                          "filed": filed, "skipped": skipped, "failed": 0}
    path.write_text(json.dumps(findings, indent=2, sort_keys=True) + "\n")
print(json.dumps({
    "status": "findings_dry_run" if args.dry_run else "findings_filed",
    "ticket_repo": args.repo, "run_dir": args.run_dir,
    "dry_run": args.dry_run,
    "filed": filed, "skipped": skipped, "failed": 0,
    "outcomes": outcomes,
}))
'''


def _check(name, cond):
    if not cond:
        print(f"FAIL: {name}")
        sys.exit(1)
    print(f"ok: {name}")


def review_check(review_result, check_id):
    for check in review_result["checks"]:
        if check["id"] == check_id:
            return check
    return None


def deck_scene(deck, eyebrow):
    for scene in deck.get("scenes", []):
        if scene.get("eyebrow") == eyebrow:
            return scene
    return {}


def attach_bugfix_proof(run_dir, scenario_id):
    evidence_dir = run_dir / "test-evidence"
    evidence_dir.mkdir(parents=True, exist_ok=True)
    evidence_kinds = [
        "session_trace",
        "candidate_diff",
        "oracle_result",
        "full_suite_result",
        "key_interaction_video",
    ]
    refs = []
    for kind in evidence_kinds:
        suffix = ".mp4" if kind == "key_interaction_video" else ".md"
        artifact = evidence_dir / f"{kind}{suffix}"
        artifact.write_text(f"{kind} proof\n", encoding="utf-8")
        run.attach_evidence(
            run_dir,
            scenario_id,
            kind,
            str(artifact),
            "validated",
            "cassette",
            f"{kind} proof for autonomous fix test",
            None,
        )
        refs.append(str(artifact))
    run.record_driver_event(
        run_dir,
        scenario_id,
        "replay",
        "validated",
        "Cassette replay produced the bugfix proof artifacts.",
        "story.driver_event,visual.observe",
        ",".join(refs),
        "",
        None,
    )


def main():
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        # Keep every artifact the runner writes inside the tempdir.
        run.ARTIFACT_ROOT = tmp / "product-journey"
        run.ARTIFACT_ROOT.mkdir(parents=True)

        fake = tmp / "fake_kitsoki.py"
        fake.write_text(FAKE_KITSOKI, encoding="utf-8")
        fake.chmod(fake.stat().st_mode | stat.S_IXUSR)
        os.environ["KITSOKI_BIN"] = f"{sys.executable} {fake}"

        catalog = run.load_catalog(run.CATALOG)
        personas = run.load_personas(run.PERSONAS)
        scenarios = run.load_scenarios(run.SCENARIOS)
        run_dir, run_json = run.build_run_bundle(
            catalog, run.load_github_targets(run.GITHUB_TARGETS),
            personas, scenarios, "vscode", "", "file-findings-test", "dry-run", None,
        )
        scenario_id = run_json["scenarios"][0]["id"]

        # Two credible issue findings + one seeded issue + one strength.
        run.record_finding(run_dir, "issue", "credible one", "observed problem one",
                           scenario_id, "high", "", "open", None)
        run.record_finding(run_dir, "issue", "credible two", "observed problem two",
                           scenario_id, "medium", "", "open", None)
        run.record_finding(run_dir, "issue", "seeded demo issue", "harness-only",
                           scenario_id, "low", "", "open", None, origin="seeded")
        run.record_finding(run_dir, "strength", "nice deck", "deck renders",
                           scenario_id, "low", "", "observed", None)

        # 1. Dry-run: candidates reported, bundle untouched, gates unarmed.
        before = (run_dir / "findings.json").read_text()
        result = run.file_findings(run_dir, "o/r", True, None)
        _check("dry-run status", result["status"] == "findings_dry_run")
        _check("dry-run reports 2 candidates",
               sum(1 for o in result["outcomes"] if o["status"] == "dry-run") == 2)
        _check("dry-run leaves findings.json untouched",
               (run_dir / "findings.json").read_text() == before)
        reviewed = run.review_run_bundle(run_dir, None)
        _check("filing gate unarmed before filing",
               review_check(reviewed, "findings-filed")["status"] == "pass"
               and review_check(reviewed, "findings-filed")["detail"] == "filing not requested")

        # 2. Filing: URLs + filing block recorded, derived artifacts refreshed.
        gh_agent_db = tmp / "gh-agent-jobs.json"
        result = run.file_findings(run_dir, "o/r", False, None, str(gh_agent_db), "stories/bugfix", True, "https://agent.example", "", "")
        _check("filed 2 credible findings", result["findings_filed_count"] == 2)
        _check("no credible finding left unfiled", result["findings_unfiled_count"] == 0)
        _check("filed urls surface", len(result["filed_issue_urls"]) == 2
               and all(u.startswith("https://github.com/o/r/issues/") for u in result["filed_issue_urls"]))
        _check("filed findings queued for gh-agent fixes",
               result["gh_agent_enqueue_status"] == "queued"
               and result["gh_agent_enqueued_count"] == 2
               and result["gh_agent_skipped_count"] == 0)
        _check("queued fixes drained by gh-agent",
               result["gh_agent_drain_status"] == "drained"
               and result["gh_agent_done_count"] == 2
               and result["gh_agent_failed_count"] == 0)
        _check("gh-agent run summary surfaces review links",
               "https://agent.example/run/job-1" in result["gh_agent_run_summary"])
        _check("gh-agent fix evidence fields are reported",
               result["gh_agent_fix_evidence_count"] == 4
               and result["gh_agent_missing_evidence_count"] == 0
               and "https://agent.example/run/job-1/artifacts/fix-report.md" in result["gh_agent_fix_evidence_summary"])
        queued_rows = json.loads(gh_agent_db.read_text())
        _check("gh-agent queue uses issue origin refs",
               sorted(row["origin_ref"] for row in queued_rows)
               == ["github:o/r/issue/101", "github:o/r/issue/102"])
        findings = run.read_json(run_dir / "findings.json")
        _check("filing block recorded", findings["filing"]["requested"] is True
               and findings["filing"]["ticket_repo"] == "o/r")
        deck = run.read_json(run_dir / "deck.slidey.json")
        gh_scene = deck_scene(deck, "GH-agent fixes")
        _check("deck has gh-agent fix review scene", bool(gh_scene))
        _check("deck includes filed issues and fix run URLs",
               "https://github.com/o/r/issues/101" in gh_scene.get("body", "")
               and "https://agent.example/run/job-1" in gh_scene.get("body", ""))
        _check("deck includes gh-agent fix evidence links",
               "https://agent.example/run/job-1/artifacts/fix-report.md" in gh_scene.get("body", "")
               and "https://agent.example/run/job-1/artifacts/fix.patch" in gh_scene.get("body", ""))
        seeded = [i for i in findings["items"] if i.get("origin") == "seeded"]
        _check("seeded finding not filed", not seeded[0].get("github_issue"))

        # 3. Idempotence: a re-run files nothing new.
        result = run.file_findings(run_dir, "o/r", False, None, str(gh_agent_db), "stories/bugfix", True, "https://agent.example", "", "")
        _check("re-run files nothing", result["findings_filed_count"] == 0)
        _check("re-run skips already-filed", result["findings_skipped_count"] == 2)
        _check("re-run attaches to queued fix jobs",
               result["gh_agent_enqueued_count"] == 2
               and not any(job["created"] for job in result["gh_agent_jobs"]))
        findings2 = run.read_json(run_dir / "findings.json")
        urls = sorted(i["github_issue"]["url"] for i in findings2["items"] if i.get("github_issue"))
        _check("urls stable across re-runs", len(urls) == 2 and len(set(urls)) == 2)

        # 4. Gates: review counts the filing check; a new credible finding
        # after filing trips review + validate until re-filed.
        reviewed = run.review_run_bundle(run_dir, None)
        _check("review has 21 checks", reviewed["total"] == 21)
        _check("findings-filed passes when fully filed",
               review_check(reviewed, "findings-filed")["status"] == "pass")
        _check("gh-agent-fixes passes when drained",
               review_check(reviewed, "gh-agent-fixes")["status"] == "pass")
        validated = run.validate_run_bundle(run_dir)
        _check("validate has no findings-filed error",
               not any(i["id"] == "findings-filed" for i in validated["issues"]))
        _check("validate has no gh-agent evidence error",
               not any(i["id"] in {"gh-agent-fixes", "gh-agent-fix-evidence", "gh-agent-fix-deck"} for i in validated["issues"]))
        saved_findings = run.read_json(run_dir / "findings.json")
        missing_asset_findings = json.loads(json.dumps(saved_findings))
        for job in missing_asset_findings["gh_agent"]["drained_jobs"]:
            job["assets"] = []
        run.write_json(run_dir / "findings.json", missing_asset_findings)
        reviewed_missing_assets = run.review_run_bundle(run_dir, None)
        _check("review fails done gh-agent jobs without evidence assets",
               review_check(reviewed_missing_assets, "gh-agent-fixes")["status"] == "fail")
        missing_asset_validated = run.validate_run_bundle(run_dir)
        _check("validate catches done gh-agent jobs without evidence assets",
               any(i["id"] == "gh-agent-fix-evidence" and i["severity"] == "error"
                   for i in missing_asset_validated["issues"]))
        run.write_json(run_dir / "findings.json", saved_findings)
        run.update_derived_artifacts(run_dir, None)
        deck_path = run_dir / "deck.slidey.json"
        stale_deck = run.read_json(deck_path)
        for scene in stale_deck["scenes"]:
            if scene.get("eyebrow") == "GH-agent fixes":
                scene["body"] = scene.get("body", "").replace("https://agent.example/run/job-1/artifacts/fix-report.md", "")
                break
        run.write_json(deck_path, stale_deck)
        stale_validated = run.validate_run_bundle(run_dir)
        _check("validate catches missing gh-agent evidence URL in deck",
               any(i["id"] == "gh-agent-fix-deck" and i["severity"] == "error"
                   for i in stale_validated["issues"]))
        run.update_derived_artifacts(run_dir, None)

        run.record_finding(run_dir, "issue", "late credible", "found after filing",
                           scenario_id, "high", "", "open", None)
        reviewed = run.review_run_bundle(run_dir, None)
        _check("late finding fails the review gate",
               review_check(reviewed, "findings-filed")["status"] == "fail")
        validated = run.validate_run_bundle(run_dir)
        _check("late finding is a validate error",
               any(i["id"] == "findings-filed" and i["severity"] == "error"
                   for i in validated["issues"]))

        # Re-file closes the gate again.
        run.file_findings(run_dir, "o/r", False, None, str(gh_agent_db), "stories/bugfix", True, "https://agent.example", "", "")
        reviewed = run.review_run_bundle(run_dir, None)
        _check("re-filing closes the gate",
               review_check(reviewed, "findings-filed")["status"] == "pass")

        # 5. Composite autonomous fix loop: one runner call owns filing,
        # gh-agent drain, review, and validation.
        stable_scenarios = [scenario for scenario in scenarios if scenario.get("id") == "bugfix"]
        run_dir2, run_json2 = run.build_run_bundle(
            catalog, run.load_github_targets(run.GITHUB_TARGETS),
            personas, stable_scenarios, "vscode", "", "autonomous-fix-test", "dry-run", None,
        )
        scenario2 = run_json2["scenarios"][0]["id"]
        attach_bugfix_proof(run_dir2, scenario2)
        run.record_finding(run_dir2, "issue", "autonomous credible", "observed problem",
                           scenario2, "high", "", "open", None)
        result = run.autonomous_fix_loop(
            run_dir2,
            "o/r",
            str(tmp / "gh-agent-autonomous.json"),
            "stories/bugfix",
            "https://agent.example",
            "",
            "",
            "",
            "none",
            None,
        )
        _check("autonomous loop validates bundle", result["autonomous_fix_status"] == "autonomous_fix_valid")
        _check("autonomous loop reports all reliability gates",
               result["autonomous_gate_summary"] == "filing=pass, gh_agent=pass, review=pass, validation=pass")
        _check("autonomous loop preserves filing status", result["filing_status"] == "findings_filed")
        _check("autonomous loop drained gh-agent", result["gh_agent_drain_status"] == "drained" and result["gh_agent_done_count"] == 1)
        _check("autonomous loop exposes fix evidence assets",
               result["gh_agent_fix_evidence_count"] == 2
               and result["gh_agent_missing_evidence_count"] == 0)
        _check("autonomous loop reviewed and validated", result["review_total_count"] == 21 and result["validation_status"] == "valid")

        run_dir3, run_json3 = run.build_run_bundle(
            catalog, run.load_github_targets(run.GITHUB_TARGETS),
            personas, stable_scenarios, "vscode", "", "autonomous-missing-evidence-test", "dry-run", None,
        )
        scenario3 = run_json3["scenarios"][0]["id"]
        attach_bugfix_proof(run_dir3, scenario3)
        run.record_finding(run_dir3, "issue", "autonomous missing evidence", "observed problem",
                           scenario3, "high", "", "open", None)
        os.environ["KITSOKI_FAKE_GH_AGENT_NO_ASSETS"] = "1"
        try:
            result = run.autonomous_fix_loop(
                run_dir3,
                "o/r",
                str(tmp / "gh-agent-autonomous-no-assets.json"),
                "stories/bugfix",
                "https://agent.example",
                "",
                "",
                "",
                "none",
                None,
            )
        finally:
            os.environ.pop("KITSOKI_FAKE_GH_AGENT_NO_ASSETS", None)
        _check("autonomous loop rejects done fixes without evidence",
               result["autonomous_fix_status"] == "autonomous_fix_invalid"
               and result["gh_agent_missing_evidence_count"] == 1)
        _check("autonomous loop reports failing gates for missing evidence",
               result["autonomous_gate_summary"] == "filing=pass, gh_agent=fail, review=fail, validation=fail"
               and any(i["id"] == "gh-agent-fix-evidence" for i in result["validation_issues"]))

        run_dir4, run_json4 = run.build_run_bundle(
            catalog, run.load_github_targets(run.GITHUB_TARGETS),
            personas, stable_scenarios, "vscode", "", "autonomous-no-issue-test", "dry-run", None,
        )
        scenario4 = run_json4["scenarios"][0]["id"]
        attach_bugfix_proof(run_dir4, scenario4)
        run.record_finding(run_dir4, "strength", "autonomous strength only", "proof exists but no issue was found",
                           scenario4, "low", "", "observed", None)
        result = run.autonomous_fix_loop(
            run_dir4,
            "o/r",
            str(tmp / "gh-agent-autonomous-no-issue.json"),
            "stories/bugfix",
            "https://agent.example",
            "",
            "",
            "",
            "none",
            None,
        )
        _check("autonomous loop rejects zero issue findings",
               result["autonomous_fix_status"] == "autonomous_fix_invalid"
               and result["findings_filed_count"] == 0
               and result["gh_agent_enqueued_count"] == 0)
        _check("autonomous loop requires at least one drained fix job",
               result["autonomous_gate_summary"] == "filing=fail, gh_agent=fail, review=fail, validation=fail"
               and any(i["id"] == "gh-agent-fixes" for i in result["validation_issues"]))

    print("PASS")


if __name__ == "__main__":
    main()
