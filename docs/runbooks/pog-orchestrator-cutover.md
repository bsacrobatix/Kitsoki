# Runbook: cut the POG merge queue over from laptop to orchestrator VM (W7)

Adapts POG's proven laptop-side "safe worker changeover" procedure
(`docs/architecture/merge-queue.md` → "Safe worker changeover", and
`docs/architecture/merge-queue.md` in the POG repo) to a cross-machine move:
the live queue worker moves from a macOS launchd job on the operator's laptop
(`ops/launchd/com.pog.kitsoki-queue.plist` in the POG repo) to
`kitsoki-queue-worker.service` on the persistent DigitalOcean "orchestrator"
droplet provisioned by `deploy/orchestrator/provision.sh`. Same invariant as
the laptop procedure: validate → snapshot → stop old → verify unchanged →
start new → verify exactly one live lease — just with an explicit state and
repo *transfer* wedged in the middle, because the two machines don't share a
filesystem.

Read `deploy/orchestrator/README.md` first if you haven't provisioned the
orchestrator yet — this runbook assumes `provision.sh` has already run there
(binary installed, `kitsoki-daemon` and `kitsoki-queue-worker` units
installed, `/etc/kitsoki/queue-worker.env` and
`~/.config/kitsoki/daemon.env` present as placeholder templates) and is now
crash-looping harmlessly because the POG checkout and real secrets aren't
there yet. That's the starting state this runbook resolves.

Variables used below (set them once per session):

```
LAPTOP_POG_ROOT=/path/to/POG              # POG checkout on the laptop today
ORCH_HOST=orchestrator.pog-do.example      # ssh alias/host for the droplet
ORCH_USER=kitsoki                          # dedicated system user (provision.sh)
ORCH_PROJECT_ROOT=/var/lib/kitsoki/pog     # --project-root from provision.sh
```

## 1. Preflight — laptop and orchestrator both green before touching anything

1.1. On the laptop, confirm the live queue is actually healthy right now (not
mid-incident — a cutover is not the time to also debug a stuck candidate):

```
kitsoki queue status --project "$LAPTOP_POG_ROOT"
```

Resolve anything in `needs_input`/`needs_conflict_input` first (`kitsoki
queue kick|resume|override|reject`, per `docs/architecture/merge-queue.md`)
— don't carry a stuck candidate across the move.

1.2. On the orchestrator, confirm the base host is ready for agent-backed
work (provider/toolkit/MCP):

```
ssh "$ORCH_HOST" kitsoki doctor
```

1.3. Confirm the ephemeral-worker base image is registered (dispatcher
readiness for the ephemeral side of this topology):

```
ssh "$ORCH_HOST" kitsoki vmpool image list --project "$ORCH_PROJECT_ROOT" --json | jq .
```

A configured `image` field (non-empty) means a worker base image is bound; an
empty one is a hard blocker — run `kitsoki vmpool image build`/`finalize`
first (see `cmd/kitsoki/vmpool.go`), out of scope for this runbook.

1.4. Bucket smoke check against `kitsoki-test.sgp1`. **CLI gap:** Kitsoki has
no `kitsoki objectstore` (or similar) smoke-test subcommand today — see "CLI
gaps" at the bottom. Until one exists, use a generic S3-compatible client
with the Spaces secret already provisioned in
`~/.config/kitsoki/daemon.env` (§4):

```
ssh "$ORCH_HOST" 'sudo bash -s' <<'REMOTE'
set -euo pipefail
secret="$(grep -o 'DO_KITSOKI_TEST_API_KEY=.*' /var/lib/kitsoki/.config/kitsoki/daemon.env | cut -d= -f2-)"
runuser -u kitsoki -- env \
  AWS_ACCESS_KEY_ID=DO801QYJLKD3UM7ZEUKE \
  AWS_SECRET_ACCESS_KEY="$secret" \
  aws s3 ls s3://kitsoki-test --endpoint-url https://sgp1.digitaloceanspaces.com
REMOTE
```

