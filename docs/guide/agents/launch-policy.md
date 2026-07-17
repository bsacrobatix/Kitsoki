# Agent Launch Policy

`agent_launch_policy:` is a machine-local preflight guard for external coding
agent launches. It rejects unsafe working directories before Kitsoki forks
`claude`, `codex`, `copilot`, `agy`, or an agent plugin path.

The policy is not a filesystem sandbox. It is the first auditable boundary:
protect the operator-owned checkout, keep delegated agents in prepared
workspaces, and record the launch decision. `sandbox:` runtime policy can still
add subprocess supervision for calls that opt into it.

## Configuration

Put machine-specific policy in `.kitsoki.local.yaml` so every operator can map
their own workspace layout without breaking the shared checkout:

```yaml
agent_launch_policy:
  enabled: true
  require_capsule: true

  # Defaults to the directory containing .kitsoki.yaml when omitted.
  protected_roots:
    - .

  # Explicit workspaces agents may use. These are also carve-outs from
  # protected_roots, so capsules under .worktrees can be allowed while the
  # primary checkout stays protected.
  allowed_roots:
    - ./.worktrees/capsules
    - /tmp/kitsoki-capsules

  # Defaults when omitted: main, master, trunk, integration/*, staging/*.
  protected_branches:
    - main
    - master
    - trunk
    - integration/*
    - staging/*
```

`protected_roots` and `allowed_roots` are resolved relative to the config file
and normalized to absolute paths at load time. `protected_branches` are git
branch patterns, not paths.

## Semantics

When enabled, a launch is allowed only when all checks pass:

1. The resolved `working_dir` exists and is a directory.
2. If `allowed_roots` is non-empty, `working_dir` must be inside one of them.
3. `working_dir` and its git root must not be inside `protected_roots`, unless
   an explicit `allowed_roots` entry carves that workspace out.
4. A non-capsule git checkout must not be on a protected branch.
5. If `require_capsule` is true, `working_dir` must be inside an opened Kitsoki
   capsule, identified by `.kitsoki-capsule` plus `capsule-manifest.json`.

Opened capsules may contain normal fixture branches such as `main`. The branch
guard is intended to protect real worktrees and integration branches, not the
throwaway git repositories inside capsule workspaces.

Use [`kitsoki capsule open`](../development/capsules.md) to create an opened capsule
workspace, then pass that path as the agent `working_dir`.

## Placement Policy (Federation)

`agent_launch_policy.placement:` is the launch-preflight gate for **remote**
dispatch, layered on top of the local `working_dir` checks above. It is part
of the standing-autonomy proposal's federation work (§9 "Federation", ask 4:
unified worker registry + placement policy) and enforces invariant **SA-I7**:
remote execution happens only through sealed envelopes against registered,
enabled workers whose advertised capabilities satisfy the lane's policy;
placement violations fail at launch preflight, not at runtime.

```yaml
agent_launch_policy:
  enabled: true
  placement:
    research:
      worker_classes: [thin, workstation, local-model]
      network_profiles: [research-egress]
    build:
      worker_classes: [thin, workstation]
      network_profiles: [git-mirror, package-mirror]
    fix:
      worker_classes: [thin, workstation]
      network_profiles: [git-mirror, package-mirror]
    # delivery (queue worker, protected-main CAS) is intentionally absent:
    # a lane with no entry in `placement:` cannot be pinned to any remote
    # worker at all once placement is configured (see below).
```

Semantics:

- `placement:` is a map of **lane name -> `{worker_classes, network_profiles}`**.
  Lane names are caller-defined strings (e.g. a Capsule CI pipeline name, or
  `--lane` on `kitsoki capsule ci run --worker <id>`).
- An **absent or empty `placement:` map** (every policy configured before
  this field existed, and any policy that simply omits it) means placement is
  **not enforced** — every existing `Check(...)` call site keeps working
  exactly as before. This is the backward-compatibility contract.
- Once `placement:` is non-empty, a lane **not listed** in the map is denied
  for any remote-worker pin — this is how the proposal's delivery lane
  (queue worker, protected-main CAS) stays local-only: never add it here.
- `worker_classes` names one of the worker registry's placement classes
  (`thin`, `workstation`, `local-model` — see
  [worker-registry.md](../development/worker-registry.md)). Empty means no
  class restriction for that lane.
- `network_profiles` names the permitted network profile(s) for that lane's
  dispatch. Empty means no network restriction for that lane.

