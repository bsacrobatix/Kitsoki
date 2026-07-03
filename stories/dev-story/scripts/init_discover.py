#!/usr/bin/env python3
"""Discover a local project profile for dev-story onboarding."""

from __future__ import annotations

import json
import os
import re
import shlex
import sys
from pathlib import Path


def slug(value: str) -> str:
    value = value.strip().lower()
    value = re.sub(r"^@[^/]+/", "", value)
    value = re.sub(r"[^a-z0-9]+", "-", value).strip("-")
    return value or "project"


def title_from_slug(value: str) -> str:
    if value == "slidey":
        return "Slidey"
    return " ".join(part.capitalize() for part in value.replace("_", "-").split("-") if part)


def transcript_slug(path: Path) -> str:
    return str(path).replace("/", "-").replace(".", "-")


def transcript_mtime(path: Path) -> int:
    try:
        return int(path.stat().st_mtime)
    except OSError:
        return 0


def count_jsonl(path: Path) -> tuple[int, int]:
    if not path.is_dir():
        return 0, 0
    count = 0
    latest = 0
    try:
        files = list(path.glob("*.jsonl"))
    except OSError:
        return 0, 0
    for item in files:
        count += 1
        latest = max(latest, transcript_mtime(item))
    return count, latest


def codex_session_refs(home: Path, repo: Path, limit: int = 200) -> tuple[int, int]:
    """Find recent Codex sessions that mention this repo path.

    Codex stores sessions by date, not in a repo-slug directory, so discovery
    uses a bounded recent-file scan. This is evidence for first-run setup, not
    the full mining pass.
    """
    root = home / ".codex" / "sessions"
    if not root.is_dir():
        return 0, 0
    target = str(repo)
    try:
        files = sorted(root.rglob("*.jsonl"), key=transcript_mtime, reverse=True)[:limit]
    except OSError:
        return 0, 0
    count = 0
    latest = 0
    for item in files:
        try:
            text = item.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue
        if target in text:
            count += 1
            latest = max(latest, transcript_mtime(item))
    return count, latest


def transcript_evidence(path: Path) -> dict:
    home_env = os.environ.get("KITSOKI_INIT_HOME", "")
    if home_env:
        home = Path(home_env).expanduser()
    else:
        try:
            home = Path.home()
        except RuntimeError:
            home = Path("")
    slug_value = transcript_slug(path)
    claude_dir = home / ".claude" / "projects" / slug_value
    claude_count, claude_latest = count_jsonl(claude_dir)
    codex_count, codex_latest = codex_session_refs(home, path)
    sources = []
    if claude_count:
        sources.append({
            "backend": "claude-code",
            "dir": str(claude_dir),
            "sessions": claude_count,
            "latest_mtime": claude_latest,
            "include": "human",
        })
    if codex_count:
        sources.append({
            "backend": "codex",
            "dir": str(home / ".codex" / "sessions"),
            "sessions": codex_count,
            "latest_mtime": codex_latest,
            "include": "human",
        })
    total = sum(int(src.get("sessions", 0)) for src in sources)
    recommendation = {
        "status": "transcripts-found" if total else "no-transcripts-found",
        "sample": "recency",
        "first_pass_sample": min(12, total) if total else 0,
        "transcript_count": total,
        "sources": sources,
        "note": (
            "Seed project customization from recent associated transcripts after operator consent."
            if total
            else "No associated Claude/Codex transcripts were found during deterministic discovery."
        ),
    }
    return {
        "slug": slug_value,
        "sources": sources,
        "count": total,
        "latest_mtime": max([0] + [int(src.get("latest_mtime", 0)) for src in sources]),
        "mining": recommendation,
    }


def parse_target(request: str, workdir: str, repo_root: str) -> Path:
    base = Path(repo_root or workdir or os.getcwd()).expanduser()
    text = (request or "").strip()
    target = ""
    if text:
        try:
            parts = shlex.split(text)
        except ValueError:
            parts = text.split()
        lowered = [p.lower() for p in parts]
        prefixes = [
            ["onboard"],
            ["project", "onboarding"],
            ["init", "project"],
            ["set", "up", "kitsoki", "for"],
        ]
        for prefix in prefixes:
            if lowered[: len(prefix)] == prefix:
                rest = parts[len(prefix) :]
                if rest and rest[0].lower() in {"this", "repo", "project"}:
                    rest = rest[1:]
                if rest:
                    target = rest[0]
                break
        if not target and len(parts) == 1:
            target = parts[0]
    if not target:
        return base.resolve()
    path = Path(target).expanduser()
    if not path.is_absolute():
        path = base / path
    return path.resolve()


