# GitHub App setup — the `@kitsoki` agent

This runbook stands up the GitHub App that authenticates the `@kitsoki` agent
against **live** GitHub. The auth seam lives in
[`internal/ghagent/githubapp`](../../internal/ghagent/githubapp/githubapp.go):
it mints a short-lived **App JWT** (RS256, stdlib crypto), exchanges it for an
**installation access token**, and exports that token as `GH_TOKEN` so the
existing `gh`/`git` CLI path (the `host.cliExec` seam) authenticates as the
installation — no per-user PATs.

Test install: the **`bsacrobatix`** org. Production is identical under
**`constructorfabric`** later. See
[`docs/proposals/kitsoki-github-agent.md`](../proposals/kitsoki-github-agent.md)
shared decision #1 for the auth/permissions decision.

> Round 1 is **poll mode**: it needs no webhook URL and no event
> subscriptions. The webhook fields below are for round 2 and can be left blank
> now.

## a. Create the App (under `bsacrobatix`)

`bsacrobatix` org → **Settings → Developer settings → GitHub Apps → New GitHub
App**.

- **GitHub App name:** `kitsoki` (or `kitsoki-test`).
- **Homepage URL:** any (e.g. your repo URL).
- **Repository permissions** (the floor — shared decision #1):
  - **Issues:** Read & write
  - **Pull requests:** Read & write
  - **Contents:** Read & write  *(rebase/push)*
  - **Checks:** Read-only
- **Subscribe to events** *(round 2 webhook only — skip for poll mode)*:
  `Issues`, `Issue comment`, `Pull request`, `Pull request review comment`,
  `Check suite`.

## b. Webhook (round 2 only)

- **Webhook URL:** `https://<your-domain>/gh-agent/webhook`
- **Webhook secret:** generate one and save it; it becomes
  `KITSOKI_GH_WEBHOOK_SECRET`. Payloads are HMAC-verified by
  `githubapp.VerifyWebhookSignature` against `X-Hub-Signature-256`.

For **poll mode (round 1)** leave the webhook URL blank and `Active` unchecked.

## c. Private key + env

After creating the App:

1. Note the **App ID** (top of the App's settings page).
2. **Generate a private key** → downloads a `.pem`. Store it outside the repo,
   readable only by you:

   ```
   mkdir -p ~/.config/kitsoki
   mv ~/Downloads/kitsoki.*.private-key.pem ~/.config/kitsoki/gh-app.pem
   chmod 600 ~/.config/kitsoki/gh-app.pem
   ```

3. Export the env (the key is referenced by **file path**, never inlined):

   ```
   export KITSOKI_GH_APP_ID=<app-id>
   export KITSOKI_GH_APP_INSTALLATION_ID=<installation-id>   # from step d
   export KITSOKI_GH_APP_PRIVATE_KEY_FILE=~/.config/kitsoki/gh-app.pem
   export KITSOKI_GH_WEBHOOK_SECRET=<secret>                 # round 2 only
   ```

## d. Install on a test repo + find the Installation ID

1. App settings → **Install App** → install on `bsacrobatix`, scoped to a
   throwaway test repo (e.g. `bsacrobatix/kitsoki-sandbox`).
2. The **Installation ID** is the trailing number in the post-install URL:
   `https://github.com/organizations/bsacrobatix/settings/installations/<INSTALLATION_ID>`.

   Or list installations with the App JWT:

   ```
   gh api -H "Accept: application/vnd.github+json" /app/installations
   # → each object's "id" is an installation id
   ```

## e. Run the live poll loop

Flags override the env (`--gh-app-id`, `--gh-app-installation-id`,
`--gh-app-key-file`); `--github-app` forces App auth on.

```
go run ./cmd/kitsoki gh-agent poll \
  --repo bsacrobatix/kitsoki-sandbox \
  --github-app
```

This mints an installation token, sets `GH_TOKEN`, lists `@kitsoki` mentions
via `gh`, and dispatches each to its mapped story. Seed the loop by opening an
issue in the test repo labelled `bug` whose body mentions **`@kitsoki`** (e.g.
"`@kitsoki` the foo button crashes on click"); the next poll picks it up.

## f. Production

Identical under the **`constructorfabric`** org: create/install the same App
there, point the env at that App's ID / installation / key, and run with the
production `--repo`.
