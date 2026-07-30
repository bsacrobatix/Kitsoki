#!/usr/bin/env bash
# Deploy the POG portal beside the hosted GitHub agent and put all POG-owned
# HTTP routes behind Kitsoki's invitation-only GitHub session gate.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REMOTE="${KITSOKI_GH_AGENT_REMOTE:-}"
PUBLIC_BASE_URL="${KITSOKI_GH_AGENT_PUBLIC_BASE_URL:-}"
POG_ROOT="${KITSOKI_HOSTED_POG_ROOT:-$HOME/code/POG}"
POG_REF="${KITSOKI_HOSTED_POG_REF:-main}"
# Portfolio members federated onto the hosted site beyond POG's home catalog
# and the embedded Constructor Studio product. Each entry is
#   <catalog-id>:<repo-dir>:<source-checkout>:<ref>
# where <repo-dir> is the basename of the track's repo: field in POG's
# pog/catalog.yaml (the POG_MEMBER_ROOTS key on the host) and <catalog-id> is
# the resolved id the portal exposes (the POG_PORTFOLIO_MEMBERS entry). Members
# are bundled from <ref> — usually main, but a member whose pog/catalog.yaml is
# generated onto a branch names that branch (gears-rust). Each field is
# per-member env-overridable so operators can retarget a checkout or ref
# without editing this contract. NOTE: this publishes each member's catalog on
# the auth-gated hosted site — including private repos (gears-rust, slidey).
HOSTED_MEMBERS=(
	"kitsoki:Kitsoki:${KITSOKI_HOSTED_POG_MEMBER_KITSOKI_ROOT:-$ROOT}:${KITSOKI_HOSTED_POG_MEMBER_KITSOKI_REF:-main}"
	"gears-rust:gears-rust:${KITSOKI_HOSTED_POG_MEMBER_GEARS_ROOT:-$HOME/code/gears-rust}:${KITSOKI_HOSTED_POG_MEMBER_GEARS_REF:-docs/kitsoki-integration}"
	"sassfully:studio-sassfully:${KITSOKI_HOSTED_POG_MEMBER_SASSFULLY_ROOT:-$HOME/code/studio-sassfully}:${KITSOKI_HOSTED_POG_MEMBER_SASSFULLY_REF:-main}"
	"slidey:slidey:${KITSOKI_HOSTED_POG_MEMBER_SLIDEY_ROOT:-$HOME/code/slidey}:${KITSOKI_HOSTED_POG_MEMBER_SLIDEY_REF:-main}"
)
HOSTED_MEMBER_IDS=""
HOSTED_MEMBER_ROOTS=""
for member_entry in "${HOSTED_MEMBERS[@]}"; do
	HOSTED_MEMBER_IDS="${HOSTED_MEMBER_IDS:+$HOSTED_MEMBER_IDS,}${member_entry%%:*}"
	IFS=: read -r _member_id member_dir _member_root _member_ref <<<"$member_entry"
	HOSTED_MEMBER_ROOTS="${HOSTED_MEMBER_ROOTS:+$HOSTED_MEMBER_ROOTS,}$member_dir=/opt/pog/members/$member_dir"
