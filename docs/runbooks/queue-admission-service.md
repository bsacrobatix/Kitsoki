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
`retry_wait`, `needs_input`, `needs_conflict_input`, and `needs_human` are
deliberately not success. `needs_human` in particular is terminal *for
automation* — the worker will never retry it — but it is not a success
terminal: a human must `resume`/`override`/`reject` it.

Remote promotion requires `--remote-status-command` to be the exact command
for this authority host, for example:

```sh
ssh "$ORCH_HOST" '/opt/kitsoki/bin/kitsoki queue status --project /opt/pog/source --queue-root /var/lib/kitsoki-queue-admission/pog/queue --json'
```

The client prints that command beside the immutable candidate ID and never
executes or polls it locally. This keeps the orchestrator read-only and makes
the canonical hosted queue, rather than a local mirror, the authority for
terminal delivery state.

### Hosted registered-Capsule executor

For a read-only orchestrator, do not run `capsule promote --remote-admission-*`
there: that client intentionally owns the source bundle and therefore needs
the admission and object-store credential names.  Instead dispatch the typed
host command below from the credentialed controller/worker.  It resolves the
registered managed Capsule on that host, runs its exact Capsule-CI pipeline,
creates the receipt, run record, sealed Git bundle, and handoff there, and
then calls the loopback admission service with the host-only environment.

```sh
/opt/kitsoki/bin/kitsoki queue execute-capsule-promotion \
  --project /opt/pog/releases/<immutable-pog-sha> \
  --workspace <registered-capsule-id> \
  --pipeline change \
  --target integration/current \
  --target-base-sha <exact-40-character-integration-head> \
  --train <immutable-train-id> \
  --bucket-url https://kitsoki-test.sgp1.digitaloceanspaces.com \
  --status-command '/opt/kitsoki/bin/kitsoki queue status --project /opt/pog/releases/<immutable-pog-sha> --queue-root /var/lib/kitsoki-queue-admission/pog/queue --json'
```

The command accepts no bearer or object-store secret values.  Its token and
bucket options name environment variables only; the enclosing host service or
worker must supply them from the root-only admission environment.  It rejects
`main`, `staging/*`, a non-loopback admission endpoint, and a non-full target
base before it opens the registered Capsule or starts CI.  It is an admission
producer, not a merge worker: an admitted candidate still requires the one
mutually-exclusive integration worker described below.  `401` is terminal
configuration evidence; only `429` retains bounded retry semantics.

The request surface intentionally contains no source path, bundle, receipt, or
run-record option. `--workspace` is an opaque registered identity. The host's
Capsule manager resolves it only beneath that host project's granted managed
workspace roots, rejecting an absent, symlink-escaped, or controller-local
path before CI can begin.

If that registered Capsule exists only on the controller, bridge it without a
controller disk artifact or credential by streaming its committed Git bundle
to the hosted importer. The importer verifies that the bundle contains the
declared registered HEAD and writes an immutable identity-bound artifact:

```sh
# Controller: read-only source export over its existing authenticated transport.
git -C <managed-capsule-path> bundle create - <registered-head> | \
  ssh <host> '/opt/kitsoki/bin/kitsoki queue import-capsule-source ...'
```

The host executor then receives `--source-artifact <manifest-key>` and a
host-private `--source-root`. It resolves and verifies the artifact, creates a
temporary checkout solely from that bundle, runs doctor/CI/admission, and
removes only that temporary checkout. A missing artifact is a hard failure;
the executor never substitutes `/opt/pog/current`, host `main`, or a remote
Git fetch.

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
  --gate-tier change \
  --executor vm-pool \
  --executor-pipeline change
```

The same `--queue-root` is available on submit, external submit, process,
migrate, sweep, approval, and operator verbs. Do not copy or mirror
`state.json` into the project checkout: the external authority is canonical.

Protected workers must pass the effective tier in argv. Executor-backed
workers refuse to start when `--executor-pipeline` differs from
`--gate-tier`; a local `--gate` is opaque shell text, so its tier is exported
to the command as `KITSOKI_GATE_TIER` and cannot be inferred from that text.
Direct implementation checks should use `kitsoki queue gate-run --gate-tier
<tier> -- <command>` so they share the same host capacity pool. Nested
gate-run calls borrow a kernel-bound liveness marker and do not deadlock; the
actual capacity lock remains exclusively in the owning Kitsoki process.

### Hosted `integration/current` drain worker

The hosted deployment installs
`kitsoki-pog-integration-current-worker.service` but leaves it disabled. It is
the only supported consumer for an `integration/current` candidate: its target,
external queue root, vm-pool executor, and concurrency are all literal in the
unit. It conflicts with the normal main-target worker, so do not run both.

After a verified hosted deploy, a drain owner switches consumers only when the
external queue status shows no active main-target lease:

```sh
# Stop the normal consumer before starting the mutually-exclusive train owner.
systemctl stop kitsoki-queue-worker.service
systemctl enable --now kitsoki-pog-integration-current-worker.service

# Observe only the hosted canonical authority.
/opt/kitsoki-hosted-pog/current/kitsoki queue status \
  --project /opt/pog/current \
  --queue-root /var/lib/kitsoki-queue-admission/pog/queue --json

# After every train candidate is terminal, restore normal delivery.
systemctl disable --now kitsoki-pog-integration-current-worker.service
systemctl start kitsoki-queue-worker.service
```

The hosted installer refuses to deploy while this unit is enabled or active.
That prevents a release switch from replacing the engine below an in-flight
integration train; finish or park the train and restore the main worker first.