(The whole block is piped to a remote `sudo bash -s` so the secret is read
and substituted on the orchestrator itself — it never touches the local
shell or its history.)

A non-error listing (even empty) confirms connectivity and the credential
pair; a 403/`SignatureDoesNotMatch` means the secret in `daemon.env` is wrong
or not yet filled in.

1.5. Pool reconcile report clean (no stray orphaned droplets from prior
laptop-driven runs before you start relying on this pool for real dispatch):

```
ssh "$ORCH_HOST" kitsoki vmpool status --project "$ORCH_PROJECT_ROOT" --json | jq '.reconcile'
```

Expect empty orphan/lost lists. If not, `kitsoki vmpool reap --repair
--project "$ORCH_PROJECT_ROOT"` before proceeding.

1.6. If earlier dispatches wrote workspace-local pool fragments, migrate them
only while admission is held. Snapshot each legacy
`.capsules/vmpool/state.json` first, then pass every owning workspace root
explicitly:

```
kitsoki vmpool migrate \
  --project "$ORCH_PROJECT_ROOT" \
  --legacy-project "$ORCH_PROJECT_ROOT/.capsules/workspaces/<first>" \
  --legacy-project "$ORCH_PROJECT_ROOT/.capsules/workspaces/<second>"
```

The command lists the live provider inventory by the configured pool tag and
requires an exact join before atomically creating the outer-project state. It
aborts on a malformed fragment, conflicting worker/job/instance identity,
untracked live instance, or a non-empty divergent destination. A tracked
non-terminal worker whose provider instance is absent is safely reconciled to
a typed terminal `failed/lost` record in the atomic destination; this lets an
authoritative zero-instance provider inventory converge projected active
counts to zero without pretending the old lease is still live. It never
deletes or rewrites legacy fragments; retain them read-only for the audit
window. Do not run `vmpool reap --repair` against a fragmented store.

## 2. Drain in-flight laptop dispatches

2.1. Stop new admission mentally (don't submit new candidates during the
cutover window — no CLI flag pauses `queue submit` itself, just don't run
it), then poll until nothing is actively being prepared/gated/finalized:

```
until [ "$(kitsoki queue status --project "$LAPTOP_POG_ROOT" --json \
  | jq '[.candidates[] | select(.status=="preparing" or .status=="gating" or .status=="finalizing")] | length')" = "0" ]; do
  sleep 5
done
echo "drained: no in-flight leases"
```

2.2. Stop the laptop's launchd worker (it will not restart under
`KeepAlive` until re-bootstrapped):

```
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.pog.kitsoki-queue.plist \
  || launchctl bootout gui/$(id -u)/com.pog.kitsoki-queue
launchctl list | grep -c com.pog.kitsoki-queue   # expect: 0
```

2.3. Confirm nothing restarted it and the queue is still quiescent:

```
sleep 15
kitsoki queue status --project "$LAPTOP_POG_ROOT" --json \
  | jq '[.candidates[] | select(.status=="preparing" or .status=="gating" or .status=="finalizing")] | length'
# expect: 0
```

## 3. Queue state snapshot + transfer

The durable queue ledger is a single untracked file, `.capsules/queue/state.json`
(gitignored — never part of a git bundle; see §5). `kitsoki queue status
--json` embeds a live "as-of" timestamp and recomputed phase durations, so it
is **not** stable for a byte-hash comparison — hash the raw file itself.

3.1. Snapshot on the laptop:

```
sha256sum "$LAPTOP_POG_ROOT/.capsules/queue/state.json" | tee /tmp/pog-queue-state.sha256
kitsoki queue status --project "$LAPTOP_POG_ROOT" --json > /tmp/pog-queue-state-report.json
jq '.summary' /tmp/pog-queue-state-report.json   # human-readable counts for the record
```

