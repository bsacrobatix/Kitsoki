# Host POG behind Kitsoki GitHub login

This runbook deploys the POG portal on the existing `@kitsoki` test VM at
`https://kitsoki-test.slothattax.me`. POG remains its own Node/Vite process;
Kitsoki supplies the invitation-only GitHub login and session store; Caddy
enforces the boundary before any POG-owned upstream is reached.

Use the versioned assets under [`deploy/hosted-pog/`](../../../deploy/hosted-pog/)
and the idempotent [`scripts/deploy-hosted-pog.sh`](../../../scripts/deploy-hosted-pog.sh).
Do not hand-edit the live Caddyfile or create an untracked process-manager
configuration as the primary deployment path.

## Topology and security boundary

| Public route | Upstream | Authentication |
|---|---|---|
| `/auth/*` | Kitsoki on `127.0.0.1:7777` | Public entry to login, invite, callback, logout, and session probes |
| `/`, POG assets, POG `/api/*`, `/rpc*` | POG Vite on `127.0.0.1:5183` | Kitsoki `/auth/check` through Caddy `forward_auth` |
| `/api/feedback*` | Existing feedback intake on `127.0.0.1:8788` | Same POG login gate |
| `/constructor-studio/decks/*` | Existing deck store | Same POG login gate |
| `/gh-agent/webhook`, `/healthz` | GitHub agent on `127.0.0.1:8787` | Public by protocol; webhook payloads remain HMAC-verified |
| `/run/*`, `/runs*`, `/api/run/*`, `/api/runs`, `/decks/*` | Existing GitHub-agent run/evidence surface | Existing behavior is unchanged |

Caddy removes any client-supplied `X-Kitsoki-Actor`, asks the Kitsoki auth
service to resolve the `kitsoki_session` cookie, and copies only Kitsoki's
resolved GitHub login to the upstream. Anonymous browser page loads receive a
`302` to `/auth/login?next=...`; anonymous API and SSE calls receive `401`
JSON. If the auth service is stopped or unreachable, Caddy returns an upstream
failure and does **not** fall through to POG. A broken login service is
therefore closed, not open.

The public exceptions above are deliberate. Do not add a POG asset, API, deck,
health route, or alternate hostname outside the authenticated handlers.

## One-time GitHub App setup

The existing GitHub App can back the login, but its browser OAuth registration
must be configured once by an App owner:

1. Open the `bsacrobatix-kitsoki-test` GitHub App settings.
2. Add this callback URL:

   ```text
   https://kitsoki-test.slothattax.me/auth/github/callback
   ```

3. Generate a new client secret. GitHub shows it once; do not paste it into a
   shell command, chat, repository file, or service unit.
4. On the VM, create `/etc/kitsoki/hosted-pog.env` with mode `0600`. Use an
   interactive editor so the secret does not enter shell history:

   ```sh
   ssh -t root@206.189.84.218 \
     'install -d -m 0755 /etc/kitsoki; umask 077; vi /etc/kitsoki/hosted-pog.env'
   ```

   The file is a systemd environment file, so use plain assignments (no
   `export`):

   ```text
   KITSOKI_GH_APP_CLIENT_ID=<the App client id>
   KITSOKI_GH_APP_CLIENT_SECRET=<the newly generated secret>
   ```

The App client ID is not secret. The existing local profile records it at
`~/.config/kitsoki/gh-app/bsacrobatix-kitsoki-test/kitsoki.env`. The client
secret is separate from the webhook secret, App private key, installation
token, and any user PAT; none of those substitutes for it.

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

The helper refuses either a Kitsoki revision or selected POG revision that is
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
7. reloads Caddy and verifies public fail-closed behavior plus agent health.

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
  'set -a; . /etc/kitsoki/hosted-pog.env; set +a; \
   runuser -u pog --preserve-environment -- \
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

Expected anonymous probes:

```sh
curl -sS -o /dev/null -w '%{http_code}\n' \
  -H 'Accept: text/html' https://kitsoki-test.slothattax.me/ # 302
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/auth/me                 # 401
curl -fsS https://kitsoki-test.slothattax.me/healthz         # ok
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

- `hosted-pog.env is missing` or has no client secret: complete the one-time
  GitHub App setup; never weaken `auth.mode` or bypass Caddy.
- GitHub reports a callback mismatch: make the App callback exactly the URL in
  this runbook, including `/auth/github/callback`.
- Public root returns `502`: check `kitsoki-pog`; this is the intended
  fail-closed state while auth is unavailable.
- Loopback portal is down but auth is healthy: inspect `pog-portal` logs and
  the immutable release's `portal/package-lock.json`; do not point Caddy at an
  ad hoc process.
- `/healthz` fails while POG works: the existing GitHub-agent service is
  unhealthy; treat that as a separate incident and use
  [`github-app-setup.md`](github-app-setup.md).

The deployed checkout is a review/operator surface, not the source authority.
Tracked POG work must still land through POG's protected Capsule/promotion
workflow; `/var/lib/pog` holds hosted feedback and stream artifacts, and the
next immutable release replaces the active source tree.
