# Getting started — install Kitsoki and onboard your project

This guide is for a developer who already has a repository — any language, any
stack — and wants to use Kitsoki there. You do not need a Kitsoki source
checkout: install the binary, run it from your project root, review what it
discovers, and commit the small, auditable setup it writes.

To build or contribute to Kitsoki itself, use
[contributor-setup.md](contributor-setup.md) instead.

> **The 30-second version.** From your project root, with only the `kitsoki`
> binary on PATH:
> ```sh
> kitsoki run                 # → type: onboard .
> ```
> Review the discovered profile, confirm, done. The rest of this page explains
> each step and exactly what it produces.

---

## 1. Install Kitsoki

Download a prebuilt binary from
[GitHub Releases](https://github.com/bsacrobatix/Kitsoki/releases/latest) or the
[Download Kitsoki](https://bsacrobatix.github.io/Kitsoki/download.html) page.
If there is no prebuilt binary for your platform yet, build it from source —
[contributor-setup.md](contributor-setup.md) is the short path.

Put the `kitsoki` binary somewhere on your `PATH`, then check it:

```sh
kitsoki version
```

Kitsoki is a single binary. It embeds the base stories, the web UI, and the
skill/agent toolkit installed during onboarding, so no checkout or cache is
needed at runtime.

## 2. Choose how agent calls run

Kitsoki can run deterministic replay-only flows with no LLM, but normal
interactive use needs an agent backend for the steps that genuinely require
model judgment. By default, Kitsoki auto-selects the first available option:

| Available | Harness | What |
|---|---|---|
| `claude` CLI on `PATH` | `claude` | Uses your existing Claude Code login. |
| `ANTHROPIC_API_KEY` set | `live` | Calls Anthropic directly. |
| Neither | `replay` | Deterministic replay; useful for fixtures, not fresh work. |

You can force a harness when launching:

```sh
kitsoki run --harness claude
kitsoki run --harness live
```

Switching provider/model per session — and adding non-Anthropic backends — is
covered in [harness profiles](architecture/harness-profiles.md).

## 3. Set up GitHub auth for local issue/PR work

Kitsoki can open GitHub issues, open PRs, push branches/artifacts, and comment
from local runs when GitHub auth is available. The fastest local setup requires
GitHub CLI (`gh`) and reuses its browser/PIN login:

```sh
kitsoki gh-agent login
source ~/.config/kitsoki/github.env
```

What you do: approve GitHub CLI auth in the browser, or enter the one-time code
GitHub CLI prints. What Kitsoki does autonomously: run `gh auth login --web`
when needed, read `gh auth token`, write it to a local 0600 env file, and use
it as `GH_TOKEN`/`GITHUB_TOKEN` only for the GitHub actions your flow requests.
Use `gh auth status` to inspect the GitHub CLI account and OAuth scopes.

For tighter repo-limited permissions, use a GitHub App installation token:

```sh
kitsoki gh-agent setup app --name <app-name> --local-only
kitsoki gh-agent setup attach --repo owner/name
kitsoki gh-agent token
source ~/.config/kitsoki/github.env
```

For the App path, what you do: create/install the App, choose the repositories it can access, and
approve GitHub's consent page. What Kitsoki does autonomously: mint a
short-lived installation token, write it to a local 0600 env file, and use it as
`GH_TOKEN`/`GITHUB_TOKEN` only for the GitHub actions your flow requests.

No public URL is required for this local bug/PR path. OAuth uses a localhost
callback, and `--local-only` creates the App without a webhook URL or event
subscriptions. If you later run a hosted `@kitsoki` agent that receives GitHub
webhooks, recreate or update the App with a public webhook URL:

```sh
kitsoki gh-agent setup app --name <app-name> --public-base-url https://agent.example.com
```

The App permission floor is repository metadata read; issues, pull requests,
and contents write; checks read. Repository access is still limited to the repos
you selected during installation.

If you cannot use a GitHub App yet, create a fine-grained PAT scoped only to the
target repositories with the same repository permissions, put it in
`GH_TOKEN` or `GITHUB_TOKEN`, and record it for local Kitsoki commands:

```sh
export GH_TOKEN=<fine-grained-pat>
kitsoki gh-agent token --from-env
source ~/.config/kitsoki/github.env
```

For a hosted `@kitsoki` agent or deeper setup details, see
[GitHub App setup](architecture/github-app-setup.md).

## 4. Onboard your project: `onboard .`

**`onboard .` is THE onboarding.** There is one front door: the dev-story
onboarding pipeline, which **discovers** a project profile, lets you **review**
it, then **applies** everything — a runnable dev-story instance, the studio MCP
registered for your coding agent, and the kitsoki skill/agent toolkit.
Discovery is read-only; nothing is written until you confirm.

```sh
cd ~/code/my-project
kitsoki run
#   > onboard .                 # or: onboard ~/code/my-project
#   > continue                  # review the discovered profile
#   > story packs               # optional: choose a first-run pack
#   > continue (confirm)        # apply: writes config + instance + toolkit + MCP
```

With no app path, `kitsoki run` starts the embedded dev-story root for the
current project; `kitsoki run @kitsoki/dev-story` names it explicitly.
Discovery infers the project id, title, stack, and dev/test/build commands.

To start a team on a focused subset before expanding the catalog, choose a
starter pack from the onboarding review. The default pack for the current
cyber-repo rollout is `cyber-repo`: `setup`, `bugfix`, `pr-refinement`, and
`git-ops`.

You can preselect that same pack in the onboarding request:

```text
onboard ~/code/cyber-repo --pack cyber-repo
```

For one-off custom scopes, `--stories` still works, but the menu and the normal
post-onboarding command use named packs.

Two caveats:

- The interactive run needs an agent backend (§2) for the discovery
  conversation; with none available it falls back to replay mode and errors
  without a `--recording`. Use the headless sequence below instead.
- If the toolkit + MCP install fails, onboarding routes to a loud
  `init_tools_failed` read-out — it will **not** silently report success — from
  which you can retry, or finish later with `kitsoki init` (§6).

Headless equivalent (no TUI), useful for scripting or CI:

```sh
APP=@kitsoki/dev-story
kitsoki session create   --app "$APP" --key local:onboard
kitsoki session continue --app "$APP" --key local:onboard \
    --intent landing_capture --slots '{"request":"onboard /abs/path/to/my-project"}'
kitsoki session continue --app "$APP" --key local:onboard --intent init_discovered
kitsoki session continue --app "$APP" --key local:onboard --intent confirm_init
kitsoki session continue --app "$APP" --key local:onboard --intent init_applied
```

The full pipeline mechanics — rooms, the discovery/apply scripts, the world
keys, the no-LLM flow fixtures — are in
[stories/dev-story-onboarding.md](stories/dev-story-onboarding.md).

## 5. What onboarding writes

A small, **auditable, checked-in** set of files. None of it is generated at
runtime or hidden in a cache — it travels with every clone and collaborator:

| Path | What | Why |
|---|---|---|
| `.kitsoki.yaml` | `story_dirs`, `project_profile`, `root.import: dev-story`, and a disabled `mining:` scope when associated transcripts are found | so `kitsoki run` starts the profile-driven implicit root, `kitsoki web` discovers editable project stories, and transcript mining is ready for explicit opt-in |
| `.kitsoki/project-profile.yaml` | declarative profile (stack, commands, conventions, selected starter pack, focused enabled-story set, repo evidence, onboarding baseline) | the discovered description of your project and the source for the implicit dev-story root |
| `.kitsoki/check-readiness.py` | explicit verifier for the profile's `setup_plan.verifications` | so a human runs build/test/story-load checks after apply — onboarding never surprises the repo |
| `.kitsoki/promote-session-mining.py` | deterministic promotion helper for emitted session-mining recipes | so reviewed mining output can become pending profile customizations |
| `.kitsoki/stories/<id>-dev/app.yaml` (+ README) | a materialized dev-story **instance** that imports `@kitsoki/dev-story` | an editable snapshot for web discovery and project-local story extensions |
| `.mcp.json` | registers the **kitsoki studio MCP** server | so Claude Code / Cursor / any MCP client can drive kitsoki here |
| `.agents/skills/<name>/` · `.agents/agents/<name>.md` | the kitsoki skill + subagent **toolkit** (source of truth, Codex-standard location) | discovered directly by Codex |
| `.claude/skills/<name>` · `.claude/agents/<name>.md` | relative symlinks into `.agents/` | so Claude Code discovers the same toolkit |
| `.gitignore` | appended with kitsoki runtime entries | keeps sessions/artifacts out of git |

The generated instance imports `@kitsoki/dev-story` from the binary's embedded
story library and rebinds its providers to local implementations, so it runs
standalone with only the binary present. Repo metadata is inferred locally:
git checkouts record their default branch and origin remote; non-git
directories get `repo.vcs: none`.

## 6. The toolkit substep: `kitsoki init`

The apply step ends by running one standalone command — the toolkit + MCP
install. `kitsoki init` (an alias for `kitsoki project-tools install`) exposes
that substep directly:

```sh
kitsoki init                        # or: kitsoki init --target <path>
```

It installs the embedded skills/agents and registers the studio MCP — nothing
else. It does **not** discover a profile, write `.kitsoki.yaml`, or materialize
a dev-story instance; run in a repo with no `.kitsoki.yaml`, it prints a pointer
back to `onboard .` so a partial setup never masquerades as a full one.

Use it standalone only when the toolkit + MCP *is* the whole job:

- you just want an MCP client (Claude Code, Cursor, Claude Desktop) in this
  repo to drive kitsoki through the studio tools, with no project profile or
  dev-story instance;
- the repo is already onboarded and you're refreshing the toolkit (e.g. after
  upgrading the binary), or repairing a missing `.mcp.json` flagged by
  `kitsoki doctor`.

The install is idempotent: source trees are refreshed from the binary, our own
symlinks are re-pointed, an existing `.mcp.json` is **merged** (other servers
preserved), and a real file a human placed at a link path is left untouched.
Add `--json` to `kitsoki project-tools install` for a machine-readable report.

## 7. Use Kitsoki after onboarding

**Run your instance.** From the project root:

```sh
kitsoki run                                     # profile-driven implicit dev-story root
kitsoki run .kitsoki/stories/<id>-dev/app.yaml  # materialized snapshot, useful once edited
```

The implicit root reads `.kitsoki/project-profile.yaml`: command gates,
host-interface bindings, PRD/design placement, and ticket policy all come from
that single profile. The materialized instance is still checked in so teams can
extend it deliberately, but the profile is the reusable convention source.

**Story packs.** The profile records the selected pack in `kitsoki.story_pack`
and the enabled adoption scope in `kitsoki.enabled_stories` plus
`onboarding.starter_stories`. This is not a runtime fence: dev-story remains
available. Expand deliberately with:

```sh
kitsoki project-profile story-packs list
kitsoki project-profile story-packs add <pack>
```

Pair each added story with project readiness checks or local flow coverage.

**Ticket source.** When discovery classifies the tracker as GitHub (the
`origin` remote parses to a `github.com` `owner/repo` slug), the generated
instance binds `iface.ticket → host.gh.ticket` pinned on that slug, so
`pick_ticket` / triage / bugfix read and comment the project's **real GitHub
issues** — auth rides your existing `gh auth` or the §3 token. Any other remote
(or `tracker: none`) keeps local-file tickets under `issues/`. See
[hosts.md → host.gh.ticket](architecture/hosts.md#hostghticket--github-issues-backed-tracker).

**Verify readiness.** Onboarding records likely build/test/story-load checks in
the profile but does not run project commands automatically. When ready:

```sh
python3 .kitsoki/check-readiness.py --list
python3 .kitsoki/check-readiness.py --json
python3 .kitsoki/check-readiness.py --json --update-profile   # also persist the summary into the profile
```

The report is written to `.artifacts/kitsoki-readiness.json`. Red project
checks are data, not runtime errors — the story's `readiness` action shows the
failures and returns you to the applied result.

**Session mining (opt-in).** When discovery finds associated Claude/Codex
transcript history, onboarding writes `.context/kitsoki-session-mining-seed.md`
and pre-fills `.kitsoki.yaml` with a disabled `mining:` block (scope, cadence,
first-pass sample). Nothing mines or spends during onboarding; `/mine resume`
or `/mine now` is the explicit opt-in. Emitted recipes are promoted to pending
profile customizations for review — via the story's `customizations` action
from `init_done`, or directly:

```sh
python3 .kitsoki/promote-session-mining.py --dry-run
python3 .kitsoki/promote-session-mining.py --accept-pending --json
python3 .kitsoki/promote-session-mining.py --refine-pending "feedback" --json
```

The profile's `onboarding` block records why the starter pack was selected,
which repo patterns discovery used, and which customizations were applied or
queued — mining proposes there first; an operator accepts. For reusable
onboarding tests, keep `onboarding.baseline_commit` pinned to the commit before
Kitsoki files were introduced so flow/cassette tests can replay from the
baseline with no LLM.

**Drive Kitsoki from your coding agent.** With `.mcp.json` registered, an MCP
client attached to this repo gets the kitsoki **studio** tools —
author/validate/test stories, drive sessions, render the TUI/web — all through
one facade ([architecture/mcp-studio.md](architecture/mcp-studio.md)). The
installed `kitsoki-mcp-driver` agent is purpose-built to orchestrate kitsoki
entirely through that surface:

```sh
claude --agent kitsoki-mcp-driver     # or default it via .claude/settings.json: { "agent": "kitsoki-mcp-driver" }
```

Codex users: see the
[Studio MCP dogfood recipe](recipes/studio-mcp-dogfood.md#run-a-pure-kitsoki-driver)
for the mirrored subagent and headless runbook.

**VS Code.** The kitsoki VS Code extension is expected to discover the
onboarded `.kitsoki/stories/` instance automatically, with the `storiesDir`
setting remaining an explicit override. (This is the documented contract for
the extension; auto-discovery ships with its v0.2 work.)

## 8. Next steps

[`workflows/README.md`](workflows/README.md) covers the core developer
workflows onboarding unlocks — writing a PRD/design, decomposing an epic into
implemented briefs, filing a bug, and fixing one — per surface (TUI, web,
VS Code, gh-agent), with truthful current-state caveats.

| Doc | What |
|---|---|
| [evaluate-kitsoki.md](evaluate-kitsoki.md) | How to decide whether Kitsoki is a fit, and how it compares. |
| [architecture/concept.md](architecture/concept.md) | The control-inversion thesis behind Kitsoki. |
| [stories/architecture.md](stories/architecture.md) | How Kitsoki stories are structured. |
| [stories/dev-story-onboarding.md](stories/dev-story-onboarding.md) | The onboarding pipeline's rooms, scripts, and flow fixtures. |
| [tracing/testing.md](tracing/testing.md) | How to test flows without LLM cost. |
| [contributor-setup.md](contributor-setup.md) | Build Kitsoki from source and set up a checkout for development. |
