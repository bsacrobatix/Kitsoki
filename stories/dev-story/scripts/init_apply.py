#!/usr/bin/env python3
"""Apply local project onboarding files for dev-story."""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from init_transcripts import mining_recommendation  # noqa: E402


def q(value: str) -> str:
    return json.dumps(value or "")


def slug(value: str) -> str:
    value = value.strip().lower()
    value = re.sub(r"[^a-z0-9]+", "-", value).strip("-")
    return value or "project"


def write_text(path: Path, content: str, writes: list[str]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    old = path.read_text(encoding="utf-8") if path.exists() else None
    if old != content:
        path.write_text(content, encoding="utf-8")
        writes.append(str(path))


def yaml_scalar(value) -> str:
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return str(value)
    if isinstance(value, str):
        return json.dumps(value)
    return json.dumps(value)


def yaml_dump(value, indent: int = 0) -> str:
    pad = " " * indent
    if isinstance(value, dict):
        lines = []
        for key in sorted(value):
            item = value[key]
            if isinstance(item, (dict, list)):
                lines.append(f"{pad}{key}:")
                lines.append(yaml_dump(item, indent + 2))
            else:
                lines.append(f"{pad}{key}: {yaml_scalar(item)}")
        return "\n".join(lines)
    if isinstance(value, list):
        if not value:
            return pad + "[]"
        lines = []
        for item in value:
            if isinstance(item, (dict, list)):
                lines.append(f"{pad}-")
                lines.append(yaml_dump(item, indent + 2))
            else:
                lines.append(f"{pad}- {yaml_scalar(item)}")
        return "\n".join(lines)
    return pad + yaml_scalar(value)


def profile_yaml_from_draft(profile: dict) -> str:
    return yaml_dump(profile).rstrip() + "\n"


def ensure_draft_profile_defaults(profile: dict, data: dict) -> None:
    dev_story_profile = profile.setdefault("dev_story_profile", {})
    if not isinstance(dev_story_profile, dict):
        dev_story_profile = {}
        profile["dev_story_profile"] = dev_story_profile
    docs = dev_story_profile.setdefault("docs", {})
    if not isinstance(docs, dict):
        docs = {}
        dev_story_profile["docs"] = docs
    for key, value in dev_story_docs_profile(data).items():
        docs.setdefault(key, value)

    bugfix = dev_story_profile.setdefault("bugfix", {})
    if not isinstance(bugfix, dict):
        bugfix = {}
        dev_story_profile["bugfix"] = bugfix
    if data.get("build_command"):
        bugfix.setdefault("build_cmd", data["build_command"])
    if data.get("test_command"):
        bugfix.setdefault("test_cmd", data["test_command"])

    onboarding = profile.setdefault("onboarding", {})
    if not isinstance(onboarding, dict):
        onboarding = {}
        profile["onboarding"] = onboarding
    defaults = onboarding_profile(data)
    for key, value in defaults.items():
        onboarding.setdefault(key, value)


def validate_profile_yaml(content: str, root: Path) -> dict:
    kitsoki_bin = os.environ.get("KITSOKI_BIN", "kitsoki")
    with tempfile.TemporaryDirectory(prefix="kitsoki-profile-") as tmp:
        path = Path(tmp) / "project-profile.yaml"
        path.write_text(content, encoding="utf-8")
        proc = subprocess.run(
            [kitsoki_bin, "project-profile", "validate", "--json", "--repo-root", str(root), str(path)],
            check=False,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    report = {}
    if proc.stdout.strip():
        try:
            report = json.loads(proc.stdout)
        except json.JSONDecodeError:
            report = {"ok": False, "schema": ["validator returned non-json stdout"]}
    ok = bool(report.get("ok")) and proc.returncode == 0
    return {
        "ok": ok,
        "schema": report.get("schema", []),
        "semantic": report.get("semantic", []),
        "warnings": report.get("warnings", []),
        "validator_stdout": proc.stdout,
        "validator_stderr": proc.stderr,
        "validator_exit_code": proc.returncode,
    }


def git_output(path: Path, *args: str) -> str:
    try:
        proc = subprocess.run(
            ["git", "-C", str(path), *args],
            check=False,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        )
    except (OSError, ValueError):
        return ""
    if proc.returncode != 0:
        return ""
    return proc.stdout.strip()


def git_info(path: Path) -> dict:
    inside = git_output(path, "rev-parse", "--is-inside-work-tree")
    if inside != "true":
        return {"vcs": "none", "default_branch": "", "remote": ""}
    remote = git_output(path, "config", "--get", "remote.origin.url")
    default_branch = git_output(path, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
    if default_branch.startswith("origin/"):
        default_branch = default_branch.split("/", 1)[1]
    if not default_branch:
        default_branch = git_output(path, "symbolic-ref", "--quiet", "--short", "HEAD")
    if not default_branch:
        for candidate in ("main", "master"):
            if git_output(path, "show-ref", "--verify", f"refs/heads/{candidate}"):
                default_branch = candidate
                break
    return {"vcs": "git", "default_branch": default_branch or "main", "remote": remote}


def append_gitignore(path: Path, writes: list[str]) -> None:
    additions = [
        ".kitsoki.local.yaml",
        ".kitsoki/sessions/",
        ".artifacts/",
        ".context/",
        ".worktrees/",
    ]
    current = path.read_text(encoding="utf-8") if path.exists() else ""
    existing = {line.strip() for line in current.splitlines()}
    existing_normalized = {line.rstrip("/") for line in existing}
    missing = [entry for entry in additions if entry.rstrip("/") not in existing_normalized]
    if not missing:
        return
    block = "\n# Kitsoki local runtime\n" + "\n".join(missing) + "\n"
    path.write_text(current.rstrip() + block, encoding="utf-8")
    writes.append(str(path))


def dev_story_docs_profile(data: dict) -> dict:
    if data["project_id"] == "slidey":
        defaults = {
            "publish_durable_path": "docs/prd",
            "prd_doc_filename": "",
            "design_template_dir": "docs/proposals/templates",
            "design_durable_path": "docs/proposals",
            "design_doc_filename": "",
            "design_ticket_dir": "",
            "ticket_repo": "",
        }
    else:
        defaults = {
            "publish_durable_path": ".context/prd",
            "prd_doc_filename": "",
            "design_template_dir": "",
            "design_durable_path": ".context/designs",
            "design_doc_filename": "",
            "design_ticket_dir": "",
            "ticket_repo": "",
        }
    override = data.get("dev_story_docs_profile")
    if isinstance(override, dict):
        for key in defaults:
            if key in override and override[key] is not None:
                defaults[key] = str(override[key])
    return defaults


def app_yaml(data: dict) -> str:
    project_id = data["project_id"]
    title = data["project_title"]
    docs = dev_story_docs_profile(data)
    return f"""app:
  id: {project_id}-dev
  version: 0.1.0
  title: {q(project_id + "-dev - work on " + title + " with Kitsoki")}
  author: "Kitsoki"
  license: "CC0"

routing:
  embedding:
    model: "nomic-embed-text-v1.5"

hosts:
  - host.local_files.ticket
  - host.gh.ticket
  - host.git
  - host.local
  - host.git_worktree
  - host.append_to_file
  - host.inbox.add
  - host.agent.ask
  - host.agent.decide
  - host.agent.task
  - host.agent.search
  - host.agent.converse
  - host.chat.resolve
  - host.artifacts_dir
  - host.ide.open_file
  - host.ide.open_diff
  - host.diff.open
  - host.run
  - host.starlark.run

imports:
  core:
    source: "@kitsoki/dev-story"
    entry: landing
    hosts: declared
    host_bindings:
      ticket:    host.local_files.ticket
      vcs:       host.git
      ci:        host.local
      workspace: host.git_worktree
      transport: host.append_to_file
    world_in:
      workdir:                    "{{{{ world.workdir }}}}"
      repo_root:                  "{{{{ world.repo_root }}}}"
      judge_mode:                 "{{{{ world.judge_mode }}}}"
      judge_confidence_threshold: "{{{{ world.judge_confidence_threshold }}}}"
      publish_durable_path:       "{{{{ world.publish_durable_path }}}}"
      prd_doc_filename:           "{{{{ world.prd_doc_filename }}}}"
      design_template_dir:        "{{{{ world.design_template_dir }}}}"
      design_durable_path:        "{{{{ world.design_durable_path }}}}"
      design_doc_filename:        "{{{{ world.design_doc_filename }}}}"
      design_ticket_dir:          "{{{{ world.design_ticket_dir }}}}"
      ticket_repo:                "{{{{ world.ticket_repo }}}}"
      # Project toolchain commands. Empty means dev-story/bugfix keeps its
      # default gates; non-Go projects should set these from their profile.
      build_cmd:                  "{{{{ world.build_cmd }}}}"
      test_cmd:                   "{{{{ world.test_cmd }}}}"

world:
  workdir:                    {{ type: string, default: "." }}
  repo_root:                  {{ type: string, default: "." }}
  ticket_repo:                {{ type: string, default: "" }}
  judge_mode:                 {{ type: string, default: "human" }}
  judge_confidence_threshold: {{ type: float, default: 0.8 }}

  publish_durable_path:       {{ type: string, default: {q(docs["publish_durable_path"])} }}
  prd_doc_filename:           {{ type: string, default: {q(docs["prd_doc_filename"])} }}
  design_template_dir:        {{ type: string, default: {q(docs["design_template_dir"])} }}
  design_durable_path:        {{ type: string, default: {q(docs["design_durable_path"])} }}
  design_doc_filename:        {{ type: string, default: {q(docs["design_doc_filename"])} }}
  design_ticket_dir:          {{ type: string, default: {q(docs["design_ticket_dir"])} }}
  build_cmd:                  {{ type: string, default: {q(data.get("build_command", ""))} }}
  test_cmd:                   {{ type: string, default: {q(data.get("test_command", ""))} }}

root: core
"""


def profile_yaml(data: dict) -> str:
    if data["project_id"] == "slidey":
        return slidey_profile_yaml(data)
    return generic_profile_yaml(data)


def stack_kind(data: dict) -> str:
    stack = (data.get("stack") or "").lower()
    if "rust" in stack:
        return "rust"
    if "go project" in stack:
        return "go"
    if "node" in stack:
        return "node"
    if "python" in stack:
        return "python"
    return "generic"


def enrich_project_shape(data: dict, root: Path) -> None:
    repo = git_info(root)
    data["repo_vcs"] = repo["vcs"]
    data["repo_default_branch"] = repo["default_branch"]
    data["repo_remote"] = repo["remote"]
    data["has_makefile"] = (root / "Makefile").exists()
    cargo = root / "Cargo.toml"
    data["has_cargo"] = cargo.exists()
    package_json = root / "package.json"
    data["has_package_json"] = package_json.exists()
    data["node_package_manager"] = "npm"
    if package_json.exists():
        try:
            package = json.loads(package_json.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            package = {}
        package_manager = package.get("packageManager") if isinstance(package.get("packageManager"), str) else ""
        if package_manager:
            name = package_manager.split("@", 1)[0].strip().lower()
            if name in {"npm", "pnpm", "yarn", "bun"}:
                data["node_package_manager"] = name
    if (root / "pnpm-lock.yaml").exists():
        data["node_package_manager"] = "pnpm"
    elif (root / "yarn.lock").exists():
        data["node_package_manager"] = "yarn"
    elif (root / "bun.lock").exists() or (root / "bun.lockb").exists():
        data["node_package_manager"] = "bun"
    data["has_pyproject"] = (root / "pyproject.toml").exists()
    data["has_requirements"] = (root / "requirements.txt").exists()
    data["has_uv_lock"] = (root / "uv.lock").exists()
    data["has_poetry_lock"] = (root / "poetry.lock").exists()
    data["rules_files"] = [
        name
        for name in ("AGENTS.md", "CLAUDE.md", ".cursorrules", ".windsurfrules")
        if (root / name).exists()
    ]
    data["is_monorepo"] = False
    if cargo.exists():
        try:
            text = cargo.read_text(encoding="utf-8")
            data["is_monorepo"] = "[workspace]" in text or "\n[workspace." in text
        except OSError:
            data["is_monorepo"] = False


def package_managers(data: dict, kind: str) -> str:
    managers: list[str] = []
    if kind == "rust" or data.get("has_cargo"):
        managers.append("cargo")
    elif kind == "go":
        managers.append("go")
    elif kind == "node":
        managers.append(data.get("node_package_manager") or "npm")
    elif kind == "python":
        if data.get("has_uv_lock"):
            managers.append("uv")
        if data.get("has_poetry_lock"):
            managers.append("poetry")
        if data.get("has_pyproject"):
            managers.append("pyproject")
        if data.get("has_requirements") or not managers:
            managers.append("pip")
    if data.get("has_makefile"):
        managers.append("make")
    return "[" + ", ".join(managers) + "]" if managers else "[]"


def convention_source(value: str) -> str:
    normalized = (value or "").strip().lower()
    if normalized in {"kitsoki", "project", "hybrid"}:
        return normalized
    return "project"


def mining_seed_enabled(data: dict) -> bool:
    mining = data.get("mining_recommendation")
    if not isinstance(mining, dict):
        return False
    return int(mining.get("transcript_count") or 0) > 0 or mining.get("status") == "transcripts-found"


def mining_seed_path() -> str:
    return ".context/kitsoki-session-mining-seed.md"


def mining_setup_writes(data: dict) -> list[dict]:
    if not mining_seed_enabled(data):
        return []
    return [{
        "path": mining_seed_path(),
        "action": "create",
        "summary": "Operator review note for seeding project customization from associated Claude/Codex transcripts.",
    }]


def mining_profile_yaml(data: dict) -> str:
    mining = data.get("mining_recommendation") or mining_recommendation(Path(data["target_path"]))
    if mining_seed_enabled({"mining_recommendation": mining}):
        mining = dict(mining)
        mining["job"] = mining.get("job") or "seed-pending-operator-review"
        note = mining.get("note") or ""
        review = (
            f"Seed review note: `{mining_seed_path()}`. "
            "Run the first mining pass in propose-only mode after operator review."
        )
        mining["note"] = f"{note}\n\n{review}".strip()
    return yaml_dump(mining, 2)


def onboarding_profile(data: dict) -> dict:
    kind = stack_kind(data)
    patterns = [
        {
            "id": "selected-starter",
            "source": "deterministic-init",
            "evidence": f"Selected dev-story for a {data.get('stack') or 'local project'} checkout.",
            "recommendation": "Start with the shared dev-story workflow and keep project-specific changes in `.kitsoki/project-profile.yaml` or the generated `.kitsoki/stories/<id>-dev/` wrapper.",
        }
    ]
    managers = package_managers(data, kind)
    if managers != "[]":
        patterns.append({
            "id": "toolchain",
            "source": "repo-files",
            "evidence": f"Stack kind `{kind}` with package/tool managers {managers}.",
            "recommendation": "Project command gates are projected into dev-story from the profile rather than hardcoded in the shared story.",
        })
    if data.get("repo_vcs") != "none" or data.get("repo_default_branch") or data.get("repo_remote"):
        patterns.append({
            "id": "repo-metadata",
            "source": "local-git",
            "evidence": f"VCS `{data.get('repo_vcs', 'none')}`, default branch `{data.get('repo_default_branch', '')}`, remote `{data.get('repo_remote', '')}`.",
            "recommendation": "Use local VCS metadata for worktree and handoff defaults; no network lookup is required during onboarding.",
        })
    rules_files = data.get("rules_files") if isinstance(data.get("rules_files"), list) else []
    if rules_files:
        patterns.append({
            "id": "repo-rules",
            "source": "rules-files",
            "evidence": "Found project rule files: " + ", ".join(f"`{path}`" for path in rules_files) + ".",
            "recommendation": "Treat these as the first customization source before mining older transcripts.",
        })
    mining = data.get("mining_recommendation") if isinstance(data.get("mining_recommendation"), dict) else {}
    if int(mining.get("transcript_count") or 0) > 0:
        patterns.append({
            "id": "associated-transcripts",
            "source": "local-transcripts",
            "evidence": f"Found {mining.get('transcript_count')} associated Claude/Codex sessions.",
            "recommendation": f"Review `{mining_seed_path()}` and run session mining in propose-only mode to evolve this profile.",
        })

    customizations = [
        {
            "id": "project-local-instance",
            "status": "applied",
            "summary": "Generate a thin project-owned dev-story wrapper under `.kitsoki/stories/<id>-dev/` that imports `@kitsoki/dev-story`.",
            "evidence": f".kitsoki/stories/{data['project_id']}-dev/app.yaml",
        },
        {
            "id": "project-doc-defaults",
            "status": "applied",
            "summary": "Retarget generated PRD/design outputs to project-local runtime docs paths instead of Kitsoki's own docs tree.",
            "evidence": f"{dev_story_docs_profile(data)['publish_durable_path']}, {dev_story_docs_profile(data)['design_durable_path']}",
        },
    ]
    if data.get("build_command") or data.get("test_command"):
        customizations.append({
            "id": "toolchain-gates",
            "status": "applied",
            "summary": "Project build/test commands are projected into dev-story bugfix gates.",
            "evidence": f"build={data.get('build_command', '')}; test={data.get('test_command', '')}",
        })
    if mining_seed_enabled(data):
        customizations.append({
            "id": "session-mining-seed",
            "status": "pending",
            "summary": "Associated transcript history is captured as an operator-reviewed seed for future project customization.",
            "evidence": mining_seed_path(),
        })

    return {
        "base_story": "dev-story",
        "base_story_title": "Dev-story project workflow",
        "base_story_reason": "Default starter for normal software repositories: it supports project workbench, design/PRD, bugfix, git/worktree, and follow-up customization through a project-local wrapper.",
        "repo_patterns": patterns,
        "story_customizations": customizations,
        "recording_policy": "no-llm-only",
    }


def onboarding_profile_yaml(data: dict, indent: int = 2) -> str:
    return yaml_dump(onboarding_profile(data), indent)


def generic_setup_plan_yaml(data: dict) -> str:
    project_id = data["project_id"]
    docs = dev_story_docs_profile(data)
    verifications = [
        {
            "id": "story-load",
            "kind": "story",
            "command": f"kitsoki validate .kitsoki/stories/{project_id}-dev/app.yaml",
            "gate": "required",
        },
    ]
    if data.get("build_command"):
        verifications.append({
            "id": "build",
            "kind": "build",
            "command": data["build_command"],
            "gate": "required",
        })
    if data.get("test_command"):
        verifications.append({
            "id": "unit-tests",
            "kind": "tests",
            "command": data["test_command"],
            "gate": "required",
        })
    if data.get("check_command"):
        verifications.append({
            "id": "check",
            "kind": "tests",
            "command": data["check_command"],
            "gate": "recommended",
        })
    if data.get("dev_command"):
        verifications.append({
            "id": "dev-command",
            "kind": "dev-server",
            "command": data["dev_command"],
            "gate": "advisory",
        })
    return yaml_dump({
        "writes": [
            {
                "path": ".kitsoki/project-profile.yaml",
                "action": "create",
                "summary": "Declarative onboarding profile for this project.",
            },
            {
                "path": f".kitsoki/stories/{project_id}-dev/app.yaml",
                "action": "create",
                "summary": "Thin project-owned dev-story instance importing @kitsoki/dev-story.",
            },
            {
                "path": f".kitsoki/stories/{project_id}-dev/README.md",
                "action": "create",
                "summary": "Local operator handoff for running and extending the generated instance.",
            },
            {
                "path": ".kitsoki.yaml",
                "action": "create",
                "summary": "Discover project-local Kitsoki stories and select the generated instance.",
            },
            {
                "path": ".gitignore",
                "action": "merge",
                "summary": "Ignore local Kitsoki runtime, session, artifact, and worktree files.",
            },
        ] + mining_setup_writes(data),
        "dirs_create": [
            ".kitsoki",
            ".kitsoki/stories",
            f".kitsoki/stories/{project_id}-dev",
            ".context",
            docs["publish_durable_path"],
            docs["design_durable_path"],
            ".artifacts",
            ".worktrees",
        ],
        "gitignore_additions": [
            ".kitsoki.local.yaml",
            ".kitsoki/sessions/",
            ".artifacts/",
            ".context/",
            ".worktrees/",
        ],
        "verifications": verifications,
    }, 2)


def generic_profile_yaml(data: dict) -> str:
    kind = stack_kind(data)
    docs = dev_story_docs_profile(data)
    languages = {
        "rust": "[rust]",
        "go": "[go]",
        "node": "[javascript]",
        "python": "[python]",
    }.get(kind, "[]")
    return f"""schema: project-profile/v1
id: {data["project_id"]}
title: {data["project_title"]}
summary: |
  Project-local Kitsoki dev-story binding for {data["project_title"]}. The
  generated instance imports `@kitsoki/dev-story`; this repository owns only the
  profile values, tool commands, and local setup files.

commands:
  dev: {q(data.get("dev_command", ""))}
  test: {q(data.get("test_command", ""))}
  build: {q(data.get("build_command", ""))}
  check: {q(data.get("check_command", ""))}

repo:
  root: "."
  vcs: {data.get("repo_vcs", "git")}
  default_branch: {q(data.get("repo_default_branch", "main"))}
  remote: {q(data.get("repo_remote", ""))}
  monorepo: {str(bool(data.get("is_monorepo"))).lower()}

stack:
  kind: {q(kind)}
  languages: {languages}
  package_managers: {package_managers(data, kind)}

testing:
  mechanisms:
    - kind: unit
      runner: command
      command: {q(data.get("test_command", ""))}
    - kind: build
      runner: command
      command: {q(data.get("build_command", ""))}

conventions:
  source: {convention_source(data.get("conventions", "project"))}
  dirs:
    context:   {{ path: ".context",   use: local-runtime }}
    artifacts: {{ path: ".artifacts", use: local-runtime }}
    worktrees: {{ path: ".worktrees", use: local-runtime }}
  rules_files:
{yaml_dump(data.get("rules_files") or [], 4)}
  gitignore:
    manage: true
    additions:
      - ".kitsoki.local.yaml"
      - ".kitsoki/sessions/"
      - ".artifacts/"
      - ".context/"
      - ".worktrees/"

tracker:
  provider: {q(data.get("tracker", "none"))}

kitsoki:
  story: dev-story
  instance:
    id: {data["project_id"]}-dev
    path: .kitsoki/stories/{data["project_id"]}-dev/app.yaml
    bindings:
      ticket: host.local_files.ticket
      vcs: host.git
      ci: host.local
      workspace: host.git_worktree
      transport: host.append_to_file
  judge_mode: human
  autonomy: supervised

dev_story_profile:
  docs:
    publish_durable_path: {q(docs["publish_durable_path"])}
    prd_doc_filename: {q(docs["prd_doc_filename"])}
    design_template_dir: {q(docs["design_template_dir"])}
    design_durable_path: {q(docs["design_durable_path"])}
    design_doc_filename: {q(docs["design_doc_filename"])}
    design_ticket_dir: {q(docs["design_ticket_dir"])}
    ticket_repo: {q(docs["ticket_repo"])}
  bugfix:
    build_cmd: {q(data.get("build_command", ""))}
    test_cmd: {q(data.get("test_command", ""))}

onboarding:
{onboarding_profile_yaml(data)}

mining:
{mining_profile_yaml(data)}

setup_plan:
{generic_setup_plan_yaml(data)}

readiness:
  status: not-run
"""


def slidey_profile_yaml(data: dict) -> str:
    docs = dev_story_docs_profile(data)
    return f"""schema: project-profile/v1
id: slidey
title: Slidey
summary: |
  Deterministic, spec-driven declarative deck engine. Slidey renders JSON scene
  specs through the same Vue components as an interactive web player, a
  self-contained HTML deck, PDF, and optional deterministic MP4 video export.

generated:
  by: "kitsoki dev-story project onboarding"
  at: "2026-06-23"

repo:
  root: "."
  vcs: {data.get("repo_vcs", "git")}
  default_branch: {q(data.get("repo_default_branch", "main"))}
  remote: {q(data.get("repo_remote", ""))}
  monorepo: false

stack:
  kind: fullstack
  languages: [javascript]
  frameworks: [vue, vite, puppeteer]
  package_managers: [npm]

dev_server:
  components:
    - name: viewer
      role: frontend
      command: "npm run dev"
      cwd: "."
      url: "http://127.0.0.1:5173"
      ready:
        probe: http
        target: "http://127.0.0.1:5173/"
        expect: "200"
        timeout_ms: 30000
        interval_ms: 500
    - name: cli-viewer
      role: backend
      command: {q(data.get("dev_command", ""))}
      cwd: "."
      url: "http://127.0.0.1:5000"
      ready:
        probe: http
        target: "http://127.0.0.1:5000/"
        expect: "200"
        timeout_ms: 30000
        interval_ms: 500

environments:
  - name: local
    kind: local
    url: "http://127.0.0.1:5000"
    config_ref: ".kitsoki.yaml"
    notes: "Use the CLI viewer for workspace behavior; plain Vite is useful for component work."
  - name: ci
    kind: test
    config_ref: "package.json"
    notes: "npm test exercises the Node test suite."

commands:
  install: "npm install"
  build: {q(data.get("build_command", ""))}
  dev: {q(data.get("dev_command", ""))}
  viewer: {q(data.get("dev_command", ""))}
  html_bundle: "node src/index.js bundle examples/hello.slidey.json .artifacts/hello.html"
  validate_deck: "node src/index.js examples/hello.slidey.json --validate"
  render_pdf: "node src/index.js examples/hello.slidey.json .artifacts/hello.pdf"
  render_mp4: "node src/index.js examples/hello.slidey.json .artifacts/hello.mp4"
  test: {q(data.get("test_command", ""))}
  e2e: "npm run test:vscode"
  lint: ""
  typecheck: ""

output_workflows:
  primary_review:
    format: web-player
    command: {q(data.get("dev_command", ""))}
    url: "http://127.0.0.1:5000/"
    use_when: "Inspecting, editing, navigating, or reviewing a deck interactively."
    notes: "This is the default human review path; it serves the real Vue viewer and workspace assets."
  shareable_review:
    format: single-file-html
    command: "node src/index.js bundle examples/hello.slidey.json .artifacts/hello.html"
    output: ".artifacts/hello.html"
    use_when: "Sending or archiving a deck review artifact that should open without a local server."
    notes: "The HTML bundle inlines the viewer, CSS, spec, and local image/gif assets."
  export:
    format: mp4
    command: "node src/index.js examples/hello.slidey.json .artifacts/hello.mp4"
    output: ".artifacts/hello.mp4"
    use_when: "Producing fixed video evidence, narrated playback, or a video scene source for another deck."
    notes: "MP4 is not the primary review surface; use it only when a baked video artifact is needed."

testing:
  frameworks: [node-test]
  mechanisms:
    - kind: unit
      runner: node-test
      command: {q(data.get("test_command", ""))}
    - kind: build
      runner: npm
      command: {q(data.get("build_command", ""))}
    - kind: e2e
      runner: node-test
      command: "npm run test:vscode"
  ci:
    provider: none
    config_ref: "package.json"

golden_path:
  applicable: true
  kind: ui
  name: "Open a Slidey spec in the web player"
  description: |
    Start the Slidey CLI workspace server on a known example deck and confirm
    the interactive web player responds without using any LLM or
    network-dependent narration. Use single-file HTML for portable review and
    MP4 only for baked video evidence or narration output.
  steps:
    - "Run {data.get("dev_command", "")}"
    - "Open http://127.0.0.1:5000/"
    - "Confirm the selected example deck renders."
    - "For shareable deck review, run node src/index.js bundle examples/hello.slidey.json .artifacts/hello.html"
  verify:
    harness: command
    spec: "node src/index.js examples/hello.slidey.json --validate"
    url: "http://127.0.0.1:5000/"

conventions:
  source: hybrid
  dirs:
    context:   {{ path: ".context",   use: existing }}
    artifacts: {{ path: ".artifacts", use: existing }}
    worktrees: {{ path: ".worktrees", use: existing }}
  gitignore:
    manage: true
    additions:
      - ".kitsoki.local.yaml"
      - ".kitsoki/sessions/"
      - ".artifacts/"
      - ".context"
      - ".worktrees"
  rules_files: []

rules:
  - id: web-player-first
    scope: testing
    source: operator
    ref: "README.md#install-as-a-cli--open-a-folderfile"
    text: "Use the Slidey web player as the primary deck review surface; reserve MP4 for fixed video evidence or narration export."
  - id: html-for-shareable-review
    scope: artifacts
    source: operator
    ref: "README.md#install-as-a-cli--open-a-folderfile"
    text: "When a reviewable deck artifact is needed, prefer a single-file HTML bundle over an embedded MP4 unless motion/narration is the goal."
  - id: no-real-llm-in-tests
    scope: tests
    source: kitsoki
    ref: "Kitsoki AGENTS.md"
    text: "Automated Kitsoki story tests use mocked interactions and never require a real LLM."

kitsoki:
  story: dev-story
  instance:
    id: slidey-dev
    path: ".kitsoki/stories/slidey-dev/app.yaml"
    bindings:
      ticket: host.local_files.ticket
      vcs: host.git
      ci: host.local
      workspace: host.git_worktree
      transport: host.append_to_file
  harness_profile: ""
  judge_mode: human
  autonomy: supervised

dev_story_profile:
  docs:
    publish_durable_path: {q(docs["publish_durable_path"])}
    prd_doc_filename: {q(docs["prd_doc_filename"])}
    design_template_dir: {q(docs["design_template_dir"])}
    design_durable_path: {q(docs["design_durable_path"])}
    design_doc_filename: {q(docs["design_doc_filename"])}
    design_ticket_dir: {q(docs["design_ticket_dir"])}
    ticket_repo: {q(docs["ticket_repo"])}
  bugfix:
    build_cmd: {q(data.get("build_command", ""))}
    test_cmd: {q(data.get("test_command", ""))}

onboarding:
{onboarding_profile_yaml(data)}

mining:
{mining_profile_yaml(data)}

setup_plan:
  writes:
    - path: ".kitsoki/project-profile.yaml"
      action: create
      summary: "Declarative onboarding profile for Slidey."
    - path: ".kitsoki/stories/slidey-dev/app.yaml"
      action: create
      summary: "Materialized dev-story instance for Slidey."
    - path: ".kitsoki.yaml"
      action: create
      summary: "Discover project-local stories under ./.kitsoki/stories."
    - path: ".gitignore"
      action: merge
      summary: "Ignore local Kitsoki runtime/session artifacts."
{yaml_dump(mining_setup_writes(data), 4) if mining_seed_enabled(data) else ""}
  dirs_create: [".context", ".artifacts", ".worktrees", ".kitsoki"]
  gitignore_additions:
    - ".kitsoki.local.yaml"
    - ".kitsoki/sessions/"
    - ".artifacts/"
    - ".context"
    - ".worktrees"
  verifications:
    - id: story-load
      kind: story
      command: "kitsoki run .kitsoki/stories/slidey-dev/app.yaml"
      gate: required
    - id: unit-tests
      kind: tests
      command: {q(data.get("test_command", ""))}
      gate: required
    - id: build
      kind: build
      command: {q(data.get("build_command", ""))}
      gate: required
    - id: cli-validate
      kind: golden-path
      command: "node src/index.js examples/hello.slidey.json --validate"
      gate: required
    - id: html-bundle
      kind: artifact
      command: "node src/index.js bundle examples/hello.slidey.json .artifacts/hello.html"
      gate: recommended
    - id: web-player
      kind: ui
      command: {q(data.get("dev_command", ""))}
      gate: recommended

readiness:
  status: not-run
"""


def config_yaml(project_id: str) -> str:
    return f"""story_dirs:
  - ./.kitsoki/stories

project_profile: .kitsoki/project-profile.yaml

root:
  import: dev-story
"""


def mining_seed_markdown(data: dict) -> str:
    mining = data.get("mining_recommendation") if isinstance(data.get("mining_recommendation"), dict) else {}
    sources = mining.get("sources") if isinstance(mining, dict) else []
    if not isinstance(sources, list):
        sources = []
    source_lines = []
    for source in sources:
        if not isinstance(source, dict):
            continue
        backend = source.get("backend", "unknown")
        sessions = source.get("sessions", 0)
        path = source.get("dir", "")
        source_lines.append(f"- {backend}: {sessions} sessions at `{path}`")
    if not source_lines:
        source_lines.append("- No transcript source details were recorded.")
    first_pass = mining.get("first_pass_sample", 0) if isinstance(mining, dict) else 0
    sample = mining.get("sample", "recency") if isinstance(mining, dict) else "recency"
    note = mining.get("note", "") if isinstance(mining, dict) else ""
    return f"""# Kitsoki Session Mining Seed

Kitsoki found existing Claude/Codex transcript history associated with this
checkout during first-run onboarding. No mining pass has run yet and no LLM cost
was incurred by onboarding.

Review goal:

- Seed project-local customization from prior real usage.
- Prefer proposed `.kitsoki/` profile or root-instance changes over changes to
  the shared `@kitsoki/dev-story`.
- Keep generated proposals under `.artifacts/session-mining/` until an operator
  accepts them.

Discovered sources:

{chr(10).join(source_lines)}

Seed pass defaults:

- trigger: `seed`
- sample: `{sample}`
- first pass sample: `{first_pass}`
- target: `root-instance`
- apply mode: `propose-only`

Discovery note:

{note or "(none)"}
"""


def readme(data: dict, profile_path: str) -> str:
    title = data["project_title"]
    story_id = f"{data['project_id']}-dev"
    docs = dev_story_docs_profile(data)
    commands = []
    if data.get("dev_command"):
        commands.append(("dev", data["dev_command"]))
    if data.get("test_command"):
        commands.append(("test", data["test_command"]))
    if data.get("build_command"):
        commands.append(("build", data["build_command"]))
    command_block = "\n".join(cmd for _, cmd in commands) or "# No project commands were inferred during onboarding."
    command_notes = "\n".join(f"- `{name}`: `{cmd}`" for name, cmd in commands) or "- No project commands were inferred; update `.kitsoki/project-profile.yaml` and this README after choosing them."
    flow_note = (
        "No deterministic flow fixtures are generated for this project instance yet. "
        "Use the imported dev-story fixtures in the Kitsoki checkout for hub coverage, "
        "and add project-local flows when this repo needs its own story-specific assertions."
    )
    mining_note = ""
    if mining_seed_enabled(data):
        mining_note = f"""
Session mining seed:

Kitsoki found associated Claude/Codex transcript history during onboarding and
wrote `{mining_seed_path()}` as an operator review note. Treat it as a proposed
seed pass for project-local customization; no mining pass or LLM call ran during
onboarding.
"""
    return f"""# {story_id}

Kitsoki dev-story instance for the {title} checkout.

Run from the {title} repo root:

```sh
kitsoki run
```

`kitsoki run` starts the profile-driven implicit dev-story root for this
checkout. Use the materialized wrapper explicitly only after editing it:

```sh
kitsoki run .kitsoki/stories/{story_id}/app.yaml
```

Start the browser UI:

```sh
kitsoki web
```

This instance imports `@kitsoki/dev-story` from the Kitsoki binary. The shared
dev-story hub defines the general workflow; this repository owns the local
profile, command defaults, and any project-specific extensions.

Project profile: `{Path(profile_path).relative_to(Path(data["target_path"]))}`

Generated PRDs publish under `{docs["publish_durable_path"]}` and design drafts
publish under `{docs["design_durable_path"]}`. Update
`.kitsoki/project-profile.yaml` and `.kitsoki/stories/{story_id}/app.yaml`
together if this project later adopts a different documentation layout.

Inferred project commands:

```sh
{command_block}
```

Command map:

{command_notes}

Testing:

{flow_note}
{mining_note}
"""


def main() -> int:
    if len(sys.argv) < 9:
        raise SystemExit("usage: init_apply.py target_path project_id project_title stack dev test build conventions tracker")
    data = {
        "target_path": str(Path(sys.argv[1]).expanduser().resolve()),
        "project_id": slug(sys.argv[2]),
        "project_title": sys.argv[3] or sys.argv[2],
        "stack": sys.argv[4],
        "dev_command": sys.argv[5],
        "test_command": sys.argv[6],
        "build_command": sys.argv[7],
        "conventions": sys.argv[8],
        "tracker": sys.argv[9] if len(sys.argv) > 9 else "none",
    }
    draft_profile = None
    if len(sys.argv) > 10 and sys.argv[10].strip():
        draft_profile = json.loads(sys.argv[10])
        commands = draft_profile.get("commands") if isinstance(draft_profile, dict) else {}
        if isinstance(commands, dict):
            data["dev_command"] = commands.get("dev") or data["dev_command"]
            data["test_command"] = commands.get("test") or data["test_command"]
            data["build_command"] = commands.get("build") or data["build_command"]
        if isinstance(draft_profile.get("title"), str):
            data["project_title"] = draft_profile["title"]
    if len(sys.argv) > 11 and sys.argv[11].strip():
        data["mining_recommendation"] = json.loads(sys.argv[11])
    if draft_profile is not None and "mining" not in draft_profile:
        draft_profile["mining"] = data.get("mining_recommendation") or mining_recommendation(Path(data["target_path"]))
    if isinstance(draft_profile, dict):
        dev_story_profile = draft_profile.get("dev_story_profile")
        docs_profile = dev_story_profile.get("docs") if isinstance(dev_story_profile, dict) else None
        if isinstance(docs_profile, dict):
            data["dev_story_docs_profile"] = docs_profile
        ensure_draft_profile_defaults(draft_profile, data)
    root = Path(data["target_path"])
    if not root.exists() or not root.is_dir():
        print(json.dumps({
            "status": "target-invalid",
            "target_path": str(root),
            "error": "target path does not exist" if not root.exists() else "target path is not a directory",
        }, sort_keys=True))
        return 1
    enrich_project_shape(data, root)
    makefile = root / "Makefile"
    if makefile.exists() and not data.get("check_command"):
        try:
            if re.search(r"^check\s*:", makefile.read_text(encoding="utf-8"), re.MULTILINE):
                data["check_command"] = "make check"
        except OSError:
            data["check_command"] = ""
    profile_content = profile_yaml_from_draft(draft_profile) if draft_profile is not None else profile_yaml(data)
    profile_validation = validate_profile_yaml(profile_content, root)
    if not profile_validation["ok"]:
        print(json.dumps({
            "status": "profile-validation-failed",
            "target_path": str(root),
            "profile_validation": profile_validation,
        }, sort_keys=True))
        return 1
    writes: list[str] = []
    docs = dev_story_docs_profile(data)
    dirs = [
        ".kitsoki",
        ".kitsoki/stories",
        ".context",
        docs["publish_durable_path"],
        docs["design_durable_path"],
        ".artifacts",
        ".worktrees",
        f".kitsoki/stories/{data['project_id']}-dev",
    ]
    for rel in dirs:
        (root / rel).mkdir(parents=True, exist_ok=True)

    config_path = root / ".kitsoki.yaml"
    profile_path = root / ".kitsoki" / "project-profile.yaml"
    instance_path = root / ".kitsoki" / "stories" / f"{data['project_id']}-dev" / "app.yaml"
    readme_path = root / ".kitsoki" / "stories" / f"{data['project_id']}-dev" / "README.md"
    gitignore_path = root / ".gitignore"

    write_text(config_path, config_yaml(data["project_id"]), writes)
    write_text(profile_path, profile_content, writes)
    write_text(instance_path, app_yaml(data), writes)
    write_text(readme_path, readme(data, str(profile_path)), writes)
    mining_seed_note = ""
    if mining_seed_enabled(data):
        mining_seed_note = str(root / mining_seed_path())
        write_text(root / mining_seed_path(), mining_seed_markdown(data), writes)
    append_gitignore(gitignore_path, writes)

    print(json.dumps({
        "status": "applied",
        "config_path": str(config_path),
        "profile_path": str(profile_path),
        "instance_path": str(instance_path),
        "gitignore_path": str(gitignore_path),
        "mining_seed_path": mining_seed_note,
        "dirs_created": [str(root / rel) for rel in dirs],
        "profile_validation": profile_validation,
        "writes": writes,
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
