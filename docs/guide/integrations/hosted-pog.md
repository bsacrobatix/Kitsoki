# Host POG behind Kitsoki GitHub login

This runbook deploys the POG portal on the existing `@kitsoki` test VM at
`https://kitsoki-test.slothattax.me`. POG remains its own Node/Vite process;
Kitsoki supplies the invitation-only GitHub login and session store; Caddy
enforces the boundary before any human-readable upstream is reached. The
domain is private as a whole, not merely the new POG routes.

Use the versioned assets under [`deploy/hosted-pog/`](../../../deploy/hosted-pog/)
and the idempotent [`scripts/deploy-hosted-pog.sh`](../../../scripts/deploy-hosted-pog.sh).
Do not hand-edit the live Caddyfile or create an untracked process-manager
configuration as the primary deployment path.

## Topology and security boundary

| Route | Upstream | Authentication |
|---|---|---|
| `/auth/*` | Kitsoki on `127.0.0.1:7777` | Public entry to login, invite, GitHub Device Flow polling, logout, and session probes |
| `/`, POG assets, POG `/api/*`, `/rpc*` | POG Vite on `127.0.0.1:5183` | Kitsoki `/auth/check` through Caddy `forward_auth` |
| `/api/feedback*` | Existing feedback intake on `127.0.0.1:8788` | Same POG login gate |
| `/constructor-studio/decks/*` | Existing deck store | Same POG login gate |
| `/healthz`, `/api/ready`, `/run/*`, `/runs*`, `/api/run/*`, `/api/runs`, `/decks/*` | Existing GitHub-agent health/run/evidence surface | Same invitation-only login gate |
| `/gh-agent/webhook` | GitHub agent on `127.0.0.1:8787` | Browser-session bypass by protocol; every payload remains HMAC-verified |

Caddy removes any client-supplied `X-Kitsoki-Actor`, asks the Kitsoki auth
service to resolve the `kitsoki_session` cookie, and copies only Kitsoki's
resolved GitHub login to the upstream. Anonymous browser page loads receive a
`302` to `/auth/login?next=...`; anonymous API and SSE calls receive `401`
JSON. If the auth service is stopped or unreachable, Caddy returns an upstream
failure and does **not** fall through to POG. A broken login service is
therefore closed, not open.

The only public exceptions are the GitHub login protocol surface and GitHub's signed
webhook receiver. Neither exposes portal, run, health, readiness, feedback, or
evidence content. Do not add an asset, API, deck, diagnostic route, or alternate
hostname outside the authenticated handlers. An invalid or unsigned webhook
must return `401`.

## GitHub App prerequisite

This deployment uses GitHub Device Flow. It preserves the same GitHub identity,
invite redemption, returning-user, admin, and Kitsoki session semantics without
placing a client secret on the VM or registering a callback URL. On first login,
the person copies a one-time code from Kitsoki, opens GitHub, and authorizes the
App; the browser then continues automatically. The resulting GitHub access token
is used once to fetch identity and discarded.

The `bsacrobatix-kitsoki-test` App must have **Enable Device Flow** selected in
its GitHub App settings. The deploy helper probes this prerequisite before it
builds or changes the VM. The App client ID is public configuration and is read
from the generated local profile at
`~/.config/kitsoki/gh-app/bsacrobatix-kitsoki-test/kitsoki.env`. Override that
path with `KITSOKI_HOSTED_POG_GH_APP_PROFILE`, or provide the non-secret value
directly as `KITSOKI_HOSTED_POG_GH_CLIENT_ID`.

Do not add an OAuth client secret, App private key, webhook secret, installation
token, or user PAT to the hosted POG config. Device Flow needs none of them.

## Deploy or upgrade

After both changes are contained in protected `main`, run the deploy helper
from the protected Kitsoki checkout and point it at the protected local POG
checkout:

```sh
export KITSOKI_GH_AGENT_REMOTE=root@206.189.84.218
export KITSOKI_GH_AGENT_PUBLIC_BASE_URL=https://kitsoki-test.slothattax.me
export KITSOKI_HOSTED_POG_ROOT="$HOME/code/POG"

scripts/deploy-hosted-pog.sh          # dry run
scripts/deploy-hosted-pog.sh --yes    # build, activate, and verify
scripts/deploy-hosted-pog.sh --verify # read-only live verification later
```

The dry run also makes a non-authorizing Device Flow prerequisite probe. The
helper refuses either a Kitsoki revision or selected POG revision that is
not contained in its protected `main`. It also refuses a POG revision that
predates the separate `POG_KITSOKI_BROWSER_URL` seam; without that seam the
server could work while browsers were incorrectly sent to their own
`127.0.0.1`. It builds Kitsoki for Linux, creates a Git bundle from POG `main`,
checks the uploaded binary's SHA-256, and invokes the versioned remote
installer. The installer then:

1. installs immutable POG and Kitsoki releases under `/opt/pog/releases/<sha>`
   and `/opt/kitsoki-hosted-pog/releases/<sha>` without replacing the existing
   GitHub agent's `/usr/local/bin/kitsoki`;
