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
  4. once filing was requested, review gains a findings-filed check (20 checks
     total) and validate errors on credible-but-unfiled findings.
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
"""Fake `kitsoki bug file-findings` used by file_findings_test.py.

Mirrors the Go orchestration's bundle contract: file each credible unfiled
issue finding (record github_issue), stamp findings.filing, print the JSON
result. --dry-run touches nothing.
"""
import argparse, json, sys
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("verb1")
parser.add_argument("verb2")
parser.add_argument("--run-dir", required=True)
parser.add_argument("--repo", required=True)
parser.add_argument("--dry-run", action="store_true")
args = parser.parse_args()
assert (args.verb1, args.verb2) == ("bug", "file-findings"), (args.verb1, args.verb2)

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
        result = run.file_findings(run_dir, "o/r", False, None)
        _check("filed 2 credible findings", result["findings_filed_count"] == 2)
        _check("no credible finding left unfiled", result["findings_unfiled_count"] == 0)
        _check("filed urls surface", len(result["filed_issue_urls"]) == 2
               and all(u.startswith("https://github.com/o/r/issues/") for u in result["filed_issue_urls"]))
        findings = run.read_json(run_dir / "findings.json")
        _check("filing block recorded", findings["filing"]["requested"] is True
               and findings["filing"]["ticket_repo"] == "o/r")
        seeded = [i for i in findings["items"] if i.get("origin") == "seeded"]
        _check("seeded finding not filed", not seeded[0].get("github_issue"))

        # 3. Idempotence: a re-run files nothing new.
        result = run.file_findings(run_dir, "o/r", False, None)
        _check("re-run files nothing", result["findings_filed_count"] == 0)
        _check("re-run skips already-filed", result["findings_skipped_count"] == 2)
        findings2 = run.read_json(run_dir / "findings.json")
        urls = sorted(i["github_issue"]["url"] for i in findings2["items"] if i.get("github_issue"))
        _check("urls stable across re-runs", len(urls) == 2 and len(set(urls)) == 2)

        # 4. Gates: review counts the filing check; a new credible finding
        # after filing trips review + validate until re-filed.
        reviewed = run.review_run_bundle(run_dir, None)
        _check("review has 20 checks", reviewed["total"] == 20)
        _check("findings-filed passes when fully filed",
               review_check(reviewed, "findings-filed")["status"] == "pass")
        validated = run.validate_run_bundle(run_dir)
        _check("validate has no findings-filed error",
               not any(i["id"] == "findings-filed" for i in validated["issues"]))

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
        run.file_findings(run_dir, "o/r", False, None)
        reviewed = run.review_run_bundle(run_dir, None)
        _check("re-filing closes the gate",
               review_check(reviewed, "findings-filed")["status"] == "pass")

    print("PASS")


if __name__ == "__main__":
    main()