done
HOSTED_PORTFOLIO_MEMBERS="pog,constructor-studio${HOSTED_MEMBER_IDS:+,$HOSTED_MEMBER_IDS}"
# Full hosted product set (POG home + embedded Constructor Studio + federated
# members), sorted, for the post-deploy verification assertion below.
HOSTED_PRODUCTS_SORTED="$(printf 'pog\nconstructor-studio\n%b\n' "${HOSTED_MEMBER_IDS//,/\\n}" | sort -u | paste -sd, -)"
ADMIN="${KITSOKI_HOSTED_POG_ADMIN:-bsacrobatix}"
GH_CLIENT_ID="${KITSOKI_HOSTED_POG_GH_CLIENT_ID:-}"
# Callback (authorization-code) login needs the GitHub App client secret.
# GH_KITSOKI_TEST_CLIENT_SECRET is the operator's conventional env name for
# this deployment's secret; the generic name wins when both are set.
GH_CLIENT_SECRET="${KITSOKI_HOSTED_POG_GH_CLIENT_SECRET:-${GH_KITSOKI_TEST_CLIENT_SECRET:-}}"
GH_APP_PROFILE="${KITSOKI_HOSTED_POG_GH_APP_PROFILE:-$HOME/.config/kitsoki/gh-app/bsacrobatix-kitsoki-test/kitsoki.env}"
# Session-store database backend. sqlite is the default and requires no
# configuration at all — every existing deployment that sets none of these
# keeps running on SQLite exactly as before. Postgres is strictly opt-in via
# KITSOKI_HOSTED_POG_DB_BACKEND=postgres plus the connection details below;
# see docs/architecture/storage-backends.md and
# docs/guide/integrations/hosted-pog.md for the full contract and the
# SQLite -> Postgres migration procedure. The password never touches this
# script's argv or a log line: it travels the same way GH_CLIENT_SECRET does,
# inside the 0700 staged upload as a 0600 file, and reuses the value already
# installed on the host when neither an env var nor a file is supplied here.
DB_BACKEND="${KITSOKI_HOSTED_POG_DB_BACKEND:-sqlite}"
PG_HOST="${KITSOKI_HOSTED_POG_PG_HOST:-}"
PG_PORT="${KITSOKI_HOSTED_POG_PG_PORT:-5432}"
PG_DATABASE="${KITSOKI_HOSTED_POG_PG_DATABASE:-}"
PG_ROLE="${KITSOKI_HOSTED_POG_PG_ROLE:-}"
PG_SSLMODE="${KITSOKI_HOSTED_POG_PG_SSLMODE:-require}"
PG_PASSWORD="${KITSOKI_HOSTED_POG_PG_PASSWORD:-}"
if [ -z "$PG_PASSWORD" ] && [ -n "${KITSOKI_HOSTED_POG_PG_PASSWORD_FILE:-}" ]; then
	PG_PASSWORD="$(tr -d '\n' <"$KITSOKI_HOSTED_POG_PG_PASSWORD_FILE")"
fi
GOCACHE="${GOCACHE:-/private/tmp/kitsoki-gocache}"
NODE_RUNTIME_FILE="$ROOT/deploy/hosted-pog/node-runtime.env"
STATE_PACKAGER="$ROOT/scripts/package-hosted-pog-state.sh"
[ -f "$NODE_RUNTIME_FILE" ] || { echo "missing hosted POG Node runtime contract: $NODE_RUNTIME_FILE" >&2; exit 2; }
# shellcheck disable=SC1090 -- this is a tracked deployment contract.
. "$NODE_RUNTIME_FILE"
NODE_VERSION="${KITSOKI_HOSTED_POG_NODE_VERSION:-}"
NODE_ARCHIVE="${KITSOKI_HOSTED_POG_NODE_ARCHIVE:-}"
NODE_URL="${KITSOKI_HOSTED_POG_NODE_URL:-}"
NODE_SHA256="${KITSOKI_HOSTED_POG_NODE_SHA256:-}"

mode="dry-run"
sync_local_state=0
for arg in "$@"; do
	case "$arg" in
		--yes)
			[ "$mode" = "dry-run" ] || { echo "choose only one of --yes or --verify" >&2; exit 2; }
			mode="deploy"
			;;
		--verify)
			[ "$mode" = "dry-run" ] || { echo "choose only one of --yes or --verify" >&2; exit 2; }
			mode="verify"
			;;
		--sync-local-state) sync_local_state=1 ;;
		*) echo "usage: scripts/deploy-hosted-pog.sh [--yes|--verify] [--sync-local-state]" >&2; exit 2 ;;
	esac
done
[ "$mode" != "verify" ] || [ "$sync_local_state" -eq 0 ] || {
	echo "--sync-local-state changes remote state and cannot be combined with --verify" >&2
	exit 2
}

