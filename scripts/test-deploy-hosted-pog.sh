#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
deploy="$root/scripts/deploy-hosted-pog.sh"
packager="$root/scripts/package-hosted-pog-state.sh"
assets="$root/deploy/hosted-pog"
digest_tool="$assets/state-content-digest.mjs"
legacy_ship_importer="$assets/import-legacy-worker-ships.sh"
capsule_state_linker="$assets/link-capsule-state.sh"

bash -n "$deploy" "$packager" "$assets/install.sh" "$legacy_ship_importer" "$capsule_state_linker" "$assets/prune-releases.sh"
node --check "$digest_tool"

for required in \
  "$assets/Caddyfile" \
  "$assets/hosted-pog.yaml" \
  "$assets/kitsoki-pog.service" \
  "$assets/node-runtime.env" \
  "$assets/pog-portal.service" \
  "$assets/prune-releases.sh" \
	"$assets/pog-capsule-state.service" \
	"$assets/pog-colony-runner-portfolio.conf" \
	"$assets/pog-worker-finalizer.service" \
	"$assets/pog-worker-finalizer.timer" \
	"$assets/kitsoki-queue-admission.service" \
	"$assets/kitsoki-queue-worker-admission.conf" \
	"$assets/kitsoki-queue-worker-hosted-engine.conf" \
	"$digest_tool" \
	"$legacy_ship_importer" \
	"$capsule_state_linker" \
  "$packager"; do
  [ -f "$required" ] || { echo "missing hosted POG deployment asset: $required" >&2; exit 1; }
done