3.2. Transfer the raw state file (not a bundle — it's not git content):

```
scp "$LAPTOP_POG_ROOT/.capsules/queue/state.json" "$ORCH_HOST:/tmp/pog-queue-state.json"
scp /tmp/pog-queue-state.sha256 "$ORCH_HOST:/tmp/pog-queue-state.sha256"
```

3.3. On the orchestrator, verify the transferred file's hash matches before
using it (do this *before* it is ever placed under `$ORCH_PROJECT_ROOT` —
catch a truncated/corrupted transfer before it becomes live state):

```
ssh "$ORCH_HOST" 'cd /tmp && sha256sum -c pog-queue-state.sha256'
```

Do not copy it into `$ORCH_PROJECT_ROOT/.capsules/queue/state.json` yet —
that happens in §6, after the POG checkout itself lands (§5), so the queue's
first read on the new host sees a consistent repo + state pair together
rather than state referencing branches the local git object store doesn't
have yet.

## 4. Secrets provisioning checklist

Fill in real values on the orchestrator only, never in a script or a commit.
Both files were created as root-only (`0600`) placeholder templates by
`provision.sh`; edit them in place:

```
ssh "$ORCH_HOST" sudo -e /var/lib/kitsoki/.config/kitsoki/daemon.env
```

- [ ] `DO_KITSOKI_TEST_API_KEY` set (Spaces secret key, access key ID
      `DO801QYJLKD3UM7ZEUKE`, bucket `kitsoki-test.sgp1`).
- [ ] `DO_GAGNRENOUS_TURD_API_KEY` set (DigitalOcean API token scoped to
      droplet create/destroy for the worker pool).
- [ ] File is still `root:root 0600` after editing (`stat -c '%U:%G %a'`).
- [ ] Bucket smoke check (§1.4) passes with these values.
- [ ] `kitsoki vmpool status --project "$ORCH_PROJECT_ROOT" --json` runs
      without an auth error (proves the DO token works).

`/etc/kitsoki/queue-worker.env` holds no secrets (project root, target,
gate, concurrency, retry budget) — confirm its `TARGET=main` and `GATE`
match POG's proven values before moving on:

```
ssh "$ORCH_HOST" sudo cat /etc/kitsoki/queue-worker.env
```

## 5. POG repo transfer (git bundle over SSH — the old origin is dead)

5.1. On the laptop, bundle every ref (branches, tags, and any
queue-managed integration/conflict branches under refs the worker created),
not just `main` — the transferred `state.json` may reference candidate SHAs
on branches other than main:

```
git -C "$LAPTOP_POG_ROOT" bundle create /tmp/pog-cutover.bundle --all
git -C "$LAPTOP_POG_ROOT" bundle verify /tmp/pog-cutover.bundle
```

5.2. Transfer it (scp shown; rsync works identically):

```
scp /tmp/pog-cutover.bundle "$ORCH_HOST:/tmp/pog-cutover.bundle"
```

5.3. On the orchestrator, clone into the empty, already-owned
`$ORCH_PROJECT_ROOT` that `provision.sh` created:

```
ssh "$ORCH_HOST" sudo runuser -u kitsoki -- git clone /tmp/pog-cutover.bundle "$ORCH_PROJECT_ROOT"
ssh "$ORCH_HOST" sudo runuser -u kitsoki -- git -C "$ORCH_PROJECT_ROOT" checkout main
ssh "$ORCH_HOST" sudo runuser -u kitsoki -- git -C "$ORCH_PROJECT_ROOT" rev-parse HEAD
```

Compare that last SHA against `git -C "$LAPTOP_POG_ROOT" rev-parse main` —
they must match exactly.

5.4. The old origin is dead — don't leave a broken remote around to
confuse the next `git fetch`:

```
ssh "$ORCH_HOST" sudo runuser -u kitsoki -- git -C "$ORCH_PROJECT_ROOT" remote remove origin
```

(Re-add a real `origin` later, once one exists, as a separate deliberate
step — out of scope here.)

5.5. Copy `kitsoki.lock`/`.kitsoki.yaml`/any untracked-but-required config
the laptop checkout has that a fresh clone won't (check for drift first):

```
ssh "$ORCH_HOST" sudo runuser -u kitsoki -- git -C "$ORCH_PROJECT_ROOT" status --porcelain --ignored
```

Anything listed under `.kitsoki.yaml`, `kitsoki.lock`, or similar
project-root config that isn't tracked needs an explicit `scp` here before
proceeding — the daemon/worker units default `--config .kitsoki.yaml`
relative to `$ORCH_PROJECT_ROOT`.

## 6. Land the queue state, then start services

6.1. Stop the (so-far crash-looping, per the intro above) worker so it cannot
race this step by auto-vivifying its own empty `state.json` against the
now-populated `$ORCH_PROJECT_ROOT`, then place the verified state file
(from §3) into the freshly cloned checkout:

```
ssh "$ORCH_HOST" sudo systemctl stop kitsoki-queue-worker
ssh "$ORCH_HOST" sudo install -d -o kitsoki -g kitsoki -m 0750 "$ORCH_PROJECT_ROOT/.capsules/queue"
ssh "$ORCH_HOST" sudo install -o kitsoki -g kitsoki -m 0600 /tmp/pog-queue-state.json "$ORCH_PROJECT_ROOT/.capsules/queue/state.json"
ssh "$ORCH_HOST" sha256sum "$ORCH_PROJECT_ROOT/.capsules/queue/state.json"
```

Compare against `/tmp/pog-queue-state.sha256` from §3.1 — must still match
byte-for-byte after the copy.

6.2. Start (or restart, if `provision.sh` already enabled them) both units:

```
ssh "$ORCH_HOST" sudo systemctl restart kitsoki-queue-worker
ssh "$ORCH_HOST" sudo runuser -u kitsoki -- env HOME=/var/lib/kitsoki \
  XDG_RUNTIME_DIR=/run/user/"$(ssh "$ORCH_HOST" id -u kitsoki)" \
  systemctl --user restart kitsoki-daemon
```

6.3. Confirm the queue read the transferred state without complaint and the
counts match the laptop's last snapshot (§3.1):

```
ssh "$ORCH_HOST" kitsoki queue status --project "$ORCH_PROJECT_ROOT" --json | jq '.summary'
```

Compare `train_depth`/`parked_count`/`phase_counts` against
`/tmp/pog-queue-state-report.json`'s `.summary` — identical.

## 7. DNS / intake repointing

POG's own feedback intake (webhook receiver, portal) currently resolves to
the laptop. Repointing DNS/webhook config is owned by POG's ops surface, not
this repo's units — this runbook only calls out the checkpoint:

7.1. Update whatever DNS record / webhook target config currently names the
laptop to name `$ORCH_HOST` (or a stable A/AAAA record pointed at the
droplet) instead. Concrete mechanism (Caddy vhost, GitHub webhook URL, DO
DNS record) is POG-repo-owned; there is no Kitsoki CLI surface for it.

7.2. Verify from an external vantage point (not the orchestrator itself)
that the new address is live and reachable:

```
curl -fsS -o /dev/null -w '%{http_code}\n' http://$ORCH_HOST:7777/  # or whatever POG's public front-door is
```

## 8. Verification gates

8.1. **Exactly one live lease.** With the laptop worker stopped and the
orchestrator worker running, the train should show at most one candidate
actively preparing/gating/finalizing at a time (bounded by
`--concurrency`), each with a distinct non-empty `worker_id` and a
`lease_expires_at` in the future:

```
ssh "$ORCH_HOST" kitsoki queue status --project "$ORCH_PROJECT_ROOT" --json \
  | jq '[.candidates[] | select(.worker_id != "" and (.status=="preparing" or .status=="gating" or .status=="finalizing"))]'
```

Confirm exactly one OS process too:

```
ssh "$ORCH_HOST" 'pgrep -fc "kitsoki queue worker"'   # expect: 1
```

8.2. **One end-to-end dogfood report.** File (or replay) one real feedback
report through the pipeline and confirm it lands on the orchestrator's
`main` through the queue, per the W6 dogfood acceptance predicate in
`.context/p0-persistent-vm-orchestrator-plan.md`: triaged → dispatched →
fixed on an ephemeral worker → independently verified → capsule CI green →
landed via the queue → runtime observation green. Confirm the landing SHA
is now the orchestrator checkout's `main`:

```
ssh "$ORCH_HOST" sudo runuser -u kitsoki -- git -C "$ORCH_PROJECT_ROOT" log -1 --format='%H %s' main
```

Do this **with the laptop closed** — that's the whole point of P0.

## 9. Demote the laptop

Once §8 is green, the laptop keeps its POG checkout as a dev client only:

```
mv ~/Library/LaunchAgents/com.pog.kitsoki-queue.plist ~/Library/LaunchAgents/com.pog.kitsoki-queue.plist.disabled-$(date +%Y%m%d)
```

Do not delete it — it's the fastest rollback path (§10).

## 10. Rollback

If §8 fails (no live lease appears, or the dogfood report never lands),
reverse with the exact snapshot taken in §3 — nothing on the laptop side was
destroyed, only stopped:

1. Stop the orchestrator's worker so it cannot race the laptop:
   `ssh "$ORCH_HOST" sudo systemctl stop kitsoki-queue-worker`.
2. Diff the orchestrator's current `.capsules/queue/state.json` against the
   §3 snapshot (`/tmp/pog-queue-state.json`); if the orchestrator worker
   never actually made progress (expected if §8 failed fast), they're
   identical and nothing needs merging back.
3. Re-enable the laptop launchd job:
   `mv ~/Library/LaunchAgents/com.pog.kitsoki-queue.plist.disabled-* ~/Library/LaunchAgents/com.pog.kitsoki-queue.plist && launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.pog.kitsoki-queue.plist`.
4. Verify the laptop resumes exactly where it left off:
   `kitsoki queue status --project "$LAPTOP_POG_ROOT" --json | jq '.summary'`
   — must match the §3.1 snapshot's summary (nothing progressed while the
   laptop was paused, nothing was lost).
