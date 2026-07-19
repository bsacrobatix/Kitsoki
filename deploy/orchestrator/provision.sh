#!/usr/bin/env bash
# deploy/orchestrator/provision.sh — idempotent bootstrap for a fresh Ubuntu
# 22.04 droplet into the persistent "orchestrator" host described in
# deploy/orchestrator/README.md: `kitsoki daemon` (systemd --user service under
# a dedicated system user) plus `kitsoki queue worker` (systemd system
# service). It does NOT clone the project the queue worker will operate on
# (e.g. POG) — that transfer is a runbook step
# (docs/runbooks/pog-orchestrator-cutover.md) done once secrets and DNS are
# ready. Safe to re-run: every step checks current state before acting.
#
# Usage:
#   sudo deploy/orchestrator/provision.sh --source-checkout <path> [options]
#
# Required:
#   --source-checkout <path>   A kitsoki git checkout already present on this
#                              host (e.g. cloned from a bundle/tarball before
#                              running this script). Used to run its own
#                              scripts/setup.sh (git/make/Node/Go/pnpm), to
#                              build the binary (unless --kitsoki-bin is
#                              given), and as the source of the unit files
#                              this script installs.
#
# Optional:
#   --kitsoki-bin <path>       Prebuilt linux/<arch> kitsoki binary to install
#                              instead of running `make install` from
#                              --source-checkout. scripts/setup.sh still runs
#                              (Node is a runtime dependency of the --gate
#                              command, independent of how kitsoki itself was
#                              built).
#   --user <name>              System user that owns the daemon/worker
#                              (default: kitsoki).
#   --project-root <path>      Directory the queue worker's --project points
#                              at (default: /var/lib/<user>/pog). Created
#                              empty and chowned to --user; populating it with
#                              the actual project checkout is a runbook step.
#   --target <ref>             Protected destination ref (default: main).
#   --gate <command>           Deterministic gate command (default:
#                              "node scripts/kitsoki-ci.mjs", matching POG's
#                              proven launchd worker).
#   --concurrency <N>          Initial CONCURRENCY value (default: 1).
#   --daemon-addr <host:port>  kitsoki daemon --addr (default: 127.0.0.1:7777,
#                              loopback-only; nothing in this script exposes
#                              it publicly).
#   --install-dir <path>       Where the kitsoki binary is installed
#                              (default: /usr/local/bin, matching the systemd
#                              units' hardcoded ExecStart path).
#   --skip-services            Render/install units and env-file templates but
#                              do not enable or start them (review before
#                              secrets exist).
#
# What it does NOT do (by design, kept out of a no-spend bootstrap script):
#   - populate /etc/kitsoki/{queue-worker,daemon}.env with real secrets
#   - clone the project checkout the queue worker will operate on
#   - configure DNS, a reverse proxy, or public ingress
# All three are explicit, verified steps in
# docs/runbooks/pog-orchestrator-cutover.md.
set -euo pipefail

die() {
	echo "provision.sh: $*" >&2
	exit 1
}

[ "$(id -u)" -eq 0 ] || die "must run as root (sudo deploy/orchestrator/provision.sh ...)"

source_checkout=""
kitsoki_bin=""
kitsoki_user="kitsoki"
project_root=""
target="main"
gate="node scripts/kitsoki-ci.mjs"
concurrency=1
daemon_addr="127.0.0.1:7777"
install_dir="/usr/local/bin"
skip_services=0

while [ "$#" -gt 0 ]; do
	case "$1" in
		--source-checkout) source_checkout="$2"; shift 2 ;;
		--kitsoki-bin) kitsoki_bin="$2"; shift 2 ;;
		--user) kitsoki_user="$2"; shift 2 ;;
		--project-root) project_root="$2"; shift 2 ;;
		--target) target="$2"; shift 2 ;;
		--gate) gate="$2"; shift 2 ;;
		--concurrency) concurrency="$2"; shift 2 ;;
		--daemon-addr) daemon_addr="$2"; shift 2 ;;
		--install-dir) install_dir="$2"; shift 2 ;;
		--skip-services) skip_services=1; shift ;;
		-h|--help) sed -n '2,45p' "$0"; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done

