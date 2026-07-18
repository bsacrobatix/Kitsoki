// Package webauth is the inbound login/session layer for the `kitsoki web` /
// `kitsoki daemon` HTTP surface when it is deployed beyond trusted localhost.
//
// The model is invitation-only GitHub sign-in: an operator mints a one-time
// invite link (`kitsoki web invite <name>`) tied to a person; opening it and
// completing the GitHub OAuth web flow associates that person's GitHub account
// with the invite and creates a browser session (an HttpOnly cookie). From
// then on the person signs in with GitHub directly. Admins are defined
// locally — either the invite's --admin flag or the `auth.admins` GitHub-login
// list in .kitsoki.yaml.
//
// The package has three seams, wired by cmd/kitsoki/web.go:
//
//   - [Store]: users / invites / sessions persisted in the same SQLite file as
//     the session store (webauth_* tables, idempotent DDL). Secrets are never
//     stored: invite codes and session tokens are 32-byte random values whose
//     sha256 is kept at rest.
//   - [GitHubClient]: the minimal OAuth authorize/exchange/user-fetch client.
//     Endpoint URLs are injectable so tests run against a fake server.
//   - [Manager]: the HTTP layer — Mount registers the /auth/* routes on the
//     server mux and Wrap gates every other route. An authenticated request
//     has its X-Kitsoki-Actor header overwritten with the session user's
//     GitHub login, feeding the server's existing actor/author seam (and
//     neutralizing client spoofing of that header).
//
// Whether the gate is installed at all is the caller's decision:
// [IsLoopbackAddr] collapses the `auth.mode: auto` config default — loopback
// binds stay authless (the historical trusted-localhost posture), non-loopback
// binds require login.
//
// # Non-goals
//
// Role-based restriction: the admin/user role is recorded on the user row and
// reported by /auth/me, but nothing is gated by role yet. Email delivery:
// invite links are printed to the operator, who shares them out of band.
// Token refresh against GitHub: the GitHub access token is used once to read
// the user's identity and then discarded — kitsoki sessions are its own.
package webauth