grep -q 'forward_auth 127.0.0.1:7778' "$assets/Caddyfile"
grep -q 'uri /auth/check' "$assets/Caddyfile"
grep -A3 -q 'handle /gh-agent/webhook.*reverse_proxy 127.0.0.1:8787' "$assets/Caddyfile" || {
  sed -n '/handle \/gh-agent\/webhook/,/}/p' "$assets/Caddyfile" | grep -q 'reverse_proxy 127.0.0.1:8787'
}
sed -n '/handle @gh_agent/,/}/p' "$assets/Caddyfile" | grep -q 'import pog_login'
sed -n '/handle \/decks\/\*/,/}/p' "$assets/Caddyfile" | grep -q 'import pog_login'
grep -q '@hosted_feedback path /api/feedback' "$assets/Caddyfile"
! grep -q 'handle /api/feedback\*' "$assets/Caddyfile"
grep -q 'reverse_proxy 127.0.0.1:7777' "$assets/Caddyfile"
! grep -q '127.0.0.1:5183' "$assets/Caddyfile"
grep -q 'mode: required' "$assets/hosted-pog.yaml"
grep -q 'client_id: __GITHUB_CLIENT_ID__' "$assets/hosted-pog.yaml"
grep -qF 'client_secret: ${KITSOKI_HOSTED_POG_GH_CLIENT_SECRET}' "$assets/hosted-pog.yaml"
! grep -q 'device_flow' "$assets/hosted-pog.yaml"
grep -q 'EnvironmentFile=/etc/kitsoki/hosted-pog.env' "$assets/kitsoki-pog.service"
grep -q 'gh-client-secret' "$deploy"
grep -q 'chmod 0600 "$local_stage/gh-client-secret"' "$deploy"
grep -q "KITSOKI_HOSTED_POG_GH_CLIENT_SECRET=//p.*hosted-pog.env" "$deploy"
grep -q 'Repeat deployments should not require copying a root-only production secret' "$deploy"
grep -q 'install -m 0600 "$stage/hosted-pog.env" /etc/kitsoki/hosted-pog.env' "$assets/install.sh"
# Colony service-token contract: the yaml template names the env var (never a
# value), install.sh reuses-or-mints the token into the root-only env file,
# proves it authenticates via /auth/check, and mirrors it to the colony
# runner as POG_RUNNER_TOKEN through a root-only EnvironmentFile drop-in.
grep -q 'colony: KITSOKI_COLONY_TOKEN' "$assets/hosted-pog.yaml"
grep -q "KITSOKI_COLONY_TOKEN=//p' /etc/kitsoki/hosted-pog.env" "$assets/install.sh"
grep -q "printf 'KITSOKI_COLONY_TOKEN=%s" "$assets/install.sh"
grep -q 'Authorization: Bearer \$colony_token" http://127.0.0.1:7778/auth/check' "$assets/install.sh"
grep -q 'install -m 0600 "$stage/pog-colony-runner.env" /etc/kitsoki/pog-colony-runner.env' "$assets/install.sh"
grep -q 'pog-colony-runner.service.d/runner-token.conf' "$assets/install.sh"
grep -q 'POG_GEARS_RUST_SRC=/opt/pog/members/gears-rust' "$assets/install.sh"
grep -q 'POG_AGENT_RUNNER_DB=/var/lib/kitsoki-pog/sessions.db' "$assets/install.sh"
grep -q 'KITSOKI_SOURCE_DIR=/opt/kitsoki-hosted-pog/current' "$assets/install.sh"
! grep -q 'KITSOKI_SOURCE_DIR=/opt/kitsoki-src' "$assets/install.sh"
grep -q 'kitsoki-dev-workspace.sh' "$deploy"
grep -q 'kitsoki-dev-workspace.sh' "$assets/install.sh"
grep -q "printf 'POG_KITSOKI_BIN=%s.*hosted_engine" "$assets/install.sh"
# The colony dispatches federation proposals itself. Its non-secret authority
# is a distinct drop-in rendered from the same roots/member set as the portal;
# existing secret/provider/state drop-ins remain separately owned.
grep -q 'Environment=POG_PORTFOLIO_ROOT=/opt/pog/current' "$assets/pog-colony-runner-portfolio.conf"
grep -q 'Environment=POG_MEMBER_ROOTS=__POG_MEMBER_ROOTS__' "$assets/pog-colony-runner-portfolio.conf"
grep -q 'Environment=POG_PORTFOLIO_MEMBERS=__POG_PORTFOLIO_MEMBERS__' "$assets/pog-colony-runner-portfolio.conf"
grep -q 'Environment=POG_KITSOKI_BIN=/opt/kitsoki-hosted-pog/current/kitsoki' "$assets/pog-colony-runner-portfolio.conf"
! grep -Eq 'TOKEN|SECRET|EnvironmentFile|UnsetEnvironment' "$assets/pog-colony-runner-portfolio.conf"
grep -q 'pog-colony-runner-portfolio.conf.rendered' "$assets/install.sh"
grep -q 'pog-colony-runner.service.d/portfolio-authority.conf' "$assets/install.sh"
grep -q 'previous_colony_portfolio' "$assets/install.sh"
grep -q 'previous_colony_env' "$assets/install.sh"
grep -q 'install -m 0600 "$previous_colony_env" /etc/kitsoki/pog-colony-runner.env' "$assets/install.sh"
grep -q 'colony_process_environment=' "$assets/install.sh"
grep -q 'colony_process_environment=' "$deploy"
grep -q 'colony runner lacks the hosted portfolio root authority' "$assets/install.sh"
grep -q 'colony runner lacks the rendered member-root authority' "$assets/install.sh"
grep -q 'colony runner lacks the rendered portfolio-member authority' "$assets/install.sh"
grep -q 'colony runner lacks the activated hosted Kitsoki engine' "$assets/install.sh"
grep -q 'pog-colony-runner-portfolio.conf' "$deploy"
grep -q 'HOSTED_MEMBER_ROOTS=' "$deploy"
grep -q 'HOSTED_PORTFOLIO_MEMBERS=' "$deploy"
grep -q 'pog-colony-runner.service.d/portfolio-authority.conf' "$deploy"
grep -q 'POG_MEMBER_ROOTS=\$expected_member_roots' "$deploy"
grep -q 'POG_PORTFOLIO_MEMBERS=\$expected_portfolio_members' "$deploy"
grep -q 'POG_KITSOKI_BROWSER_URL=' "$assets/pog-portal.service"
grep -q 'Environment=POG_MEMBER_ROOTS=__POG_MEMBER_ROOTS__' "$assets/pog-portal.service"
grep -q 'Environment=POG_PORTFOLIO_MEMBERS=__POG_PORTFOLIO_MEMBERS__' "$assets/pog-portal.service"
grep -q 'Environment=POG_GEARS_RUST_SRC=/opt/pog/members/gears-rust' "$assets/pog-portal.service"
! grep -q 'Environment=POG_PORTFOLIO_MEMBERS=pog,constructor-studio$' "$assets/pog-portal.service"
grep -q 'POG_KITSOKI_URL=http://127.0.0.1:7778' "$assets/pog-portal.service"
grep -q 'POG_AGENT_RUNNER_DB=/var/lib/kitsoki-pog/sessions.db' "$assets/pog-portal.service"
grep -q 'POG_STREAMS_DIR=/var/lib/pog/runtime/streams' "$assets/pog-portal.service"
grep -q 'portal/src/server/production.ts' "$deploy"
grep -Fq '(cd "$ROOT" && GOOS=linux GOARCH=amd64' "$deploy"
grep -q 'login-gated portal, API, agent health/run/deck, and evidence routes' "$deploy"
grep -q 'GitHub OAuth login endpoints and the HMAC-verified webhook only' "$deploy"
grep -q 'github.com/login/oauth/access_token' "$deploy"
grep -q 'bad_verification_code' "$deploy"
! grep -q 'github.com/login/device/code' "$deploy"
grep -q '/opt/kitsoki-hosted-pog/current/kitsoki' "$assets/kitsoki-pog.service"
grep -q '^KITSOKI_HOSTED_POG_NODE_VERSION=v[0-9]' "$assets/node-runtime.env"
grep -Eq '^KITSOKI_HOSTED_POG_NODE_SHA256=[0-9a-f]{64}$' "$assets/node-runtime.env"
grep -q 'nodejs.org/download/release/' "$assets/node-runtime.env"
grep -q 'shasum -a 256 -c' "$deploy"
grep -q 'sha256sum -c' "$assets/install.sh"
grep -q 'require(.*node:sqlite' "$assets/install.sh"
grep -q '/opt/kitsoki-hosted-pog/node/current/bin/node --enable-source-maps' "$assets/pog-portal.service"
grep -q 'server/server.mjs --addr 127.0.0.1:7777' "$assets/pog-portal.service"
! grep -Eq 'npm.*run dev|vite|5183' "$assets/pog-portal.service"
grep -q 'pog-portal.service.rendered' "$assets/install.sh"
grep -q 'Host: \$public_host' "$deploy"
grep -q 'const expected = process.argv\[1\]' "$assets/install.sh"
grep -Fq 'graph.federation?.unavailable' "$assets/install.sh"
grep -q 'products=$HOSTED_PRODUCTS_SORTED' "$deploy"
! grep -q 'exact products=pog,constructor-studio' "$deploy"
grep -q 'state mode must be preserve or sync' "$assets/install.sh"
grep -q 'local-state sync refused to overwrite divergent hosted state' "$assets/install.sh"
grep -q 'active_state_pristine' "$assets/install.sh"
grep -q 'state-content-digest.mjs' "$deploy"
grep -q 'runtime_current_changed' "$assets/install.sh"
grep -q 'ln -s.*runtime_current.*release/.artifacts' "$assets/install.sh"
grep -q 'import-legacy-worker-ships.sh' "$assets/install.sh"
grep -q 'import-legacy-worker-ships.sh' "$deploy"
grep -q 'api/feedback-autonomy/scoreboard' "$assets/install.sh"
grep -q 'autonomy scoreboard regressed from' "$assets/install.sh"
grep -q 'link-capsule-state.sh' "$assets/install.sh"
grep -q 'pog-capsule-state.service' "$assets/install.sh"
grep -q 'capsule-state.conf' "$assets/install.sh"
grep -q 'capsule_state_root=/var/lib/pog/capsules' "$assets/install.sh"
grep -q '\[ -L /opt/pog/current/.capsules \]' "$assets/install.sh"
grep -q 'Requires=.*pog-capsule-state.service' "$assets/pog-portal.service"
grep -q 'After=.*pog-capsule-state.service' "$assets/pog-portal.service"
grep -q 'Before=.*pog-colony-runner.service.*kitsoki-queue-worker.service' "$assets/pog-capsule-state.service"
grep -q 'ExecStart=/usr/local/libexec/kitsoki-hosted-pog-link-capsule-state' "$assets/pog-capsule-state.service"
grep -q 'ln -s "$state_root" "$target"' "$capsule_state_linker"
grep -q 'path is not empty; refusing implicit migration' "$capsule_state_linker"
grep -q 'busy; refusing forced unmount' "$capsule_state_linker"
grep -q 'colony_was_active' "$assets/install.sh"
grep -q '^[[:space:]]*systemctl restart pog-colony-runner.service$' "$assets/install.sh"
grep -q 'queue_worker_was_active' "$assets/install.sh"
grep -q 'pog-worker-finalizer.service' "$deploy"
grep -q 'pog-worker-finalizer.timer' "$deploy"
grep -q 'Requires=pog-capsule-state.service' "$assets/pog-worker-finalizer.service"
grep -q 'After=.*pog-capsule-state.service' "$assets/pog-worker-finalizer.service"
grep -q '^User=pog$' "$assets/pog-worker-finalizer.service"
grep -q 'WorkingDirectory=/opt/pog/current' "$assets/pog-worker-finalizer.service"
grep -q 'POG_KITSOKI_BIN=/opt/kitsoki-hosted-pog/current/kitsoki' "$assets/pog-worker-finalizer.service"
grep -q 'EnvironmentFile=/etc/kitsoki/queue-worker.env' "$assets/pog-worker-finalizer.service"
grep -q 'feedback-worker-finalizer.sh --project /opt/pog/current' "$assets/pog-worker-finalizer.service"
! grep -q 'pog-portal.service' "$assets/pog-worker-finalizer.service"
grep -q '^OnBootSec=' "$assets/pog-worker-finalizer.timer"
grep -q '^OnUnitActiveSec=' "$assets/pog-worker-finalizer.timer"
grep -q '^Persistent=true$' "$assets/pog-worker-finalizer.timer"
grep -q 'systemctl start pog-worker-finalizer.service' "$assets/install.sh"
grep -q 'enable --now pog-worker-finalizer.timer' "$assets/install.sh"
grep -q "sed -i '/\^\[\[:space:\]\]\*POG_KITSOKI_BIN=/d' /etc/kitsoki/queue-worker.env" "$assets/install.sh"
grep -q 'kitsoki-pog.service.d/\*.conf' "$assets/install.sh"
grep -q "sed -i '/\^\[\[:space:\]\]\*Environment=POG_KITSOKI_BIN=/d'" "$assets/install.sh"
grep -q 'kitsoki-pog.service.d/zz-hosted-engine.conf' "$assets/install.sh"
grep -q 'kitsoki-pog did not activate with the versioned hosted engine' "$assets/install.sh"
grep -q 'POG_KITSOKI_BIN=\$hosted_engine' "$deploy"
grep -q 'for unit in kitsoki-pog.service pog-portal.service pog-worker-finalizer.service' "$deploy"
grep -q 'prune-releases.sh' "$deploy"
grep -q '"$stage/prune-releases.sh" "$release_root" "$current" 2' "$assets/install.sh"
grep -q '"$stage/prune-releases.sh" "$kitsoki_release_root" "$kitsoki_current" 2' "$assets/install.sh"
grep -q 'kitsoki-pog.service.d/zz-hosted-engine.conf' "$deploy"
grep -q 'kitsoki-queue-worker-hosted-engine.conf' "$assets/install.sh"
grep -q 'ExecStart=/opt/kitsoki-hosted-pog/current/kitsoki queue worker' "$assets/kitsoki-queue-worker-hosted-engine.conf"
grep -Fq -- '--queue-root /var/lib/kitsoki-queue-admission/pog/queue' "$assets/kitsoki-queue-worker-hosted-engine.conf"
grep -Fq -- '--executor vm-pool' "$assets/kitsoki-queue-worker-hosted-engine.conf"
grep -Fq -- '--executor-pipeline change' "$assets/kitsoki-queue-worker-hosted-engine.conf"
! grep -Fq -- '--gate ' "$assets/kitsoki-queue-worker-hosted-engine.conf"
grep -q 'Environment=KITSOKI_SOURCE_DIR=/opt/kitsoki-hosted-pog/current' "$assets/kitsoki-queue-worker-hosted-engine.conf"
! grep -q 'Environment=KITSOKI_SOURCE_DIR=/opt/kitsoki-src' "$assets/kitsoki-queue-worker-hosted-engine.conf"
grep -q 'POG_GEARS_RUST_SRC=/opt/pog/members/gears-rust' "$assets/kitsoki-queue-worker-hosted-engine.conf"
grep -Fq 'queue_admission_root=/var/lib/kitsoki-queue-admission/pog' "$assets/install.sh"
grep -Fq 'install -d -o pog -g pog -m 0700 "$queue_admission_root"' "$assets/install.sh"
grep -Fq 'EnvironmentFile=/etc/kitsoki/queue-admission.env' "$assets/kitsoki-queue-admission.service"
grep -Fq -- '--listen 127.0.0.1:7444' "$assets/kitsoki-queue-admission.service"
grep -Fq -- '--root /var/lib/kitsoki-queue-admission/pog' "$assets/kitsoki-queue-admission.service"
grep -Fq -- '--project /opt/pog/releases/__POG_RELEASE_SHA__' "$assets/kitsoki-queue-admission.service"
grep -Fqx 'User=pog' "$assets/kitsoki-queue-admission.service"
grep -Fqx 'ReadWritePaths=/var/lib/kitsoki-queue-admission/pog' "$assets/kitsoki-queue-admission.service"
! grep -Eq 'KITSOKI_QUEUE_ADMISSION_TOKEN=.+[^}]' "$assets/kitsoki-queue-admission.service"
grep -Fqx 'Requires=kitsoki-queue-admission.service' "$assets/kitsoki-queue-worker-admission.conf"
grep -Fqx 'After=kitsoki-queue-admission.service' "$assets/kitsoki-queue-worker-admission.conf"
grep -Fq 'prepare_queue_admission_env' "$assets/install.sh"
grep -Fq 'rendered_queue_admission_service=' "$assets/install.sh"
grep -Fq '"$stage/kitsoki-queue-admission.service" >"$rendered_queue_admission_service"' "$assets/install.sh"
grep -Fq 'install -m 0644 "$rendered_queue_admission_service" /etc/systemd/system/kitsoki-queue-admission.service' "$assets/install.sh"
grep -Fq 'KITSOKI_QUEUE_ADMISSION_TOKEN' "$assets/install.sh"
grep -Fq 'queue_admission_bucket_url=https://kitsoki-test.sgp1.digitaloceanspaces.com' "$assets/install.sh"
grep -Fq 'queue_admission_key_env=DO_SPACES_KEY_ID' "$assets/install.sh"
grep -Fq 'queue_admission_secret_env=DO_KITSOKI_TEST_API_KEY' "$assets/install.sh"
grep -Fq 'KITSOKI_QUEUE_ADMISSION_BUCKET_URL=%s' "$assets/install.sh"
grep -Fq 'printf '\''%s=%s\\n'\'' "$queue_admission_key_env" "$access_key"' "$assets/install.sh"
grep -Fq -- '--bucket-url ${KITSOKI_QUEUE_ADMISSION_BUCKET_URL}' "$assets/kitsoki-queue-admission.service"
grep -Fq -- '--bucket-key-env DO_SPACES_KEY_ID' "$assets/kitsoki-queue-admission.service"
grep -Fq -- '--bucket-secret-env DO_KITSOKI_TEST_API_KEY' "$assets/kitsoki-queue-admission.service"
grep -Fq 'first admission install requires $queue_worker_env' "$assets/install.sh"
grep -Fq 'queue-admission environment must be root-owned mode 0600' "$assets/install.sh"
grep -Fq 'queue-worker environment must be root-owned mode 0600' "$assets/install.sh"
grep -Fq 'hosted queue-admission service did not become active' "$assets/install.sh"
grep -Fq 'queue-admission authentication probe returned' "$assets/install.sh"
grep -Fq 'for _ in $(seq 1 30); do' "$assets/install.sh"
grep -Fq 'unauthenticated probe returned 429; authentication must precede capacity' "$assets/install.sh"
grep -Fq 'kitsoki-queue-admission.service' "$deploy"
grep -Fq '127.0.0.1:7444' "$deploy"
grep -q 'zz-hosted-engine.conf' "$deploy"
grep -q '/opt/pog/members/gears-rust/pog/catalog.yaml' "$deploy"
grep -q 'queue_pid=' "$deploy"
grep -q 'readlink -f "/proc/\$queue_pid/exe"' "$deploy"
grep -q 'test -L /opt/pog/current/.capsules' "$deploy"
grep -q 'package-hosted-pog-state.sh' "$deploy"
grep -q -- '--sync-local-state' "$deploy"
grep -q 'previous_node_current' "$assets/install.sh"
grep -q 'npm.* run build' "$assets/install.sh"
grep -q 'node.*--check.*server/server.mjs' "$assets/install.sh"
grep -q 'POG_PORTFOLIO_MEMBERS="$portfolio_members"' "$assets/install.sh"
! grep -q 'products.join.*pog,constructor-studio' "$assets/install.sh"
! grep -q 'hosted catalog does not contain exactly POG and Constructor Studio' "$assets/install.sh"
grep -Fq 'POG_RUNNER_URL= \' "$assets/install.sh"
grep -q 'runuser -u pog -- git -C.*rev-parse HEAD' "$assets/install.sh"
grep -q 'runuser -u pog -- git -C.*update-ref refs/heads/main' "$assets/install.sh"
grep -q 'runuser -u pog -- git -C.*rev-parse main' "$assets/install.sh"
grep -q 'runuser -u pog -- git -C.*status --porcelain' "$assets/install.sh"
grep -q 'caddy validate' "$assets/install.sh"
grep -q 'expect_public_status 401 /decks/access-probe' "$deploy"
grep -q 'expect_public_status 401 /constructor-studio/decks/access-probe' "$deploy"
grep -q 'expect_public_status 401 /api/feedback-reports' "$deploy"
grep -q 'expect_public_status 401 /api/portal-health' "$deploy"
grep -q 'expect_public_status 401 /api/colony' "$deploy"
grep -q 'expect_public_status 401 /api/streams' "$deploy"
grep -q 'expect_public_status 401 /api/agent-runner/reaped-sessions' "$deploy"
grep -q 'expect_public_status 401 /api/feedback-reports' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/portal-health' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/colony' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/streams' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/agent-runner/reaped-sessions' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/run/access-probe' "$assets/install.sh"
grep -q 'expect_public_status 302 /auth/github/start' "$deploy"
grep -q 'expect_public_status 302 /auth/github/start' "$assets/install.sh"
grep -q 'expect_public_status 404 /auth/github/device/poll' "$deploy"
grep -q 'expect_public_status 404 /auth/github/device/poll' "$assets/install.sh"
grep -q '__GITHUB_CLIENT_ID__.*github_client_id' "$assets/install.sh"
grep -q 'curl -fsS http://127.0.0.1:8787/healthz' "$root/scripts/deploy-gh-agent.sh"
grep -q "ssh.*curl -fsS http://127.0.0.1:8787/healthz" "$root/scripts/collect-gh-agent-poc-evidence.sh"
grep -q 'restoring the prior release targets, service units, and Caddyfile' "$assets/install.sh"
grep -q 'previous_kitsoki_service' "$assets/install.sh"
grep -q 'previous_portal_service' "$assets/install.sh"
grep -q "ss -ltnH 'sport = :5183'" "$deploy"
grep -q 'no Vite command or 5183 listener' "$deploy"

