# Web login — invitation-only GitHub sign-in

`kitsoki web` / `kitsoki daemon` serve their HTTP surface with no
authentication by default: the historical posture assumes a trusted
localhost. `internal/webauth` adds a login gate for the case where the
server is deployed somewhere reachable beyond that — only people the
operator has explicitly invited can get in.

## Model

There is no identity provider of kitsoki's own and no password. An
operator mints a one-time invite link tied to a person; opening it and
completing a standard GitHub OAuth web flow associates that person's
GitHub account with the invite and starts a browser session. From then on
they sign in with GitHub directly — the invite is consumed on first use.
Admins are defined locally: either `--admin` on the invite, or a GitHub
login listed in `auth.admins` (no invite needed for those). The
admin/user role is recorded and reported (`GET /auth/me`); nothing is
gated by role yet.

## When the gate is on

`auth.mode` in `.kitsoki.yaml` is `off` | `required` | `auto` (default
`auto`). Auto collapses against the listen address
(`webauth.IsLoopbackAddr`, `internal/webauth/addr.go`): a loopback
`--addr` (the `127.0.0.1:7777` default) stays open with no login; any
other bind requires it. `required` with no GitHub OAuth App configured
fails the server at startup rather than serving open or silently
unusable — see `buildWebAuth` in `cmd/kitsoki/web_auth.go`.

```yaml
# .kitsoki.yaml (safe to check in — no secret here)
auth:
  mode: auto                              # off | required | auto
  public_url: https://kitsoki.example.com # anchors redirect_uri + invite links
  admins: [your-github-login]
  session_ttl: 720h                       # default 30 days
  github:
    client_id: <OAuth App client id>
```

```yaml
# .kitsoki.local.yaml (gitignored — the secret lives here)
auth:
  github:
    client_secret: "${KITSOKI_GH_CLIENT_SECRET}"
```

Register a GitHub OAuth App with callback URL
`<public_url>/auth/github/callback` and put its client id/secret above.

## Inviting someone

```
kitsoki web invite "Ana"            # role: user
kitsoki web invite "Bob" --admin    # role: admin
kitsoki web invite --list           # see pending/redeemed invites
```

This prints a one-time link (`<public_url>/auth/invite?code=...`) the
operator shares out of band (chat, email — kitsoki sends nothing itself).
The command opens the same `sessions.db` the server uses (WAL makes that
safe alongside a running daemon) and creates it if the server has never
run.

## Request flow

```mermaid
sequenceDiagram
    participant User as Browser
    participant Gate as webauth.Manager (Wrap)
    participant GH as GitHub

    User->>Gate: GET /auth/invite?code=...
    Gate-->>User: 302 /auth/login?invite=...
    User->>Gate: GET /auth/github/start
    Gate-->>User: 302 GitHub authorize (state cookie set)
    User->>GH: sign in
    GH-->>User: 302 /auth/github/callback?code&state
    User->>Gate: GET /auth/github/callback
    Gate->>GH: exchange code, fetch user
    Gate->>Gate: redeem invite / auto-provision admin
    Gate-->>User: 302 next (session cookie set)
    User->>Gate: any /rpc, /rpc/events, ...
    Gate->>Gate: X-Kitsoki-Actor := session user's GitHub login
```

`Manager.Wrap` (`internal/runstatus/server/server.go`'s `Handler()`,
gated by `WithAuth`) sits over the whole mux and exempts only `/auth/*`.
An authenticated request has its `X-Kitsoki-Actor` header **overwritten**
with the session user's GitHub login before reaching the RPC dispatch —
reusing the identity seam `resolveActor` already read (`server.go:121`),
and neutralizing any client-supplied spoof of that header. An
unauthenticated page load (`Accept: text/html`) is redirected to
`/auth/login?next=...`; everything else — the SPA's `/rpc` fetches and
its `EventSource` streams — gets a `401` JSON body so clients fail fast.

The frontend (`tools/runstatus/src/transport/transport.ts`) bounces the
tab to `/auth/login` on a `401` from `call()`/`postEventStream()`, and
probes `GET /auth/me` when an `EventSource` errors (the only way to infer
a `401` there, since `EventSource` hides HTTP status) before deciding
whether to reconnect or redirect.

## Storage

Three SQLite tables (`webauth_users`, `webauth_invites`,
`webauth_sessions`) live in the same `sessions.db` the session store
already uses, following its idempotent-DDL convention
(`internal/webauth/store.go`). Invite codes and session cookies are
32-byte random values; only their sha256 is ever persisted — the
plaintext exists solely in the printed invite link and the cookie itself.

## Non-goals (for now)

- **No role enforcement.** admin/user is recorded and reported, not
  gated. Add checks in `Manager.Wrap` or per-route when a feature needs
  them.
- **No email delivery.** Invite links are printed for the operator to
  share manually.
- **No GitHub token retained.** The OAuth access token is used once
  (`FetchUser`) and discarded.