[ -n "$REMOTE" ] || { echo "KITSOKI_GH_AGENT_REMOTE is required" >&2; exit 2; }
[ -n "$PUBLIC_BASE_URL" ] || { echo "KITSOKI_GH_AGENT_PUBLIC_BASE_URL is required" >&2; exit 2; }
[[ "$PUBLIC_BASE_URL" =~ ^https://[A-Za-z0-9.-]+/?$ ]] || { echo "public base URL must be an https origin with no path" >&2; exit 2; }
PUBLIC_HOST="${PUBLIC_BASE_URL%/}"
PUBLIC_HOST="${PUBLIC_HOST#https://}"
[[ "$ADMIN" =~ ^[A-Za-z0-9-]+$ ]] || { echo "KITSOKI_HOSTED_POG_ADMIN is not a valid GitHub login" >&2; exit 2; }
[[ "$NODE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "invalid hosted POG Node version" >&2; exit 2; }
[ "$NODE_ARCHIVE" = "node-$NODE_VERSION-linux-x64.tar.xz" ] || { echo "hosted POG Node archive does not match its version" >&2; exit 2; }
[ "$NODE_URL" = "https://nodejs.org/download/release/$NODE_VERSION/$NODE_ARCHIVE" ] || { echo "hosted POG Node URL is not the pinned official release URL" >&2; exit 2; }
[[ "$NODE_SHA256" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid hosted POG Node SHA-256" >&2; exit 2; }
case "$DB_BACKEND" in
	sqlite|postgres) ;;
	*) echo "KITSOKI_HOSTED_POG_DB_BACKEND must be sqlite or postgres, got: $DB_BACKEND" >&2; exit 2 ;;
esac
if [ "$DB_BACKEND" = postgres ]; then
	[[ "$PG_HOST" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "postgres backend requires KITSOKI_HOSTED_POG_PG_HOST" >&2; exit 2; }
	[[ "$PG_PORT" =~ ^[0-9]{1,5}$ ]] && [ "$PG_PORT" -ge 1 ] && [ "$PG_PORT" -le 65535 ] \
		|| { echo "invalid KITSOKI_HOSTED_POG_PG_PORT: $PG_PORT" >&2; exit 2; }
	[[ "$PG_DATABASE" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || { echo "postgres backend requires KITSOKI_HOSTED_POG_PG_DATABASE" >&2; exit 2; }
	[[ "$PG_ROLE" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || { echo "postgres backend requires KITSOKI_HOSTED_POG_PG_ROLE" >&2; exit 2; }
	case "$PG_SSLMODE" in
		disable|allow|prefer|require|verify-ca|verify-full) ;;
		*) echo "invalid KITSOKI_HOSTED_POG_PG_SSLMODE: $PG_SSLMODE" >&2; exit 2 ;;
	esac
fi

verify() {
	expect_public_status() {
		local expected="$1" path="$2" actual
		shift 2
		actual="$(curl -sS -o /dev/null -w '%{http_code}' "$@" "${PUBLIC_BASE_URL%/}$path")"
		[ "$actual" = "$expected" ] || {
			echo "expected anonymous $path to return HTTP $expected, got HTTP $actual" >&2
			return 1
		}
	}

	# Every content handler in the Caddyfile has a representative anonymous
	# probe. HTML navigation redirects into login; non-HTML/API traffic is
	# rejected directly. The GitHub login entrypoint and signed-webhook transport are
	# the only deliberate protocol exceptions.
	expect_public_status 302 / -H 'Accept: text/html'
	expect_public_status 401 /assets/access-probe.js
	expect_public_status 401 /api/catalog
	expect_public_status 401 /api/portal-health
	expect_public_status 401 /api/feedback-reports
	expect_public_status 401 /api/colony
	expect_public_status 401 /api/streams
	expect_public_status 401 /api/agent-runner/reaped-sessions
	expect_public_status 401 /rpc
	expect_public_status 401 /api/feedback
	expect_public_status 401 /constructor-studio/decks/access-probe
	expect_public_status 401 /healthz
	expect_public_status 401 /api/ready
	expect_public_status 401 /api/runs
	expect_public_status 401 /api/run/access-probe
	expect_public_status 401 /runs
	expect_public_status 401 /run/access-probe
	expect_public_status 401 /decks/access-probe
	expect_public_status 401 /auth/me
	expect_public_status 200 /auth/login
	expect_public_status 302 /auth/github/start
	expect_public_status 404 /auth/github/device/poll -X POST
	expect_public_status 401 /gh-agent/webhook -X POST -H 'Content-Type: application/json' --data '{}'
	login_page="$(curl -fsS "${PUBLIC_BASE_URL%/}/auth/login")"
	grep -q '/auth/github/start' <<<"$login_page"
	ssh "$REMOTE" bash -s -- "$PUBLIC_HOST" "$HOSTED_PRODUCTS_SORTED" "$HOSTED_MEMBER_ROOTS" "$HOSTED_PORTFOLIO_MEMBERS" <<'REMOTE_VERIFY'
set -euo pipefail
public_host="$1"
expected_products="$2"
expected_member_roots="$3"
expected_portfolio_members="$4"
node_bin=/opt/kitsoki-hosted-pog/node/current/bin/node
systemctl is-active --quiet kitsoki-gh-agent caddy kitsoki-pog pog-portal pog-worker-finalizer.timer kitsoki-queue-admission.service kitsoki-queue-worker.service
systemctl is-enabled --quiet pog-worker-finalizer.timer
test "$(systemctl show --property Result --value pog-worker-finalizer.service)" = success
test "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7778/auth/me)" = 401
test -L /var/lib/pog/runtime
test -L /opt/pog/current/.artifacts
test -L /opt/pog/current/.capsules
test "$(readlink -f /opt/pog/current/.capsules)" = "$(readlink -f /var/lib/pog/capsules)"
hosted_engine=/opt/kitsoki-hosted-pog/current/kitsoki
hosted_source=/opt/kitsoki-hosted-pog/current
hosted_queue_root=/var/lib/kitsoki-queue-admission/pog/queue
hosted_admission_root=/var/lib/kitsoki-queue-admission/pog
active_pog_release="$(readlink -f /opt/pog/current)"
test -x "$hosted_engine"
test -x "$hosted_source/scripts/dev-workspace.sh"
active_kitsoki_sha="$(basename "$(readlink -f "$hosted_source")")"
[[ "$active_kitsoki_sha" =~ ^[0-9a-f]{40}$ ]]
hosted_engine_version="$("$hosted_engine" version)"
grep -Fxq "kitsoki $active_kitsoki_sha" <<<"$hosted_engine_version"
grep -Fxq "revision: $active_kitsoki_sha" <<<"$hosted_engine_version"
test -f /etc/systemd/system/kitsoki-queue-admission.service
test -f /etc/systemd/system/kitsoki-pog-integration-current-worker.service
test "$(stat -c '%U:%G %a' "$hosted_admission_root")" = 'pog:pog 700'
test "$(stat -c '%U:%G %a' /etc/kitsoki/queue-admission.env)" = 'root:root 600'
grep -Fq 'EnvironmentFile=/etc/kitsoki/queue-admission.env' /etc/systemd/system/kitsoki-queue-admission.service
grep -Fq -- '--listen 127.0.0.1:7444' /etc/systemd/system/kitsoki-queue-admission.service
grep -Fq -- "--root $hosted_admission_root" /etc/systemd/system/kitsoki-queue-admission.service
grep -Fq -- "--project $active_pog_release" /etc/systemd/system/kitsoki-queue-admission.service
grep -Fq 'Requires=kitsoki-queue-admission.service' /etc/systemd/system/kitsoki-queue-worker.service.d/10-queue-admission.conf
grep -Fqx 'Conflicts=kitsoki-queue-worker.service' /etc/systemd/system/kitsoki-pog-integration-current-worker.service
grep -Fqx 'Before=kitsoki-queue-worker.service' /etc/systemd/system/kitsoki-pog-integration-current-worker.service
grep -Fq -- '--queue-root /var/lib/kitsoki-queue-admission/pog/queue' /etc/systemd/system/kitsoki-pog-integration-current-worker.service
grep -Fq -- '--target integration/current' /etc/systemd/system/kitsoki-pog-integration-current-worker.service
grep -Fq -- '--concurrency 1' /etc/systemd/system/kitsoki-pog-integration-current-worker.service
! systemctl is-enabled --quiet kitsoki-pog-integration-current-worker.service
! systemctl is-active --quiet kitsoki-pog-integration-current-worker.service
test -z "$(ss -ltnH 'sport = :7444' | awk '$4 != "127.0.0.1:7444" { print }')"
test "$(curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:7444/v1/queue/admissions)" = 401
# The hosted queue-worker drop-in replaces the old `/usr/local/bin/kitsoki`
# invocation without changing that seal/control binary, which remains pinned
# independently to the worker image's story closure.
! grep -q '^[[:space:]]*POG_KITSOKI_BIN=' /etc/kitsoki/queue-worker.env
for unit in kitsoki-pog.service pog-portal.service pog-worker-finalizer.service; do
  systemctl show --property Environment --value "$unit" | grep -Fq "POG_KITSOKI_BIN=$hosted_engine"
done
kitsoki_pog_pid="$(systemctl show --property MainPID --value kitsoki-pog.service)"
[[ "$kitsoki_pog_pid" =~ ^[1-9][0-9]*$ ]]
kitsoki_pog_environ="$(tr '\0' '\n' <"/proc/$kitsoki_pog_pid/environ")"
if grep -qx 'KITSOKI_DB_BACKEND=postgres' <<<"$kitsoki_pog_environ"; then
  pg_dsn="$(sed -n 's/^KITSOKI_PG_DSN=//p' <<<"$kitsoki_pog_environ" | head -n 1)"
  test -n "$pg_dsn"
  postgres_verification=/var/lib/kitsoki-pog/postgres-verification.json
  KITSOKI_PG_DSN="$pg_dsn" "$hosted_engine" db verify --sqlite-path /var/lib/kitsoki-pog/sessions.db --output "$postgres_verification"
  chown pog:pog "$postgres_verification"
  chmod 0600 "$postgres_verification"
else
  ! grep -q '^KITSOKI_PG_DSN=' <<<"$kitsoki_pog_environ"
fi
unset pg_dsn
test -f /etc/systemd/system/pog-colony-runner.service.d/portfolio-authority.conf
colony_environment="$(systemctl show --property Environment --value pog-colony-runner.service)"
colony_pid="$(systemctl show --property MainPID --value pog-colony-runner.service)"
test "$colony_pid" -gt 0
colony_process_environment="$(tr '\0' '\n' <"/proc/$colony_pid/environ")"
grep -Fq 'POG_PORTFOLIO_ROOT=/opt/pog/current' <<<"$colony_environment"
grep -Fq "POG_MEMBER_ROOTS=$expected_member_roots" <<<"$colony_environment"
grep -Fq "POG_PORTFOLIO_MEMBERS=$expected_portfolio_members" <<<"$colony_environment"
grep -Fq "POG_KITSOKI_BIN=$hosted_engine" <<<"$colony_environment"
grep -Fxq "KITSOKI_SOURCE_DIR=$hosted_source" <<<"$colony_process_environment"
portal_pid="$(systemctl show --property MainPID --value pog-portal.service)"
[[ "$portal_pid" =~ ^[1-9][0-9]*$ ]]
portal_process_environment="$(tr '\0' '\n' <"/proc/$portal_pid/environ")"
if grep -qx 'KITSOKI_DB_BACKEND=postgres' <<<"$kitsoki_pog_environ"; then
  grep -Fxq 'KITSOKI_DB_BACKEND=postgres' <<<"$portal_process_environment"
  grep -q '^KITSOKI_PG_DSN=' <<<"$portal_process_environment"
  ! grep -q '^POG_AGENT_RUNNER_DB=' <<<"$portal_process_environment"
  grep -Fxq 'KITSOKI_DB_BACKEND=postgres' <<<"$colony_process_environment"
  grep -q '^KITSOKI_PG_DSN=' <<<"$colony_process_environment"
  ! grep -q '^POG_AGENT_RUNNER_DB=' <<<"$colony_process_environment"
else
  ! grep -q '^KITSOKI_DB_BACKEND=' <<<"$portal_process_environment"
  grep -Fxq 'POG_AGENT_RUNNER_DB=/var/lib/kitsoki-pog/sessions.db' <<<"$portal_process_environment"
  ! grep -q '^KITSOKI_DB_BACKEND=' <<<"$colony_process_environment"
  grep -Fxq 'POG_AGENT_RUNNER_DB=/var/lib/kitsoki-pog/sessions.db' <<<"$colony_process_environment"
fi
unset kitsoki_pog_environ portal_process_environment colony_process_environment
test -f /etc/systemd/system/kitsoki-pog.service.d/zz-hosted-engine.conf
grep -Fq "Environment=POG_KITSOKI_BIN=$hosted_engine" /etc/systemd/system/kitsoki-pog.service.d/zz-hosted-engine.conf
test -f /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
grep -Fq "ExecStart=$hosted_engine queue worker" /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
grep -Fq -- "--queue-root $hosted_queue_root" /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
grep -Fq -- '--executor vm-pool' /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
grep -Fq -- '--gate-tier full' /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
grep -Fq -- '--executor-pipeline full' /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
! grep -Fq -- '--gate ' /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
grep -Fq "Environment=KITSOKI_SOURCE_DIR=$hosted_source" /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
grep -Fq 'POG_GEARS_RUST_SRC=/opt/pog/members/gears-rust' /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
test -f /opt/pog/members/gears-rust/pog/catalog.yaml
queue_pid="$(systemctl show --property MainPID --value kitsoki-queue-worker.service)"
if [ "$queue_pid" -gt 0 ]; then
	test "$(readlink -f "/proc/$queue_pid/exe")" = "$(readlink -f "$hosted_engine")"
	queue_exec_start="$(systemctl show --property ExecStart --value kitsoki-queue-worker.service)"
	grep -Fq -- "--queue-root $hosted_queue_root" <<<"$queue_exec_start"
	grep -Fq -- '--executor vm-pool' <<<"$queue_exec_start"
	grep -Fq -- '--gate-tier full' <<<"$queue_exec_start"
	grep -Fq -- '--executor-pipeline full' <<<"$queue_exec_start"
	! grep -Fq -- '--gate ' <<<"$queue_exec_start"
fi
health="$(curl -fsS http://127.0.0.1:7777/api/portal-health)"
active_pog_sha="$(basename "$(readlink -f /opt/pog/current)")"
printf '%s' "$health" | "$node_bin" -e '
const fs=require("node:fs");
const health=JSON.parse(fs.readFileSync(0,"utf8"));
if(health.ok!==true||health.service!=="pog-portal"||health.mode!=="production"||health.revision!==process.argv[1]){
  console.error(JSON.stringify(health));
  process.exit(1);
}' "$active_pog_sha"
catalog="$(curl -fsS -H "Host: $public_host" http://127.0.0.1:7777/api/catalog)"
printf '%s' "$catalog" | "$node_bin" -e '
const fs=require("node:fs");
const graph=JSON.parse(fs.readFileSync(0,"utf8"));
const expected=process.argv[1];
const products=[...new Set((graph.comparison_catalogs??[]).map((entry)=>entry.id))].sort().join(",");
const repos=[...new Set((graph.nodes??[]).map((node)=>node.attrs?.repo).filter(Boolean))].sort().join(",");
const unavailable=(graph.federation?.unavailable??[]).map((u)=>u.repo);
if(products!==expected||repos!==expected||unavailable.length){
  console.error(JSON.stringify({expected,products,repos,unavailable}));
  process.exit(1);
}' "$expected_products"
curl -fsS http://127.0.0.1:7777/api/feedback-reports | "$node_bin" -e '
const fs=require("node:fs");
if(!Array.isArray(JSON.parse(fs.readFileSync(0,"utf8")).reports)) process.exit(1);'
portal_pid="$(systemctl show --property MainPID --value pog-portal.service)"
portal_command="$(tr '\0' ' ' <"/proc/$portal_pid/cmdline")"
case "$portal_command" in
  *server/server.mjs*"--addr 127.0.0.1:7777"*) ;;
  *) echo "unexpected POG service command: $portal_command" >&2; exit 1 ;;
esac
case "$portal_command" in
  *vite*|*"npm run dev"*) echo "Vite is running in the POG service: $portal_command" >&2; exit 1 ;;
esac
test -z "$(ss -ltnH 'sport = :5183')"
curl -fsS -o /dev/null http://127.0.0.1:8787/healthz
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
REMOTE_VERIFY
	echo "hosted-pog verify: production POG active on 127.0.0.1:7777; Kitsoki auth/RPC active on 127.0.0.1:7778; no Vite command or 5183 listener; products=$HOSTED_PRODUCTS_SORTED with no unavailable federation members; reviewed feedback route owned by POG; versioned runtime active; anonymous route-family matrix denied; GitHub OAuth entrypoint reachable; unsigned webhook denied; loopback health=ok"
}

if [ "$mode" = "verify" ]; then
	verify
	exit 0
fi

if [ -z "$GH_CLIENT_ID" ] && [ -f "$GH_APP_PROFILE" ]; then
	profile_line="$(grep -E '^KITSOKI_GH_APP_CLIENT_ID=' "$GH_APP_PROFILE" | tail -n 1 || true)"
	GH_CLIENT_ID="${profile_line#KITSOKI_GH_APP_CLIENT_ID=}"
fi
[[ "$GH_CLIENT_ID" =~ ^[A-Za-z0-9._-]+$ ]] || {
	echo "set KITSOKI_HOSTED_POG_GH_CLIENT_ID or provide a generated Kitsoki App profile at $GH_APP_PROFILE" >&2
	exit 2
}
if [ -z "$GH_CLIENT_SECRET" ] && [ -f "$GH_APP_PROFILE" ]; then
	secret_line="$(grep -E '^KITSOKI_GH_APP_CLIENT_SECRET=' "$GH_APP_PROFILE" | tail -n 1 || true)"
	GH_CLIENT_SECRET="${secret_line#KITSOKI_GH_APP_CLIENT_SECRET=}"
	unset secret_line
fi
# Repeat deployments should not require copying a root-only production secret
# back onto the operator's workstation. Reuse the exact value already installed
# by a prior successful release. The fixed remote command does not put the
# secret in argv, and command substitution keeps it out of deploy output and
# local files. A first install still fails closed below when no explicit,
# profile, or installed value exists.
if [ -z "$GH_CLIENT_SECRET" ]; then
	if ! GH_CLIENT_SECRET="$(ssh "$REMOTE" \
		"sed -n 's/^KITSOKI_HOSTED_POG_GH_CLIENT_SECRET=//p' /etc/kitsoki/hosted-pog.env 2>/dev/null | head -n 1")"; then
		echo "could not inspect the existing remote hosted POG OAuth credential" >&2
		exit 1
	fi
fi
[[ "$GH_CLIENT_SECRET" =~ ^[A-Za-z0-9._-]+$ ]] || {
	echo "callback login needs the GitHub App client secret: set KITSOKI_HOSTED_POG_GH_CLIENT_SECRET (or GH_KITSOKI_TEST_CLIENT_SECRET), add KITSOKI_GH_APP_CLIENT_SECRET to $GH_APP_PROFILE, or install it through an initial deployment" >&2
	exit 2
}
# Exchange a bogus code: valid credentials answer bad_verification_code, a
# wrong secret answers incorrect_client_credentials. Neither response carries
# the secret, so the failure output is safe to print.
if ! cred_probe="$(curl -sS -X POST -H 'Accept: application/json' \
	--data-urlencode "client_id=$GH_CLIENT_ID" \
	--data-urlencode "client_secret=$GH_CLIENT_SECRET" \
	--data-urlencode "code=kitsoki-deploy-credential-preflight" \
	https://github.com/login/oauth/access_token)"; then
	echo "could not probe GitHub OAuth credentials for the configured App client ID" >&2
	exit 1
fi
grep -q '"bad_verification_code"' <<<"$cred_probe" || {
	echo "GitHub rejected the configured OAuth client credentials: $cred_probe" >&2
	exit 1
}
unset cred_probe

# Same reuse contract as the GH client secret above: a repeat deploy that
# only changes host/port/database/role/sslmode should not have to resupply a
# stable database password, and the value never needs to round-trip through
# the operator's shell history to keep working. install.sh stores it (and the
# assembled DSN) in the same root-only /etc/kitsoki/hosted-pog.env. A first
# postgres deployment still fails closed below when no explicit, file, or
# installed value exists.
if [ "$DB_BACKEND" = postgres ] && [ -z "$PG_PASSWORD" ]; then
	if ! PG_PASSWORD="$(ssh "$REMOTE" \
		"sed -n 's/^KITSOKI_HOSTED_POG_PG_PASSWORD=//p' /etc/kitsoki/hosted-pog.env 2>/dev/null | head -n 1")"; then
		echo "could not inspect the existing remote hosted POG database credential" >&2
		exit 1
	fi
fi
if [ "$DB_BACKEND" = postgres ] && [ -z "$PG_PASSWORD" ]; then
	echo "postgres backend needs a database password: set KITSOKI_HOSTED_POG_PG_PASSWORD or KITSOKI_HOSTED_POG_PG_PASSWORD_FILE, or install it through an initial deployment before later deployments can reuse it" >&2
	exit 2
fi

[ -d "$POG_ROOT/.git" ] || { echo "POG checkout is missing at $POG_ROOT" >&2; exit 2; }
KITSOKI_SHA="$(git -C "$ROOT" rev-parse HEAD)"
git -C "$ROOT" merge-base --is-ancestor "$KITSOKI_SHA" main || { echo "Kitsoki HEAD ($KITSOKI_SHA) is not contained in Kitsoki main" >&2; exit 2; }
pog_sha="$(git -C "$POG_ROOT" rev-parse "$POG_REF^{commit}")"
git -C "$POG_ROOT" merge-base --is-ancestor "$pog_sha" main || { echo "$POG_REF ($pog_sha) is not contained in POG main" >&2; exit 2; }
git -C "$POG_ROOT" show "$pog_sha:portal/package.json" | grep -q 'build:server' \
	&& git -C "$POG_ROOT" cat-file -e "$pog_sha:portal/src/server/production.ts" || {
	echo "POG $pog_sha does not contain the production portal server; promote that change first" >&2
	exit 2
}

cat <<EOF
deploy-hosted-pog:
  kitsoki source: $ROOT ($KITSOKI_SHA)
  POG source:     $POG_ROOT ($POG_REF -> $pog_sha)
  remote:         $REMOTE
  public URL:     ${PUBLIC_BASE_URL%/}
  GitHub admin:   $ADMIN
  GitHub login:   OAuth authorization-code flow (callback ${PUBLIC_BASE_URL%/}/auth/github/callback)
  Node runtime:   $NODE_VERSION (pinned official linux-x64 archive)
  topology:       Caddy -> Kitsoki /auth/check 127.0.0.1:7778 -> production POG 127.0.0.1:7777
  products:       pog, constructor-studio, and federated members: $HOSTED_MEMBER_IDS
  local state:    $([ "$sync_local_state" -eq 1 ] && echo 'bounded portal-state snapshot enabled' || echo 'preserve hosted runtime (no local import)')
  access policy:  login-gated portal, API, agent health/run/deck, and evidence routes
  public protocol: GitHub OAuth login endpoints and the HMAC-verified webhook only
  db backend:     $([ "$DB_BACKEND" = postgres ] && echo "postgres ($PG_ROLE@$PG_HOST:$PG_PORT/$PG_DATABASE sslmode=$PG_SSLMODE)" || echo 'sqlite (default)')
EOF

if [ "$mode" = "dry-run" ]; then
	cat <<'EOF'

dry run only. Re-run with --yes to build, upload, activate, and verify; use
--verify for read-only checks. Callback login requires the GitHub App client
secret and <public-url>/auth/github/callback registered as the App's callback
URL; the secret is shipped inside the staged upload and installed as a
root-only env file, never placed on the ssh command line. Add
--sync-local-state to publish the bounded local feedback, graph-feedback,
streams, colony, and runner-session state. Divergent remote files fail closed
instead of being overwritten.
EOF
	exit 0
fi

[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=no)" ] || {
	echo "refusing to deploy a Kitsoki working tree with uncommitted tracked changes" >&2
	exit 1
}

local_stage="$(mktemp -d /private/tmp/kitsoki-hosted-pog.XXXXXX)"
remote_stage="/tmp/kitsoki-hosted-pog.$$"
cleanup() {
	rm -rf "$local_stage"
	ssh "$REMOTE" "rm -rf '$remote_stage'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# The hosted release is an immutable source-SHA directory, so its executable
# must identify that same protected source rather than the development default
# (0.0.1-scaffold).  Stamp both the legacy displayed version and buildinfo,
# which is what `kitsoki version` and internal receipts respectively expose.
(cd "$ROOT" && GOOS=linux GOARCH=amd64 GOCACHE="$GOCACHE" go build \
	-ldflags "-X main.version=$KITSOKI_SHA -X kitsoki/internal/buildinfo.Revision=$KITSOKI_SHA -X kitsoki/internal/buildinfo.RevisionShort=${KITSOKI_SHA:0:12}" \
	-o "$local_stage/kitsoki" ./cmd/kitsoki)
git -C "$POG_ROOT" bundle create "$local_stage/pog.bundle" main
# Bundle each federated portfolio member from its declared source and ref, and
# record it in members.manifest for install.sh. A declared ref that lacks
# pog/catalog.yaml is a hard error: deploying it would silently drop that
# product from the hosted catalog — the very failure this whole change fixes.
: >"$local_stage/members.manifest"
for member_entry in "${HOSTED_MEMBERS[@]}"; do
	IFS=: read -r member_id member_dir member_root member_ref <<<"$member_entry"
	[ -n "$member_id" ] && [ -n "$member_dir" ] && [ -n "$member_root" ] && [ -n "$member_ref" ] \
		|| { echo "malformed HOSTED_MEMBERS entry: $member_entry" >&2; exit 2; }
	[ -d "$member_root/.git" ] || { echo "member $member_id: not a git checkout at $member_root" >&2; exit 2; }
	git -C "$member_root" cat-file -e "$member_ref:pog/catalog.yaml" 2>/dev/null \
		|| { echo "member $member_id: ref '$member_ref' has no pog/catalog.yaml in $member_root" >&2; exit 2; }
	member_sha="$(git -C "$member_root" rev-parse "$member_ref^{commit}")"
	git -C "$member_root" bundle create "$local_stage/member-$member_dir.bundle" "$member_ref"
	printf '%s %s %s\n' "$member_dir" "$member_sha" "$member_id" >>"$local_stage/members.manifest"
	echo "  federated member $member_id <- $member_root@$member_ref ($member_sha)"
done
cp "$ROOT"/deploy/hosted-pog/{Caddyfile,kitsoki-queue-admission.service,kitsoki-queue-worker-admission.conf,kitsoki-queue-worker-hosted-engine.conf,kitsoki-pog-integration-current-worker.service,link-capsule-state.sh,hosted-pog.yaml,import-legacy-worker-ships.sh,install.sh,kitsoki-pog.service,node-runtime.env,pog-capsule-state.service,pog-colony-runner-portfolio.conf,pog-portal.service,pog-worker-finalizer.service,pog-worker-finalizer.timer,prune-releases.sh,state-content-digest.mjs,wait-for-postgres.sh} "$local_stage/"
# POG's compatibility bridge still delegates lifecycle verbs to the checked-in
# helper. Ship that exact-revision helper beside the immutable hosted binary;
# the orchestrator must never require a mutable source checkout merely to close
# or inspect durable Capsule state.
cp "$ROOT/scripts/dev-workspace.sh" "$local_stage/kitsoki-dev-workspace.sh"
chmod 0755 "$local_stage/kitsoki-dev-workspace.sh"
# The client secret travels inside the 0700 stage directories (local mktemp,
# remote install -d) instead of the ssh argv, which would be visible in ps.
printf '%s\n' "$GH_CLIENT_SECRET" >"$local_stage/gh-client-secret"
chmod 0600 "$local_stage/gh-client-secret"
# Non-secret db backend selection travels as a plain KEY=VALUE file install.sh
# sources, the same convention as node-runtime.env. The password is a
# separate 0600 file for the same reason the GH client secret is: it must
# never be an install.sh positional argument (visible in ps/journal) or a
# rendered unit value.
{
	printf 'KITSOKI_HOSTED_POG_DB_BACKEND=%s\n' "$DB_BACKEND"
	if [ "$DB_BACKEND" = postgres ]; then
		printf 'KITSOKI_HOSTED_POG_PG_HOST=%s\n' "$PG_HOST"
		printf 'KITSOKI_HOSTED_POG_PG_PORT=%s\n' "$PG_PORT"
		printf 'KITSOKI_HOSTED_POG_PG_DATABASE=%s\n' "$PG_DATABASE"
		printf 'KITSOKI_HOSTED_POG_PG_ROLE=%s\n' "$PG_ROLE"
		printf 'KITSOKI_HOSTED_POG_PG_SSLMODE=%s\n' "$PG_SSLMODE"
	fi
} >"$local_stage/hosted-pog-db.env"
printf '%s' "$PG_PASSWORD" >"$local_stage/pg-password"
chmod 0600 "$local_stage/pg-password"
state_mode="preserve"
if [ "$sync_local_state" -eq 1 ]; then
	[ -x "$STATE_PACKAGER" ] || { echo "missing hosted POG state packager: $STATE_PACKAGER" >&2; exit 2; }
	"$STATE_PACKAGER" "$POG_ROOT" "$pog_sha" "$local_stage"
	state_mode="sync"
fi
[ "$(git -C "$POG_ROOT" rev-parse "$POG_REF^{commit}")" = "$pog_sha" ] || {
	echo "POG $POG_REF advanced while the release/state snapshot was being prepared; rerun so source and state share one declared revision" >&2
	exit 1
}
curl --fail --location --silent --show-error --retry 3 --output "$local_stage/$NODE_ARCHIVE" "$NODE_URL"
printf '%s  %s\n' "$NODE_SHA256" "$local_stage/$NODE_ARCHIVE" | shasum -a 256 -c - >/dev/null

ssh "$REMOTE" "install -d -m 0700 '$remote_stage'"
scp "$local_stage"/* "$REMOTE:$remote_stage/"
local_binary_sha="$(shasum -a 256 "$local_stage/kitsoki" | awk '{print $1}')"
remote_binary_sha="$(ssh "$REMOTE" "sha256sum '$remote_stage/kitsoki' | awk '{print \$1}'")"
[ "$local_binary_sha" = "$remote_binary_sha" ] || { echo "uploaded Kitsoki binary checksum mismatch" >&2; exit 1; }

ssh "$REMOTE" "KITSOKI_HOSTED_POG_ADMIN='$ADMIN' bash '$remote_stage/install.sh' '$pog_sha' '$KITSOKI_SHA' '${PUBLIC_BASE_URL%/}' '$GH_CLIENT_ID' '$state_mode'"
verify
