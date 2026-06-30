"""Cell execution + the container-backend seam (dependency-injected).

A cell runs in a container. `ContainerBackend` is the seam: `DockerBackend` runs
a real `docker --context <host> run …`; `FakeBackend` records calls and returns
canned results so the whole pipeline is unit-testable with no docker and no LLM.
Placement is just *which host (docker context)* the container lands on — `local`
is the local daemon, `vm-N` a remote VM's daemon (a later phase registers those).
"""

from __future__ import annotations

import subprocess
from dataclasses import dataclass
from typing import Callable, Protocol

from .model import Cell, CellResult
from .plugins import base as plugins


@dataclass
class ContainerRun:
    """Result of running one command in one container."""

    exit_code: int
    stdout: str
    stderr: str
    host: str


class ContainerBackend(Protocol):
    """The execution seam: place+run a container on a host, return its output."""

    def run(self, *, host: str, image: str, argv: list[str], mounts: dict[str, str]) -> ContainerRun:
        ...


class DockerBackend:
    """Runs cells as real containers via `docker --context <host> run`.

    `host` maps to a docker context — `local` to the default daemon, any other
    name to that context (a VM registered as a remote docker host). The VM
    *executes the container*; placement never forks a separate code path.
    """

    def __init__(self, *, context_for: Callable[[str], str | None] | None = None, runner=subprocess.run) -> None:
        # `local` → the operator's ACTIVE context (omit --context, least surprise
        # with Docker Desktop etc.); any other host → its own named context (a VM
        # registered as a remote docker host).
        self._context_for = context_for or (lambda host: None if host == "local" else host)
        self._runner = runner

    def run(self, *, host: str, image: str, argv: list[str], mounts: dict[str, str]) -> ContainerRun:
        context = self._context_for(host)
        cmd = ["docker"]
        if context:
            cmd += ["--context", context]
        cmd += ["run", "--rm"]
        for src, dst in mounts.items():
            cmd += ["-v", f"{src}:{dst}"]
        cmd += [image, *argv]
        proc = self._runner(cmd, capture_output=True, text=True)
        return ContainerRun(
            exit_code=proc.returncode,
            stdout=proc.stdout or "",
            stderr=proc.stderr or "",
            host=host,
        )


class FakeBackend:
    """No-docker backend for tests: returns scripted runs and records every call."""

    def __init__(self, responder: Callable[[Cell, str, list[str]], ContainerRun]) -> None:
        self._responder = responder
        self.calls: list[dict] = []

    def run(self, *, host: str, image: str, argv: list[str], mounts: dict[str, str]) -> ContainerRun:
        self.calls.append({"host": host, "image": image, "argv": argv, "mounts": mounts})
        # The responder is keyed by the cell embedded in argv-free context, so we
        # pass image+argv through; tests close over the cell list.
        return self._responder_run(host, image, argv)

    # Indirection so the responder can be (cell, host, argv) — the executor sets
    # _current_cell right before each run.
    _current_cell: Cell | None = None

    def _responder_run(self, host: str, image: str, argv: list[str]) -> ContainerRun:
        assert self._current_cell is not None, "FakeBackend used outside executor"
        return self._responder(self._current_cell, host, argv)


class CellExecutor:
    """Runs one cell: pick image → drive in container → plugin scores the output."""

    def __init__(self, backend: ContainerBackend, *, mounts_for: Callable[[Cell, str], dict[str, str]] | None = None) -> None:
        self._backend = backend
        # mounts_for is (cell, host) — a remote host's `-v` paths resolve on that
        # host's daemon, so the source side must be the checkout path ON the host.
        self._mounts_for = mounts_for or (lambda cell, host: {})

    def execute(self, cell: Cell, *, host: str = "local", live: bool = False) -> CellResult:
        plugin = plugins.get(cell.job_type)
        image = plugin.image(cell)
        argv = plugin.drive_command(cell, live=live)
        # Let FakeBackend route its responder by the current cell.
        if isinstance(self._backend, FakeBackend):
            self._backend._current_cell = cell
        run = self._backend.run(host=host, image=image, argv=argv, mounts=self._mounts_for(cell, host))
        result = plugin.score(cell, exit_code=run.exit_code, stdout=run.stdout, stderr=run.stderr)
        result.metrics.setdefault("host", run.host)
        return result