2. runs `npm ci` and the POG typecheck/production build before activation;
3. atomically moves `/opt/pog/current` to that release;
4. installs and restarts `kitsoki-pog.service` and `pog-portal.service`;
5. waits for loopback auth (`401` when anonymous) and catalog (`200`) probes;
6. validates the candidate Caddyfile before installing it;
7. reloads Caddy and verifies anonymous denial across POG, feedback, health,
   runs, and evidence while checking agent health over loopback.

If activation fails after either symlink changes, the installer restores the
previous POG and Kitsoki release targets and Caddyfile before returning
non-zero. Failed release directories remain available for diagnosis; they are
never treated as active.

POG's server APIs currently live in the Vite `configureServer` plugin, so a
static `vite preview` deployment would silently omit important routes. The
systemd service intentionally runs the real Vite server and uses the production
build as its pre-activation compile/type gate. Revisit that choice when POG
gains a standalone production HTTP server.

## Invite a person

The login is invitation-only. Mint the link against the same SQLite database
and config as the service, then share the printed one-time link out of band:

```sh
ssh root@206.189.84.218 \
  'runuser -u pog -- \
   /opt/kitsoki-hosted-pog/current/kitsoki daemon invite "Person name" \
     --db /var/lib/kitsoki-pog/sessions.db \
     --config /etc/kitsoki/hosted-pog.yaml \
     --base-url https://kitsoki-test.slothattax.me'
```

The invite binds the first GitHub account that redeems it. Returning users can
then sign in directly. List invite state with the same command plus `--list` in
place of the person's name. Sessions last seven days in this deployment; a
redeploy does not erase users, invites, or sessions because the database lives
under `/var/lib/kitsoki-pog`.

## Operations and verification

Read-only checks:

```sh
scripts/deploy-hosted-pog.sh --verify

ssh root@206.189.84.218 \
  'systemctl is-active kitsoki-gh-agent caddy kitsoki-pog pog-portal; \
   systemctl --no-pager --full status kitsoki-pog pog-portal'

ssh root@206.189.84.218 \
  'journalctl -u kitsoki-pog -u pog-portal -u caddy --since "30 minutes ago" --no-pager'
```

Expected anonymous probes (no session cookie):

```sh
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H 'Accept: text/html' https://kitsoki-test.slothattax.me/ # 302
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/auth/me                 # 401
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/healthz                 # 401
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/api/runs                # 401
curl -sS -o /dev/null -w '%{http_code}\n' -X POST \
  https://kitsoki-test.slothattax.me/auth/github/device/poll # 410
curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST -H 'Content-Type: application/json' --data '{}' \
  https://kitsoki-test.slothattax.me/gh-agent/webhook        # 401
```

The public login page itself returns `200`, and a poll with no matching
HttpOnly attempt cookie returns `410`; these are protocol entrypoints, not
authorization. A GitHub account that is neither the configured admin nor bound
through a live one-time invite receives `403` and cannot obtain a session that
reaches content.
Check operational health from the VM instead of weakening the public policy:

```sh
ssh root@206.189.84.218 'curl -fsS http://127.0.0.1:8787/healthz'
```

After signing in, verify the root portal, `/api/catalog`, a direct routed page,
the feedback widget, and a Constructor Studio deck in a real browser. A root
page alone is not sufficient proof that every protected POG upstream works.

## Rollback and recovery

List deployed revisions and the active target:

```sh
ssh root@206.189.84.218 \
  'readlink -f /opt/pog/current; find /opt/pog/releases -mindepth 1 -maxdepth 1 -type d -printf "%f\n" | sort'
```

To roll back, choose a full commit SHA that is still contained in POG `main`
and redeploy it through the same gate:

```sh
export KITSOKI_HOSTED_POG_REF=<full-pog-commit-sha>
scripts/deploy-hosted-pog.sh --yes
```

Common failures:

- The deploy helper reports Device Flow is disabled: enable it on the existing
  GitHub App; never weaken `auth.mode` or bypass Caddy.
- The deploy helper cannot find a client ID: regenerate the local GitHub App
  profile or set `KITSOKI_HOSTED_POG_GH_CLIENT_ID` to the App's public client
  ID.
- Public root returns `502`: check `kitsoki-pog`; this is the intended
  fail-closed state while auth is unavailable.
- Loopback portal is down but auth is healthy: inspect `pog-portal` logs and
  the immutable release's `portal/package-lock.json`; do not point Caddy at an
  ad hoc process.
- Loopback `/healthz` fails while POG works: the existing GitHub-agent service
  is unhealthy; the public route intentionally returns `401`. Treat that as a
  separate incident and use
  [`github-app-setup.md`](github-app-setup.md).

The deployed checkout is a review/operator surface, not the source authority.
Tracked POG work must still land through POG's protected Capsule/promotion
workflow; `/var/lib/pog` holds hosted feedback and stream artifacts, and the
next immutable release replaces the active source tree.
