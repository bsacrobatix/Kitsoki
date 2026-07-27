# Remote queue-admission service

This is the deployment and wire contract for accepting a disposable worker's
integration result without giving that worker filesystem access to the
protected project or making the project checkout a bundle download target.
The architecture and trust invariants are described in
[Capsule merge queue](../architecture/merge-queue.md#authenticated-remote-admission).

## Authority layout

Choose a dedicated durable root on a controller volume:

```text
/var/lib/kitsoki-queue-admission/pog/
├── admissions/          immutable successful response records
├── incoming/            bounded temporary bundle streams; empty at rest
├── intents/             execution-id replay/substitution authority
├── queue/
│   ├── external-anchors/
│   ├── external-objects.git/
│   ├── gate-memo/
│   └── state.json
└── receipts/            exact receipt bytes, keyed by receipt digest
```

`--root` must be outside `--project`; startup rejects equality, either
directory nested inside the other, and symlink roots. The service performs
this check before creating the authority directory. It creates authority
directories as mode `0700` and durable JSON as mode `0600`.

Admission is read-only with respect to the protected project: it does not
write the worktree, refs, index, or Git object database. A later merge-queue
worker is a separate execution phase and must be given the same exact
`<root>/queue` through `--queue-root`.

## Credentials and process command

Use a random service-to-service bearer secret, not an interactive OAuth
session. Keep all secrets in the process manager's credential/environment
store; none belong in command arguments or repository files.

Required environment:

```text
KITSOKI_QUEUE_ADMISSION_TOKEN=<random high-entropy bearer token>
KITSOKI_WORKER_OUTPUTS_ACCESS_KEY=<Spaces access key ID>
KITSOKI_WORKER_OUTPUTS_SECRET_KEY=<Spaces secret key>
```

Loopback listener for access through an SSH tunnel:

```sh
/opt/kitsoki/bin/kitsoki queue serve-admission \
  --project /opt/pog/source \
  --project-id pog \
  --root /var/lib/kitsoki-queue-admission/pog \
  --listen 127.0.0.1:7444 \
  --bucket-url https://kitsoki-test.sgp1.digitaloceanspaces.com
```

For a worker-direct network endpoint, bind a routable address only with TLS:

```sh
/opt/kitsoki/bin/kitsoki queue serve-admission \
  --project /opt/pog/source \
  --project-id pog \
  --root /var/lib/kitsoki-queue-admission/pog \
  --listen 0.0.0.0:7444 \
  --tls-cert /etc/kitsoki/admission.crt \
  --tls-key /etc/kitsoki/admission.key \
  --bucket-url https://kitsoki-test.sgp1.digitaloceanspaces.com
```

The default bucket credential variable names can be changed with
`--bucket-key-env` and `--bucket-secret-env`. `--max-bundle-bytes` is capped
at 512 MiB. `--max-concurrent` defaults to four authenticated admissions.

### Hosted POG controller

For the supported hosted POG controller, do not copy an unlanded POG installer
or create the unit by hand. A protected Kitsoki release installed with
`scripts/deploy-hosted-pog.sh --yes` owns all of the following atomically:

- `/etc/kitsoki/queue-admission.env`, root-owned and mode `0600`; it reuses an
  existing valid environment, or on the first install derives the POG
  vm-pool's configured Spaces credentials (`DO_SPACES_KEY_ID` and
  `DO_KITSOKI_TEST_API_KEY`) from the root-owned queue-worker environment and
  mints the admission bearer. Per-worker `KITSOKI_WORKER_OUTPUTS_*` aliases
  are deliberately not required on the controller;
- `kitsoki-queue-admission.service`, running as `pog` with a strict writable
  allow-list only for `/var/lib/kitsoki-queue-admission/pog`. Its `--project`
  is rendered to the immutable real release directory
  `/opt/pog/releases/<sha>` rather than `/opt/pog/current`, because admission
  rejects symlink project roots before it accepts any request;
- a literal loopback listener at `127.0.0.1:7444`; and
- the hosted queue-worker dependency, so a missing or invalid admission
  service prevents consumer startup instead of silently falling back to a
  project-local queue.

`scripts/deploy-hosted-pog.sh --verify` proves the unit is active, the root
and environment modes/owners are correct, a missing bearer receives `401`,
and no non-loopback listener owns port 7444. Installation waits for that `401`
after starting the `Type=simple` service; `429` is a hard contract failure, not
a startup-ready result. The deployment rollback restores
the preceding unit and root-only environment; it never copies or rewrites
queue state.

