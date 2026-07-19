# Host POG behind Kitsoki GitHub login

This runbook deploys the POG portal on the existing `@kitsoki` test VM at
`https://kitsoki-test.slothattax.me`. The built POG Node service owns
`127.0.0.1:7777`; Kitsoki supplies invitation-only GitHub login, RPC, and the
session store on `127.0.0.1:7778`; Caddy enforces the boundary before any
human-readable upstream is reached. Vite is a build dependency only and must
never run on the VM. The domain is private as a whole, not merely the new POG
routes.

The hosted product selector is intentionally limited to two products:
`pog` and `constructor-studio`. POG's other repository tracks remain visible
as program/dependency objects in the POG graph, but their sibling catalogs are
not mounted as additional hosted products. The installer verifies this exact
product set from the live `/api/catalog` response before activation succeeds.

Use the versioned assets under [`deploy/hosted-pog/`](../../../deploy/hosted-pog/)
and the idempotent [`scripts/deploy-hosted-pog.sh`](../../../scripts/deploy-hosted-pog.sh).
Do not hand-edit the live Caddyfile or create an untracked process-manager
configuration as the primary deployment path.

## Topology and security boundary

| Route | Upstream | Authentication |
|---|---|---|
| `/auth/*` | Kitsoki on `127.0.0.1:7778` | Public entry to login, invite, the GitHub OAuth redirect/callback, logout, and session probes |
| `/`, built POG assets, POG `/api/*`, `/rpc*` | Production POG on `127.0.0.1:7777` | Kitsoki `/auth/check` through Caddy `forward_auth` |
| Exact `/api/feedback` | Existing hosted feedback intake on `127.0.0.1:8788` | Same POG login gate |
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

The feedback-intake matcher is deliberately exact. `/api/feedback-reports`
and `/api/feedback-autonomy/*` are POG-owned production APIs; routing them with
an `/api/feedback*` wildcard sends them to the intake service and makes durable
POG state appear to disappear. The asset test and live verifier both retain a
tripwire for that boundary.

## GitHub App prerequisite

This deployment uses the normal GitHub OAuth authorization-code (callback)
flow: clicking **Sign in with GitHub** redirects to GitHub's authorize page and
GitHub redirects straight back, signed in. Identity, invite redemption,
returning-user, admin, and Kitsoki session semantics are unchanged from the
earlier Device Flow configuration. The resulting GitHub access token is used
once to fetch identity and discarded.

The `bsacrobatix-kitsoki-test` App must register
`https://kitsoki-test.slothattax.me/auth/github/callback` as a callback URL and
have a generated client secret. The App client ID is public configuration and
is read from the generated local profile at
`~/.config/kitsoki/gh-app/bsacrobatix-kitsoki-test/kitsoki.env`. Override that
path with `KITSOKI_HOSTED_POG_GH_APP_PROFILE`, or provide the non-secret value
directly as `KITSOKI_HOSTED_POG_GH_CLIENT_ID`.

The client secret is supplied to the deploy helper as
`KITSOKI_HOSTED_POG_GH_CLIENT_SECRET` (or the operator's conventional
`GH_KITSOKI_TEST_CLIENT_SECRET`), or as a `KITSOKI_GH_APP_CLIENT_SECRET` line
in the local App profile. The helper validates the credential pair against
GitHub before touching the VM, ships the secret inside the 0700 staged upload
(never on the ssh command line), and installs it as root-only
`/etc/kitsoki/hosted-pog.env`; systemd injects it into the service, so the
`pog` user never reads the file and the rendered YAML stays secret-free via a
`${KITSOKI_HOSTED_POG_GH_CLIENT_SECRET}` reference.

Do not add an App private key, webhook secret, installation token, or user PAT
to the hosted POG config. Callback login needs none of them.

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

To seed the host with the state currently visible in the local POG portal, add
the explicit state-sync flag:

```sh
scripts/deploy-hosted-pog.sh --yes --sync-local-state
```

The bounded snapshot includes every local state root the portal consumes:
feedback and its evidence/lifecycle records, graph-MCP feedback, streams,
colony telemetry/research/supervisor records, and a consistent SQLite backup
of runner sessions. It deliberately excludes the rest of `.artifacts`, such
as caches, compiled binaries, render scratch, temporary workspaces, logs, and
local runner PIDs. Those files can be several gigabytes, are not product state,
and in the PID case are actively unsafe to copy to another machine.

