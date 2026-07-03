#!/usr/bin/env python3
"""Discover a local project profile for dev-story onboarding."""

from __future__ import annotations

import json
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from init_transcripts import transcript_evidence, transcript_slug  # noqa: E402


def slug(value: str) -> str:
    value = value.strip().lower()
    value = re.sub(r"^@[^/]+/", "", value)
    value = re.sub(r"[^a-z0-9]+", "-", value).strip("-")
    return value or "project"


def title_from_slug(value: str) -> str:
    if value == "slidey":
        return "Slidey"
    return " ".join(part.capitalize() for part in value.replace("_", "-").split("-") if part)


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


def read_pyproject(path: Path) -> dict:
    pyproject = path / "pyproject.toml"
    if not pyproject.exists():
        return {}
    try:
        text = pyproject.read_text(encoding="utf-8")
    except OSError:
        return {}
    name = ""
    in_project = False
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            in_project = stripped == "[project]"
            continue
        if in_project:
            match = re.match(r"name\s*=\s*[\"']([^\"']+)[\"']", stripped)
            if match:
                name = match.group(1)
                break
    return {"name": name, "text": text}


def command_from_scripts(package: dict, name: str) -> str:
    return command_from_scripts_with_manager(package, name, "npm")


def command_from_scripts_with_manager(package: dict, name: str, manager: str) -> str:
    scripts = package.get("scripts") if isinstance(package, dict) else {}
    if isinstance(scripts, dict) and scripts.get(name):
        if manager == "npm":
            return f"npm run {name}" if name != "test" else "npm test"
        if manager == "pnpm":
            return f"pnpm run {name}" if name != "test" else "pnpm test"
        if manager == "yarn":
            return f"yarn {name}"
        if manager == "bun":
            return f"bun run {name}"
        return f"{manager} run {name}"
    return ""


def node_package_manager(path: Path, package: dict) -> str:
    package_manager = package.get("packageManager") if isinstance(package.get("packageManager"), str) else ""
    if package_manager:
        name = package_manager.split("@", 1)[0].strip().lower()
        if name in {"npm", "pnpm", "yarn", "bun"}:
            return name
    if (path / "pnpm-lock.yaml").exists():
        return "pnpm"
    if (path / "yarn.lock").exists():
        return "yarn"
    if (path / "bun.lock").exists() or (path / "bun.lockb").exists():
        return "bun"
    return "npm"


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


def discover(path: Path) -> dict:
    package = read_package(path)
    package_name = package.get("name") if isinstance(package.get("name"), str) else ""
    pyproject = read_pyproject(path)
    pyproject_name = pyproject.get("name") if isinstance(pyproject.get("name"), str) else ""
    project_id = slug(package_name or pyproject_name or path.name)
    node_manager = node_package_manager(path, package) if package else ""
    targets = make_targets(path)
    deps = {}
    for key in ("dependencies", "devDependencies"):
        value = package.get(key)
        if isinstance(value, dict):
            deps.update(value)
    py_text = (pyproject.get("text") or "").lower()
    requirements = path / "requirements.txt"
    if requirements.exists():
        try:
            py_text += "\n" + requirements.read_text(encoding="utf-8").lower()
        except OSError:
            pass
    python_project = any(
        (path / marker).exists()
        for marker in ("pyproject.toml", "requirements.txt", "setup.py", "setup.cfg", "Pipfile", "poetry.lock", "uv.lock")
    )

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
    elif python_project:
        stack_bits = ["python"]
        for name in ("django", "fastapi", "flask"):
            if name in py_text:
                stack_bits.append(name)
        stack = "/".join(stack_bits) + " project"
    else:
        stack = "local project"

    dev_command = command_from_scripts_with_manager(package, "dev", node_manager or "npm")
    test_command = command_from_scripts_with_manager(package, "test", node_manager or "npm")
    build_command = command_from_scripts_with_manager(package, "build", node_manager or "npm")
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
    elif python_project:
        build_command = "make build" if "build" in targets else ""
        if "test" in targets:
            test_command = "make test"
        elif (path / "tox.ini").exists():
            test_command = "tox"
        elif (path / "pytest.ini").exists() or (path / "tests").exists() or "pytest" in py_text:
            test_command = "python -m pytest"
        if "dev" in targets:
            dev_command = "make dev"
        elif "run" in targets:
            dev_command = "make run"
        elif "fastapi" in py_text or "uvicorn" in py_text:
            dev_command = "uvicorn app:app --reload"
        elif "flask" in py_text:
            dev_command = "flask run"

    transcripts = transcript_evidence(path)
    repo = git_info(path)
    return {
        "target_path": str(path),
        "project_id": project_id,
        "project_title": title_from_slug(project_id),
        "stack": stack,
        "dev_command": dev_command,
        "test_command": test_command,
        "build_command": build_command,
        "node_package_manager": node_manager,
        "repo_vcs": repo["vcs"],
        "repo_default_branch": repo["default_branch"],
        "repo_remote": repo["remote"],
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