## Worker request

The worker first uploads its verified `refs.bundle` to the exact Spaces key
sealed in its handoff. It then sends:

```http
POST /v1/queue/admissions HTTP/1.1
Authorization: Bearer <token>
Content-Type: application/json
X-Kitsoki-Request-ID: <execution-scoped request id>
```

```json
{
  "schema": "capsule-queue-remote-admission-request/v1",
  "handoff": {
    "schema": "pog/integration-train-worker-admission-handoff/v1",
    "handoff_key": "runs/<execution>/artifacts/integration-train-external-admission-handoff.json",
    "result": {},
    "result_digest": "sha256:<64 hex>",
    "receipt": {},
    "bundle": {
      "key": "runs/<execution>/wip/refs.bundle",
      "digest": "sha256:<64 hex>",
      "bytes": 123,
      "head": "<40 hex candidate>",
      "base_sha": "<40 hex base>"
    },
    "handoff_digest": "sha256:<64 hex>"
  },
  "receipt_base64": "<base64 of the exact receipt file bytes>",
  "target_base_sha": "<same 40 hex base>",
  "target_policy": "wave-auto",
  "finalization_policy": "autonomous",
  "paths": []
}
```

The complete handoff fields are defined by the POG worker producer; the API
rejects unknown fields, a second JSON value, non-derived object keys,
non-canonical digests, unsafe refs, receipt/result disagreement, and any
bundle whose remote HEAD/GET metadata, byte count, digest, advertised head,
base, or ancestry differs.

Success is HTTP `201` with schema
`capsule-queue-remote-admission-response/v1` and immutable `admission`,
`anchor`, and `candidate` IDs. Exact replay returns those same identities,
including after process interruption. The first normalized submission
durably binds its execution ID; changing the handoff, receipt, target policy,
finalization policy, paths, or runtime receipt returns HTTP `409` with
`replay_substitution`.

Error classification is part of the worker retry contract:

| HTTP | Code | Worker action |
| --- | --- | --- |
| `400` | `malformed_request`, `handoff_tampered`, `receipt_tampered` | terminal; rebuild the sealed request |
| `401` | `unauthorized` | terminal configuration failure; refresh/fix the bearer token |
| `409` | `replay_substitution` | terminal; do not mutate an existing execution identity |
| `422` | `receipt_invalid`, `bundle_tampered`, `admission_rejected` | terminal content/authority failure |
| `429` | `rate_limited` | retry after the `Retry-After` delay |
| `502` | `bundle_unavailable` | retry as transient object-store infrastructure |

Authentication is evaluated before capacity. An invalid credential is always
`401`, even while the authenticated admission limit is exhausted; it is never
reported as `429`.

### Native Capsule remote promotion

`kitsoki capsule promote --remote-admission-url ...` admits a sealed managed
Capsule through this service, but admission is not delivery. It returns the
durable admission, anchor, candidate, and current candidate phase. Its
`status.terminal` is true only for `landed` or `rejected`; `queued`,
`retry_wait`, and `needs_input` are deliberately not success.

Remote promotion requires `--remote-status-command` to be the exact command
for this authority host, for example:

```sh
ssh "$ORCH_HOST" '/opt/kitsoki/bin/kitsoki queue status --project /opt/pog/source --queue-root /var/lib/kitsoki-queue-admission/pog/queue --json'
```

The client prints that command beside the immutable candidate ID and never
executes or polls it locally. This keeps the orchestrator read-only and makes
the canonical hosted queue, rather than a local mirror, the authority for
terminal delivery state.

## Queue consumption

All queue control surfaces that need this authority accept the exact external
queue directory:

```sh
/opt/kitsoki/bin/kitsoki queue status \
  --project /opt/pog/source \
  --queue-root /var/lib/kitsoki-queue-admission/pog/queue \
  --json

/opt/kitsoki/bin/kitsoki queue worker \
  --project /opt/pog/source \
  --queue-root /var/lib/kitsoki-queue-admission/pog/queue \
  --target staging/local \
  --executor vm-pool \
  --executor-pipeline change
```

The same `--queue-root` is available on submit, external submit, process,
migrate, sweep, approval, and operator verbs. Do not copy or mirror
`state.json` into the project checkout: the external authority is canonical.