5. Revert §7's DNS/webhook repointing back to the laptop.
6. Leave the orchestrator's units installed but stopped
   (`systemctl disable --now kitsoki-queue-worker`) for the next attempt —
   don't tear down `provision.sh`'s work; the state/repo/secrets already
   staged there are still valid for a retry once the blocking issue is fixed.

## CLI gaps found while writing this runbook

These are real gaps, not flags to work around — follow-ups, not invented
commands:

- **No `kitsoki objectstore`/bucket-smoke subcommand.** §1.4 falls back to a
  generic S3 client (`aws s3 ls`) against the Spaces endpoint. A native
  `kitsoki objectstore smoke --bucket-url ...` (reusing
  `objectstore.ParseBucketURL`/`objectstore.NewSpaces`, already used
  internally by `cmd/kitsoki/capsule_worker_config.go`) would remove the
  external-tool dependency from this preflight.
- **No queue "pause admission" verb.** §2.1 drains by polling status and
  relying on operator discipline not to run `queue submit` during the
  window; there is no `kitsoki queue pause`/`--admission-closed` flag to
  make that mechanical.
- **No single command to snapshot+hash the durable queue state.** §3
  hand-assembles `sha256sum` over the raw `state.json` because
  `kitsoki queue status --json` embeds a live timestamp and recomputed
  durations that make it unsuitable for hashing as-is. A
  `kitsoki queue snapshot --project <root>` that emits (or hashes) exactly
  the durable, deterministic subset of state would make §3/§6/§10 one
  command instead of five.