[ -n "$source_checkout" ] || die "--source-checkout is required"
[ -d "$source_checkout/.git" ] || die "--source-checkout does not look like a git checkout: $source_checkout"
[ -f "$source_checkout/go.mod" ] || die "--source-checkout has no go.mod: $source_checkout"
[ -x "$source_checkout/scripts/setup.sh" ] || die "--source-checkout has no scripts/setup.sh: $source_checkout"
[ -f "$source_checkout/deploy/orchestrator/kitsoki-queue-worker.service" ] || die "--source-checkout is missing deploy/orchestrator unit files (wrong checkout or branch?)"
[[ "$kitsoki_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || die "invalid --user: $kitsoki_user"
[[ "$concurrency" =~ ^[0-9]+$ ]] && [ "$concurrency" -ge 1 ] || die "--concurrency must be a positive integer"
[ -n "$gate" ] || die "--gate must not be empty (kitsoki queue worker requires --gate)"
[ -z "$kitsoki_bin" ] || [ -x "$kitsoki_bin" ] || die "--kitsoki-bin is not an executable file: $kitsoki_bin"

home_dir="/var/lib/$kitsoki_user"
[ -n "$project_root" ] || project_root="$home_dir/pog"
case "$project_root" in
	"$home_dir"/*) ;;
	*) die "--project-root must live under $home_dir (see the queue-worker unit's StateDirectory/ProtectHome hardening notes): $project_root" ;;
esac

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

# --- 1. Base packages -------------------------------------------------------
log "installing base packages (git, make, curl, ca-certificates, jq)"
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y --no-install-recommends git make curl ca-certificates gnupg jq

# --- 2. Go / Node / pnpm via the repo's own setup script --------------------
# Reuses scripts/setup.sh instead of re-implementing Go-tarball/Node-apt
# install logic here a second time. Node is a runtime dependency of the --gate
# command (POG's gate runs `node scripts/kitsoki-ci.mjs`) independent of how
# kitsoki itself is installed below; Go is only load-bearing when building
# from source (no --kitsoki-bin), but installing it unconditionally keeps this
# host able to rebuild in place later without re-provisioning.
log "running $source_checkout/scripts/setup.sh (Go/Node/pnpm/git/make)"
( cd "$source_checkout" && ./scripts/setup.sh )
# scripts/setup.sh's tarball install only exports PATH within its own child
# process; make it visible here too so the `make install` build below (and any
# future manual rebuild) finds go without a fresh login shell.
[ -d /usr/local/go/bin ] && export PATH="/usr/local/go/bin:$PATH"
command -v go >/dev/null 2>&1 || [ -n "$kitsoki_bin" ] || die "go not on PATH after scripts/setup.sh and no --kitsoki-bin was given"
command -v node >/dev/null 2>&1 || die "node not on PATH after scripts/setup.sh (required at runtime by --gate)"
node_version="$(node --version)"
log "node $node_version, $(command -v go >/dev/null 2>&1 && go version || echo 'go: not installed (--kitsoki-bin mode)')"

# --- 3. System user ----------------------------------------------------------
if ! id -u "$kitsoki_user" >/dev/null 2>&1; then
	log "creating system user $kitsoki_user (home $home_dir)"
	useradd --system --home-dir "$home_dir" --create-home --shell /usr/sbin/nologin "$kitsoki_user"
else
	log "system user $kitsoki_user already exists"
fi
install -d -o "$kitsoki_user" -g "$kitsoki_user" -m 0750 "$home_dir" "$home_dir/.cache" "$home_dir/.config"
install -d -o "$kitsoki_user" -g "$kitsoki_user" -m 0750 "$project_root"

# loginctl enable-linger is what makes `systemctl --user` for a system account
# with no interactive login session start at boot and survive without one —
# required for `kitsoki daemon install-systemd`'s systemd --user unit to run
# unattended. See README.md "What each unit does".
if ! loginctl show-user "$kitsoki_user" -p Linger 2>/dev/null | grep -q 'Linger=yes'; then
	log "enabling systemd lingering for $kitsoki_user"
	loginctl enable-linger "$kitsoki_user"
fi
kitsoki_uid="$(id -u "$kitsoki_user")"
runtime_dir="/run/user/$kitsoki_uid"
for _ in $(seq 1 20); do
	[ -d "$runtime_dir" ] && systemctl is-active --quiet "user@$kitsoki_uid.service" && break
	systemctl start "user@$kitsoki_uid.service" >/dev/null 2>&1 || true
	sleep 0.5
done
[ -d "$runtime_dir" ] || die "user@$kitsoki_uid.service did not create $runtime_dir"

kitsoki_systemctl_user() {
	runuser -u "$kitsoki_user" -- env "HOME=$home_dir" "XDG_RUNTIME_DIR=$runtime_dir" systemctl --user "$@"
}
kitsoki_run() {
	runuser -u "$kitsoki_user" -- env "HOME=$home_dir" "$@"
}

# --- 4. Install the kitsoki binary ------------------------------------------
install -d -m 0755 "$install_dir"
if [ -n "$kitsoki_bin" ]; then
	log "installing prebuilt binary $kitsoki_bin -> $install_dir/kitsoki"
	install -m 0755 "$kitsoki_bin" "$install_dir/kitsoki"
else
	log "building kitsoki from $source_checkout (make install INSTALLDIR=$install_dir)"
	( cd "$source_checkout" && make install INSTALLDIR="$install_dir" )
fi
[ -x "$install_dir/kitsoki" ] || die "kitsoki did not end up executable at $install_dir/kitsoki"
"$install_dir/kitsoki" --version >/dev/null 2>&1 || log "warning: '$install_dir/kitsoki --version' did not exit 0 (continuing; some builds don't wire --version)"

# --- 5. kitsoki daemon: install its systemd --user unit ---------------------
# `kitsoki daemon install-systemd` (cmd/kitsoki/daemon_systemd.go) writes a
# systemd --user unit to $HOME/.config/systemd/user (os.UserConfigDir()) for
# whichever account runs it — it is not a system-wide unit and takes no
# --user flag of its own, so it must be invoked as $kitsoki_user via runuser,
# which is why lingering (above) is required for it to survive without a
# login session. --force makes re-running this script with different flags
# safe (it would otherwise refuse to overwrite a differing existing unit).
log "installing the kitsoki daemon's systemd --user unit for $kitsoki_user"
kitsoki_run "$install_dir/kitsoki" daemon install-systemd \
	--working-directory "$project_root" \
	--addr "$daemon_addr" \
	--db "$home_dir/daemon.sqlite" \
	--config .kitsoki.yaml \
	--force
kitsoki_systemctl_user daemon-reload

# --- 6. /etc/kitsoki env-file templates -------------------------------------
# Root-only (0600): systemd (running as root/PID1) reads these before
# dropping privileges to User=kitsoki/User=kitsoki for the two services below;
# the service processes themselves never get filesystem read access to the
# files, only the parsed environment. Never echo real secret values here —
# these are placeholder templates the operator fills in by hand (or via the
# cutover runbook's secrets-provisioning checklist), one time, out of band.
install -d -m 0755 /etc/kitsoki

queue_worker_env=/etc/kitsoki/queue-worker.env
if [ ! -e "$queue_worker_env" ]; then
	log "writing template $queue_worker_env"
	umask 077
	cat >"$queue_worker_env" <<EOF
# kitsoki-queue-worker.service EnvironmentFile — see deploy/orchestrator/README.md.
# CONCURRENCY is the single operator knob: edit it here, then
#   systemctl restart kitsoki-queue-worker
# All other values mirror POG's proven laptop launchd worker
# (ops/launchd/com.pog.kitsoki-queue.plist in the POG repo).
PROJECT_ROOT=$project_root
TARGET=$target
GATE=$gate
CONCURRENCY=$concurrency
RETRY_DELAY=5m
MAX_RETRY_DELAY=30m
MAX_ATTEMPTS=5
EOF
else
	log "$queue_worker_env already exists; leaving it as-is"
fi
chown root:root "$queue_worker_env"
chmod 0600 "$queue_worker_env"

daemon_env="$home_dir/.config/kitsoki/daemon.env"
if [ ! -e "$daemon_env" ]; then
	log "writing template $daemon_env"
	install -d -o "$kitsoki_user" -g "$kitsoki_user" -m 0750 "$home_dir/.config/kitsoki"
	umask 077
	cat >"$daemon_env" <<'EOF'
# kitsoki daemon EnvironmentFile (systemd --user, read via
# EnvironmentFile=-%h/.config/kitsoki/daemon.env in the rendered unit).
# Fill these in out of band (secrets manager / `systemctl --user edit`
# drop-in / manual `install -m 0600`) — never commit real values, never echo
# them in a script, never leave this file group- or world-readable.
#
# DO_KITSOKI_TEST_API_KEY — Spaces (S3-compatible) secret key for the
#   kitsoki-test.sgp1 bucket (internal/objectstore); paired with access key ID
#   DO801QYJLKD3UM7ZEUKE per .context/p0-persistent-vm-orchestrator-plan.md.
# DO_GAGNRENOUS_TURD_API_KEY — DigitalOcean API token for droplet
#   control (internal/capsule/vmpool: acquire/release ephemeral workers).
#
# DO_KITSOKI_TEST_API_KEY=
# DO_GAGNRENOUS_TURD_API_KEY=
EOF
	chown "$kitsoki_user:$kitsoki_user" "$daemon_env"
	chmod 0600 "$daemon_env"
else
	log "$daemon_env already exists; leaving it as-is"
fi
umask 022

# --- 7. Install the queue-worker system units -------------------------------
install -m 0644 "$source_checkout/deploy/orchestrator/kitsoki-queue-worker.service" /etc/systemd/system/kitsoki-queue-worker.service
install -m 0644 "$source_checkout/deploy/orchestrator/kitsoki-queue-worker@.service" "/etc/systemd/system/kitsoki-queue-worker@.service"
systemctl daemon-reload

if [ "$skip_services" -eq 1 ]; then
	log "units installed but --skip-services was given: not enabling or starting anything"
	log "next steps: fill in $queue_worker_env and $daemon_env with real secrets, then:"
	log "  systemctl enable --now kitsoki-queue-worker"
	log "  runuser -u $kitsoki_user -- env HOME=$home_dir XDG_RUNTIME_DIR=$runtime_dir systemctl --user enable --now kitsoki-daemon"
	exit 0
fi

# --- 8. Enable services ------------------------------------------------------
# Both services will crash-loop harmlessly (Restart=always, backing off under
# StartLimit*) until (a) real secrets replace the placeholders above and (b)
# a project checkout exists at $project_root with a .kitsoki.yaml — that is
# the cutover runbook's job, not this script's. Enabling now means the box
# comes up green the moment those two things land, with no extra step here.
log "enabling kitsoki-daemon (systemd --user, $kitsoki_user)"
kitsoki_systemctl_user enable --now kitsoki-daemon

log "enabling kitsoki-queue-worker (system)"
systemctl enable --now kitsoki-queue-worker

log "provisioning complete."
log "  kitsoki binary:   $install_dir/kitsoki"
log "  project root:     $project_root (empty — populate per the cutover runbook)"
log "  queue worker env: $queue_worker_env (placeholders only)"
log "  daemon env:       $daemon_env (placeholders only)"
log "  status:           systemctl status kitsoki-queue-worker"
log "                     runuser -u $kitsoki_user -- env HOME=$home_dir XDG_RUNTIME_DIR=$runtime_dir systemctl --user status kitsoki-daemon"