fixture="$(mktemp -d "${TMPDIR:-/tmp}/kitsoki-hosted-pog-state-test.XXXXXX")"
cleanup() {
  rm -rf -- "$fixture"
}
trap cleanup EXIT

# Rendering is deterministic and leaves no unresolved authority placeholder.
expected_member_roots='Kitsoki=/opt/pog/members/Kitsoki,gears-rust=/opt/pog/members/gears-rust,studio-sassfully=/opt/pog/members/studio-sassfully,slidey=/opt/pog/members/slidey'
expected_portfolio_members='pog,constructor-studio,kitsoki,gears-rust,sassfully,slidey'
rendered_colony_portfolio="$fixture/pog-colony-runner-portfolio.conf"
sed \
  -e "s|__POG_MEMBER_ROOTS__|$expected_member_roots|g" \
  -e "s|__POG_PORTFOLIO_MEMBERS__|$expected_portfolio_members|g" \
  "$assets/pog-colony-runner-portfolio.conf" >"$rendered_colony_portfolio"
grep -Fq "Environment=POG_MEMBER_ROOTS=$expected_member_roots" "$rendered_colony_portfolio"
grep -Fq "Environment=POG_PORTFOLIO_MEMBERS=$expected_portfolio_members" "$rendered_colony_portfolio"
grep -Fq 'Environment=POG_KITSOKI_BIN=/opt/kitsoki-hosted-pog/current/kitsoki' "$rendered_colony_portfolio"
! grep -q '__POG_' "$rendered_colony_portfolio"