Enforcement lives in `AgentLaunchPolicy.CheckPlacement` in
`internal/host/agent_launch_policy.go`, a companion to `Check` (not a
replacement — `Check` still governs the local working-directory/branch/capsule
preflight). It is wired into `kitsoki capsule ci run --worker <id>` today
(see worker-registry.md's CLI section); other remote-dispatch call sites can
adopt it the same way as federation work continues.

**This is a separate, earlier gate than `executor.ValidateCapabilities`**
(see [worker-registry.md](../development/worker-registry.md)), the
sealed-envelope `Policy`-vs-`Capabilities` check in
`internal/capsule/executor`: `CheckPlacement` runs at launch preflight against
the worker's advertised *class*, before any sealed envelope exists;
`ValidateCapabilities` remains untouched as the runtime enforcement layer that
follows it. A lane can never silently escalate isolation or egress by only
satisfying one gate.

On denial, `CheckPlacement` returns a deterministic error. Two exact forms:

```
agent launch policy denied: lane "delivery" has no placement policy entry and placement is configured, so it may not target any remote worker
```

```
agent launch policy denied: lane "build" targets worker "vm-b" (class "workstation") which does not satisfy placement policy (allowed classes: [thin])
```

```
agent launch policy denied: lane "build" targets worker "vm-b" with network profile "open" which does not satisfy placement policy (allowed network profiles: [git-mirror])
```

## Enforcement Surface

The same policy is installed for:

- `host.agent.task`, before subprocess or plugin dispatch;
- `host.agent.converse`, before subprocess or plugin dispatch;
- `host.agent.codeact`, before the codeact runner starts;
- sessions created by `kitsoki run`, `kitsoki web`, and `kitsoki mcp`;
- `kitsoki agent launch`, including dry-run planning and raw interactive
  launches.

For host calls, allowed and denied decisions emit an `agent.launch.policy` event
when a trace/event sink is attached. For `kitsoki agent launch`, an allowed
decision appears in the dry-run JSON plan as `launch_policy`; denied launches
return an error before a command plan is emitted.

Decision records include the verb, agent name, working directory, git root,
branch, matching protected root/branch, capsule name/root/spec path, and the
effective policy lists. They never include provider secrets.

## Raw Interactive Launches

Use `kitsoki agent launch --raw --interactive` to start a normal interactive
backend CLI without an app, agent file, MCP wrapper, or Kitsoki replacement
system prompt:

```sh
kitsoki agent launch --raw --interactive --backend codex --working-dir /tmp/kitsoki-capsules/clean-repo
```

This path is useful on macOS, where operators often have Claude Code or Codex
subscription auth in the native host CLI. It still runs the launch policy
preflight first, so a raw session cannot accidentally start in the protected
main checkout when policy is enabled.

Raw interactive launch supports `codex` and `claude` backends. Harness profiles
may still supply backend, model, effort, and environment retargeting; they do
not supply any app or agent prompt.

## Delegated macOS User Setup

`run_as_user` delegation is currently disabled at runtime. The setup story and
config shape remain documented here for later re-enablement, but Kitsoki now
parses existing `agent_user_delegation:` blocks without using the wrappers,
without recording `run_as_user` in launch plans, and without surfacing the
macOS setup warning.

On macOS, `agent_launch_policy:` should be paired with a separate Standard user
for coding-agent backends. The policy rejects unsafe launch locations, but the
OS user boundary is what makes the protected checkout unwritable after the
backend starts.

Run the setup story for the guided no-LLM setup flow:

```sh
kitsoki run @kitsoki/run-as-user-setup
```

The story can show the generated `.kitsoki.local.yaml` blocks, root-owned
backend wrappers, sudoers snippet, capsule-assignment commands, and validation
probes before applying anything. When the operator chooses `apply`, it uses
non-interactive `sudo -n` to create or reuse the local account/group, install
the wrappers and sudoers file, set up the sample capsule permissions, and run
the delegated write/write-deny probes. If macOS needs a password, the story
stops in an authorization screen and asks the operator to run `sudo -v`, then
retry.

The local receipt block is:

```yaml
agent_user_delegation:
  enabled: true
  run_as_user: kitsoki-agent
  wrapper_bin: /Users/Shared/kitsoki/agent-bin
  capsule_root: /Users/Shared/kitsoki/capsules
```

This block is a local receipt for the OS-user delegation setup. While runtime
delegation is disabled, it is only parsed and path-resolved; it does not affect
launch binaries, launch plans, TUI startup notices, or web setup warnings.

When runtime delegation is re-enabled, live agent surfaces that do not consume
`agent_user_delegation.wrapper_bin` directly will still need the wrapper
directory first in `PATH`:

```sh
PATH=/Users/Shared/kitsoki/agent-bin:$PATH kitsoki run @kitsoki/dev-story
```

## Relationship To Sandboxing

`agent_launch_policy:` is fail-fast placement control. It answers "may this
agent start here?" before the backend runs. It does not constrain the child
process after launch.

`with.sandbox` on a host call is runtime control. Today the open-source
`supervised` backend records requested repo/rw/hidden/network policy, uses a
temporary HOME/XDG, controls process lifetime, and captures final diff evidence.
It records degradation when stronger filesystem/network controls are requested
but unavailable.

Together they provide the first practical step toward full sandboxing:

- policy prevents obvious unsafe launch locations;
- capsules provide reproducible, disposable workspaces;
- runtime events prove what was requested and what was actually applied;
- future macOS local-user, Linux namespace, Docker, VM, or SSH backends can
  enforce stronger confinement without changing story authoring vocabulary.