State sync is conflict-closed. On a repeat sync, the installer first hashes the
entire active runtime against its prior import manifest. The SQLite shared-memory
sidecar and an empty WAL are treated as transient; a non-empty WAL is a hosted
change and closes the update path. If the runtime is still exactly the imported
snapshot, the new snapshot can safely replace it because no hosted-only work
exists to lose. Otherwise, only missing or byte-identical files are accepted. A
divergent same-path file stops the install before any active symlink moves and
prints every conflict. A normal `--yes` deploy preserves the active hosted
runtime without importing local state.

The dry run also makes a non-authorizing OAuth credential preflight: it
exchanges a deliberately bogus code and requires GitHub to answer
`bad_verification_code` (valid client ID and secret); a wrong secret answers
`incorrect_client_credentials` and stops the deploy before the VM changes. The
helper refuses either a Kitsoki revision or selected POG revision that is
not contained in its protected `main`. It also refuses a POG revision without
the bundled production-server contract. It builds Kitsoki for Linux, creates a Git bundle from POG `main`,
checks the uploaded binary's SHA-256, downloads and locally verifies the pinned
Node archive, and invokes the versioned remote installer. The installer then:

1. verifies the uploaded Node.js linux-x64 archive again against the official
   SHA-256 declared in `deploy/hosted-pog/node-runtime.env` and installs it
   under `/opt/kitsoki-hosted-pog/node/<version>`; both the POG build and
   service use this runtime rather than the VM's ambient Node;
2. installs immutable POG and Kitsoki releases under `/opt/pog/releases/<sha>`
   and `/opt/kitsoki-hosted-pog/releases/<sha>` without replacing the existing
   GitHub agent's `/usr/local/bin/kitsoki`;
3. runs `npm ci`, the POG typecheck/client build, and the self-contained
   production-server build before activation; the server artifact receives
   the explicit `pog,constructor-studio` allowlist, while browser links are
   built same-origin and only the systemd runtime receives loopback upstreams;
4. preserves `/var/lib/pog/runtime`, or, with `--sync-local-state`, verifies the
   uploaded snapshot checksum and manifest, builds a conflict-free versioned
   runtime, and atomically moves the runtime symlink;
5. binds the release's ignored `.artifacts` path to that versioned runtime so
   every portal state reader uses the same durable data;
6. removes historical sibling discovery, enforces the explicit hosted member
   allowlist, and verifies the live products are exactly POG and Constructor Studio;
7. atomically moves the POG, Kitsoki, and Node `current` symlinks to their
   verified releases;
8. installs and restarts Kitsoki auth/RPC on 7778 and the built POG production
   service on 7777;
9. verifies revision-bound production health, the exact catalog, reviewed
   feedback ownership, the production command line, and absence of a 5183
   listener before changing Caddy;
10. validates the candidate Caddyfile before installing it;
11. reloads Caddy and verifies anonymous denial across POG, feedback, health,
   runs, and evidence while checking agent health over loopback.

If activation fails after a symlink changes, the installer restores the
previous POG, Kitsoki, Node, and runtime targets, both prior systemd units, and
the Caddyfile before returning non-zero. This matters for the one-time
5183-to-7777 cutover: a failed first activation can still restart the previous
Vite-backed release while it rolls back. Failed release/state directories
remain available for diagnosis; they are never treated as active.

POG's production server reuses the same registered portal API middleware as
development, then serves only the built `dist/` assets and narrow same-origin
proxies. Do not replace it with `vite preview`: preview omits the operational
APIs. Do not replace it with `npm run dev`: that reintroduces a development
server, watcher, HMR machinery, and a 5183 dependency. The deployed command
must contain `server/server.mjs --addr 127.0.0.1:7777`; `--verify` rejects a
Vite command or any remaining 5183 listener.

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
   systemctl --no-pager --full status kitsoki-pog pog-portal; \
   /opt/kitsoki-hosted-pog/node/current/bin/node --version; \
   curl -fsS http://127.0.0.1:7777/api/portal-health; \
   test "$(curl -sS -o /dev/null -w "%{http_code}" http://127.0.0.1:7778/auth/me)" = 401; \
   test -z "$(ss -ltnH "sport = :5183")"; \
   readlink -f /var/lib/pog/runtime; \
   cat /var/lib/pog/runtime/.hosted-pog-local-state.json 2>/dev/null || true'

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
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/api/portal-health       # 401
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/api/feedback-reports    # 401
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/api/colony              # 401
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/api/streams             # 401
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/api/agent-runner/reaped-sessions # 401
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://kitsoki-test.slothattax.me/auth/github/start       # 302 to github.com
curl -sS -o /dev/null -w '%{http_code}\n' -X POST \
  https://kitsoki-test.slothattax.me/auth/github/device/poll # 404 (Device Flow off)
curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST -H 'Content-Type: application/json' --data '{}' \
  https://kitsoki-test.slothattax.me/gh-agent/webhook        # 401
```

The public login page itself returns `200`, and the OAuth start redirect to
GitHub returns `302`; these are protocol entrypoints, not
authorization. A GitHub account that is neither the configured admin nor bound
through a live one-time invite receives `403` and cannot obtain a session that
reaches content.
Check operational health from the VM instead of weakening the public policy:

```sh
ssh root@206.189.84.218 \
  'curl -fsS http://127.0.0.1:7777/api/portal-health; \
   curl -fsS http://127.0.0.1:8787/healthz'
```

After signing in, verify the root portal, `/api/catalog`, a direct routed page,
the feedback widget, the streams/campaign surfaces, runner history, and a
Constructor Studio deck in a real browser. The product switcher must contain
only POG and Constructor Studio. A root page alone is not sufficient proof that
every protected POG upstream works.

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

A source rollback preserves the active runtime by default. State snapshots and
the mutable runtime releases created from them remain under
`/opt/kitsoki-hosted-pog/state-releases/` and
`/var/lib/pog/runtime-releases/`; the installer records the imported SHA-256 in
the active runtime. Do not repoint these symlinks by hand during an ordinary
deployment. If a state-sync conflict occurs, inspect the named files and make
an explicit data reconciliation before retrying.

Common failures:

- The deploy helper reports `incorrect_client_credentials`: the client secret
  does not match the App's client ID — regenerate the secret in the App's
  settings and update the env var/profile; never weaken `auth.mode` or bypass
  Caddy.
- The deploy helper cannot find a client ID: regenerate the local GitHub App
  profile or set `KITSOKI_HOSTED_POG_GH_CLIENT_ID` to the App's public client
  ID.
- GitHub shows a `redirect_uri` mismatch on the authorize page: register
  `https://kitsoki-test.slothattax.me/auth/github/callback` as the App's
  callback URL; no redeploy is needed afterward.
- Public root returns `502`: check both `kitsoki-pog` (auth/RPC on 7778) and
  `pog-portal` (production POG on 7777). Caddy remains fail-closed while auth
  is unavailable.
- A build reports a missing Node built-in such as `node:sqlite`: update the
  pinned runtime contract and official checksum through review; do not fall
  back to the VM's ambient `/usr/local/bin/node`.
- Loopback portal is down but auth is healthy: inspect `pog-portal` logs, the
  pinned Node target, `portal/dist/index.html`, and `portal/server/server.mjs`;
  do not point Caddy at an ad hoc process.
- `--verify` finds Vite or a 5183 listener: stop. The versioned unit was not
  activated cleanly or an untracked process survived. Inspect the PID and
  redeploy; do not declare the service production-ready while either exists.
- Feedback reports, autonomy queue, or scoreboard disappear after login:
  inspect Caddy for an accidental `/api/feedback*` wildcard. Only exact
  `/api/feedback` belongs to 8788; every longer POG route goes to 7777.
- Loopback `/healthz` fails while POG works: the existing GitHub-agent service
  is unhealthy; the public route intentionally returns `401`. Treat that as a
  separate incident and use
  [`github-app-setup.md`](github-app-setup.md).
- State sync reports divergent hosted files: do not delete or overwrite the
  hosted runtime. Reconcile the exact paths reported by the installer, retain
  both histories where appropriate, rebuild the local snapshot, and retry.
- The product selector shows a third repository: treat this as a failed
  deployment contract. Run `--verify`, inspect `/opt/pog/releases/Kitsoki` and
  the loopback `/api/catalog`, and redeploy through the versioned installer;
  do not hide the extra product only in client-side navigation.

The deployed checkout is a review/operator surface, not the source authority.
Tracked POG work must still land through POG's protected Capsule/promotion
workflow. `/var/lib/pog/runtime` holds the durable hosted portal state and is
preserved across ordinary immutable source releases.