# Release retention is bounded and conservative: current plus two inactive
# rollback releases survive, older canonical releases are removed, and
# non-release directories are never treated as deletion candidates.
retention_root="$fixture/releases"
mkdir -p \
  "$retention_root/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  "$retention_root/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" \
  "$retention_root/cccccccccccccccccccccccccccccccccccccccc" \
  "$retention_root/dddddddddddddddddddddddddddddddddddddddd" \
  "$retention_root/manual-preserve"
touch -t 202601010101 "$retention_root/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
touch -t 202602010101 "$retention_root/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
touch -t 202603010101 "$retention_root/cccccccccccccccccccccccccccccccccccccccc"
touch -t 202604010101 "$retention_root/dddddddddddddddddddddddddddddddddddddddd"
ln -s "$retention_root/dddddddddddddddddddddddddddddddddddddddd" "$fixture/current"
"$assets/prune-releases.sh" "$retention_root" "$fixture/current" 2 >/dev/null
[ ! -e "$retention_root/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ]
[ -d "$retention_root/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ]
[ -d "$retention_root/cccccccccccccccccccccccccccccccccccccccc" ]
[ -d "$retention_root/dddddddddddddddddddddddddddddddddddddddd" ]
[ -d "$retention_root/manual-preserve" ]

# A deployment from the pre-runtime layout must retain immutable worker ship
# evidence before replacing the release-local .artifacts directory with the
# durable runtime symlink. The importer copies only the two scoreboard record
# shapes, is idempotent, and refuses a divergent destination instead of
# silently rewriting already-persisted evidence.
legacy="$fixture/legacy-artifacts"
durable="$fixture/durable-runtime"
mkdir -p \
  "$legacy/feedback/dispatch/worker-a" \
  "$legacy/feedback/dispatch/worker-b" \
  "$legacy/feedback/dispatch/unrelated" \
  "$durable/feedback/dispatch/worker-a"
printf '%s\n' '{"schema":"pog/feedback-autonomy/pog-bugfix-dispatch-result/v1","report_ref":"fb-a","shipped_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' \
  >"$legacy/feedback/dispatch/worker-a/pog-bugfix-result.json"
printf '%s\n' '{"schema":"pog/feedback-autonomy/pog-bugfix-landing/v1","report_ref":"fb-b","landing_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","shipped_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}' \
  >"$legacy/feedback/dispatch/worker-b/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.pog-bugfix-landing.json"
printf '%s\n' 'large mutable log must stay release-local' \
  >"$legacy/feedback/dispatch/unrelated/worker.log"
cp "$legacy/feedback/dispatch/worker-a/pog-bugfix-result.json" \
  "$durable/feedback/dispatch/worker-a/pog-bugfix-result.json"
"$legacy_ship_importer" "$legacy" "$durable" >/dev/null
cmp "$legacy/feedback/dispatch/worker-a/pog-bugfix-result.json" \
  "$durable/feedback/dispatch/worker-a/pog-bugfix-result.json"
cmp "$legacy/feedback/dispatch/worker-b/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.pog-bugfix-landing.json" \
  "$durable/feedback/dispatch/worker-b/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.pog-bugfix-landing.json"
[ ! -e "$durable/feedback/dispatch/unrelated/worker.log" ]
printf '%s\n' '{"divergent":true}' \
  >"$durable/feedback/dispatch/worker-a/pog-bugfix-result.json"
if "$legacy_ship_importer" "$legacy" "$durable" >/dev/null 2>&1; then
  echo "legacy worker ship importer overwrote divergent durable evidence" >&2
  exit 1
fi

mkdir -p \
  "$fixture/pog/.git" \
  "$fixture/pog/.artifacts/feedback/evidence" \
  "$fixture/pog/.artifacts/graph-mcp" \
  "$fixture/pog/.artifacts/streams" \
  "$fixture/pog/.artifacts/colony" \
  "$fixture/pog/.artifacts/agent-runner" \
  "$fixture/pog/.artifacts/bin"
printf '%s\n' '{"id":"feedback-local"}' >"$fixture/pog/.artifacts/feedback/feedback.jsonl"
printf '%s\n' '{"id":"feedback-graph"}' >"$fixture/pog/.artifacts/graph-mcp/feedback.jsonl"
printf '%s\n' '{"schema":"pog/stream/v1"}' >"$fixture/pog/.artifacts/streams/stream.json"
printf '%s\n' '{"campaigns":{}}' >"$fixture/pog/.artifacts/colony/state.json"
printf '%s\n' '99999' >"$fixture/pog/.artifacts/colony/runner.pid"
printf '%s\n' 'local log' >"$fixture/pog/.artifacts/colony/runner-watch.log"
printf '%s\n' 'must not upload' >"$fixture/pog/.artifacts/bin/private-cache"
sqlite3 "$fixture/pog/.artifacts/agent-runner/sessions.db" \
  'CREATE TABLE sessions (id TEXT PRIMARY KEY); INSERT INTO sessions VALUES ("session-local");'
mkdir -p "$fixture/out" "$fixture/out-repeat" "$fixture/unpacked"
"$packager" "$fixture/pog" 0123456789012345678901234567890123456789 "$fixture/out" >/dev/null
sleep 1
"$packager" "$fixture/pog" 0123456789012345678901234567890123456789 "$fixture/out-repeat" >/dev/null
[ "$(tar -xOzf "$fixture/out-repeat/pog-state.tar.gz" ./manifest.json | jq -r .content_sha256)" = \
  "$(tar -xOzf "$fixture/out/pog-state.tar.gz" ./manifest.json | jq -r .content_sha256)" ]
printf '%s  %s\n' "$(cat "$fixture/out/pog-state.sha256")" "$fixture/out/pog-state.tar.gz" | shasum -a 256 -c - >/dev/null
tar -xzf "$fixture/out/pog-state.tar.gz" -C "$fixture/unpacked"
jq -e '
  .schema == "kitsoki/hosted-pog-local-state/v1" and
  .pog_sha == "0123456789012345678901234567890123456789" and
  (.content_sha256 | test("^[0-9a-f]{64}$")) and
  .file_count == 5
' "$fixture/unpacked/manifest.json" >/dev/null
[ -f "$fixture/unpacked/feedback/feedback.jsonl" ]
[ -f "$fixture/unpacked/graph-mcp/feedback.jsonl" ]
[ -f "$fixture/unpacked/streams/stream.json" ]
[ -f "$fixture/unpacked/colony/state.json" ]
[ -f "$fixture/unpacked/agent-runner/sessions.db" ]
[ ! -e "$fixture/unpacked/colony/runner.pid" ]
[ ! -e "$fixture/unpacked/colony/runner-watch.log" ]
[ ! -e "$fixture/unpacked/bin/private-cache" ]
[ "$(sqlite3 "$fixture/unpacked/agent-runner/sessions.db" 'SELECT id FROM sessions;')" = "session-local" ]
expected_content_digest="$(jq -r .content_sha256 "$fixture/unpacked/manifest.json")"
[ "$(node "$digest_tool" "$fixture/unpacked" manifest.json 0123456789012345678901234567890123456789)" = "$expected_content_digest" ]
mv "$fixture/unpacked/manifest.json" "$fixture/unpacked/.hosted-pog-local-state.json"
printf '%s\n' "$expected_content_digest" >"$fixture/unpacked/.hosted-pog-local-state.sha256"
[ "$(node "$digest_tool" "$fixture/unpacked" .hosted-pog-local-state.json)" = "$expected_content_digest" ]
touch "$fixture/unpacked/agent-runner/sessions.db-wal"
printf '%032d' 0 >"$fixture/unpacked/agent-runner/sessions.db-shm"
[ "$(node "$digest_tool" "$fixture/unpacked" .hosted-pog-local-state.json)" = "$expected_content_digest" ]
printf '%s\n' 'hosted WAL mutation' >"$fixture/unpacked/agent-runner/sessions.db-wal"
if node "$digest_tool" "$fixture/unpacked" .hosted-pog-local-state.json >/dev/null 2>&1; then
  echo "digest tool accepted a non-empty hosted SQLite WAL" >&2
  exit 1
fi
: >"$fixture/unpacked/agent-runner/sessions.db-wal"
printf '%s\n' 'hosted mutation' >>"$fixture/unpacked/feedback/feedback.jsonl"
if node "$digest_tool" "$fixture/unpacked" .hosted-pog-local-state.json >/dev/null 2>&1; then
  echo "digest tool accepted divergent hosted state" >&2
  exit 1
fi

trap - EXIT
cleanup

echo "hosted POG deployment assets: OK"
