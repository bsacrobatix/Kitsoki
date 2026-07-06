# Getting Started — use Kitsoki in your project

This guide is for a developer who already has a repository and wants to try
Kitsoki there. You do not need a Kitsoki source checkout. Install the binary,
run it from your project root, review what it discovers, and commit the small
Kitsoki setup it writes.

If you want to build or contribute to Kitsoki itself, use
[contributor-setup.md](contributor-setup.md) instead.

---

## 1. Install Kitsoki

Download a prebuilt binary from
[GitHub Releases](https://github.com/bsacrobatix/Kitsoki/releases/latest) or the
[Download Kitsoki](https://bsacrobatix.github.io/Kitsoki/download.html) page.

Put the `kitsoki` binary somewhere on your `PATH`, then check it:

```sh
kitsoki version
```

Kitsoki is a single binary. It embeds the base stories, the web UI, and the
skill/agent toolkit used during onboarding.

## 2. Choose how agent calls run

Kitsoki can run deterministic replay-only flows with no LLM, but normal
interactive use needs an agent backend for the steps that genuinely require
model judgment.

By default, Kitsoki auto-selects the first available option:

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

Provider/model switching for an onboarded project is covered in
[harness profiles](architecture/harness-profiles.md).

## 3. Set up GitHub auth for local issue/PR work

Kitsoki can open GitHub issues, open PRs, push branches/artifacts, and comment
from local runs when GitHub auth is available. The preferred setup is a
least-privilege GitHub App installation token, not a broad personal token:

```sh
kitsoki gh-agent setup app --name <app-name> --local-only
kitsoki gh-agent setup attach --repo owner/name
kitsoki gh-agent token
source ~/.config/kitsoki/github.env
```

What you do: create/install the App, choose the repositories it can access, and
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

## 4. Onboard your project

Run Kitsoki from the repository you want to use it in:

```sh
cd ~/code/my-project
kitsoki run
```

The first useful action is project onboarding:

```text
onboard .
```

Kitsoki discovers the project profile, shows it for review, and waits before it
writes anything. Continue only after the profile looks reasonable.

The normal path is:

```text
onboard .          # discover this repo
continue           # review the discovered profile
continue           # confirm apply
```

For the detailed contract behind this flow, see
[project-onboarding.md](project-onboarding.md).

## 5. Review what onboarding writes

Onboarding writes an auditable, checked-in Kitsoki setup into your repo:

| Path | Why it exists |
|---|---|
| `.kitsoki.yaml` | Makes `kitsoki run` start your project-local Kitsoki root. |
| `.kitsoki/project-profile.yaml` | Records stack, commands, repo conventions, selected starter story, and onboarding evidence. |
| `.kitsoki/stories/<id>-dev/app.yaml` | Your editable dev-story instance. |
| `.kitsoki/check-readiness.py` | Runs the build/test/story-load checks Kitsoki inferred. |
| `.mcp.json` | Registers the Kitsoki studio MCP server for coding agents. |
| `.agents/` and `.claude/` | Installs the Kitsoki skills and driver agents. |

Nothing is hidden in a global cache. The setup is meant to be reviewed, edited,
and committed with the rest of your project.

## 6. Verify readiness

Onboarding records likely project checks, but it does not run arbitrary project
commands automatically. Run the readiness verifier when you are ready:

```sh
python3 .kitsoki/check-readiness.py --list
python3 .kitsoki/check-readiness.py --json
```

The report is written to `.artifacts/kitsoki-readiness.json`. To also persist a
summary into `.kitsoki/project-profile.yaml`, run:

```sh
python3 .kitsoki/check-readiness.py --json --update-profile
```

## 7. Use Kitsoki after onboarding

From then on, run Kitsoki from the project root:

```sh
kitsoki run
```

That starts the project-local root described by `.kitsoki/project-profile.yaml`.
If you want to run the materialized story directly:

```sh
kitsoki run .kitsoki/stories/<id>-dev/app.yaml
```

## 8. Drive it from your coding agent

Onboarding registers the Kitsoki studio MCP server in `.mcp.json`. Attach your
coding agent to this project and it can use Kitsoki as a control plane for
authoring, validating, flow-testing, driving, and observing stories.

Claude Code can use the repo-local MCP config and the installed driver agent:

```sh
claude --agent kitsoki-mcp-driver
```

Codex can use its MCP config or the mirrored driver agent installed by the
toolkit. The full runbook is in
[Studio MCP dogfood recipe](recipes/studio-mcp-dogfood.md#run-a-pure-kitsoki-driver).

## 9. Learn the model

Once your project is onboarded, these are the next useful reads:

| Doc | What |
|---|---|
| [project-onboarding.md](project-onboarding.md) | Exact onboarding files, rooms, failure behavior, and headless commands. |
| [evaluate-kitsoki.md](evaluate-kitsoki.md) | How to decide whether Kitsoki is a fit. |
| [architecture/concept.md](architecture/concept.md) | The control-inversion thesis behind Kitsoki. |
| [stories/architecture.md](stories/architecture.md) | How Kitsoki stories are structured. |
| [tracing/testing.md](tracing/testing.md) | How to test flows without LLM cost. |
| [contributor-setup.md](contributor-setup.md) | Build Kitsoki from source and set up this checkout for development. |