def read_package(path: Path) -> dict:
    pkg = path / "package.json"
    if not pkg.exists():
        return {}
    try:
        return json.loads(pkg.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def command_from_scripts(package: dict, name: str) -> str:
    scripts = package.get("scripts") if isinstance(package, dict) else {}
    if isinstance(scripts, dict) and scripts.get(name):
        return f"npm run {name}" if name != "test" else "npm test"
    return ""


def make_targets(path: Path) -> set[str]:
    makefile = path / "Makefile"
    if not makefile.exists():
        return set()
    targets: set[str] = set()
    try:
        text = makefile.read_text(encoding="utf-8")
    except OSError:
        return targets
    for line in text.splitlines():
        match = re.match(r"^([A-Za-z0-9_.-]+)\s*:", line)
        if match:
            targets.add(match.group(1))
    return targets


def discover(path: Path) -> dict:
    package = read_package(path)
    package_name = package.get("name") if isinstance(package.get("name"), str) else ""
    project_id = slug(package_name or path.name)
    targets = make_targets(path)
    deps = {}
    for key in ("dependencies", "devDependencies"):
        value = package.get(key)
        if isinstance(value, dict):
            deps.update(value)

    if project_id == "slidey":
        stack = "node/vue/vite/puppeteer declarative deck engine with web, html, pdf, and mp4 outputs"
    elif package:
        stack_bits = ["node"]
        if "vue" in deps:
            stack_bits.append("vue")
        if "puppeteer" in deps:
            stack_bits.append("puppeteer")
        if "vite" in deps:
            stack_bits.append("vite")
        stack = "/".join(stack_bits) + " project"
    elif (path / "go.mod").exists():
        stack = "go project"
    elif (path / "Cargo.toml").exists():
        stack = "rust project"
    else:
        stack = "local project"

    dev_command = command_from_scripts(package, "dev")
    test_command = command_from_scripts(package, "test")
    build_command = command_from_scripts(package, "build")
    if project_id == "slidey" and (path / "src" / "index.js").exists():
        examples = list((path / "examples").glob("*.slidey.json")) if (path / "examples").exists() else []
        example = "examples/hello.slidey.json" if (path / "examples" / "hello.slidey.json").exists() else ""
        if not example and examples:
            example = str(examples[0].relative_to(path))
        if example:
            dev_command = f"node src/index.js {example} --port 5000 --no-open"
    elif (path / "Cargo.toml").exists():
        build_command = "make build" if "build" in targets else "cargo build"
        test_command = "make test" if "test" in targets else "cargo test"
        if "dev" in targets:
            dev_command = "make dev"
        elif "example" in targets:
            dev_command = "make example"
    elif (path / "go.mod").exists():
        # Go has canonical build/test commands, so the deterministic profile
        # should never leave them "(not yet inferred)" — a Makefile target
        # still wins where one exists. (Go has no universal "dev server", so
        # dev_command stays empty unless a make dev/run target is present.)
        build_command = "make build" if "build" in targets else "go build ./..."
        test_command = "make test" if "test" in targets else "go test ./..."
        if "dev" in targets:
            dev_command = "make dev"
        elif "run" in targets:
            dev_command = "make run"

    transcripts = transcript_evidence(path)
    return {
        "target_path": str(path),
        "project_id": project_id,
        "project_title": title_from_slug(project_id),
        "stack": stack,
        "dev_command": dev_command,
        "test_command": test_command,
        "build_command": build_command,
        "conventions": "hybrid" if project_id == "slidey" or (path / "AGENTS.md").exists() or (path / "CLAUDE.md").exists() else "local defaults",
        "tracker": "none",
        "transcript_slug": transcripts["slug"],
        "transcript_count": transcripts["count"],
        "transcript_sources": transcripts["sources"],
        "mining_recommendation": transcripts["mining"],
    }


def main() -> int:
    request = sys.argv[1] if len(sys.argv) > 1 else ""
    workdir = sys.argv[2] if len(sys.argv) > 2 else ""
    repo_root = sys.argv[3] if len(sys.argv) > 3 else ""
    print(json.dumps(discover(parse_target(request, workdir, repo_root)), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
