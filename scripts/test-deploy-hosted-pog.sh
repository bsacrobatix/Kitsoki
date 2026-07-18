#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
deploy="$root/scripts/deploy-hosted-pog.sh"
assets="$root/deploy/hosted-pog"

bash -n "$deploy" "$assets/install.sh"

for required in \
  "$assets/Caddyfile" \
  "$assets/hosted-pog.yaml" \
  "$assets/kitsoki-pog.service" \
  "$assets/node-runtime.env" \
  "$assets/pog-portal.service"; do
  [ -f "$required" ] || { echo "missing hosted POG deployment asset: $required" >&2; exit 1; }
done

grep -q 'forward_auth 127.0.0.1:7777' "$assets/Caddyfile"
grep -q 'uri /auth/check' "$assets/Caddyfile"
grep -A3 -q 'handle /gh-agent/webhook.*reverse_proxy 127.0.0.1:8787' "$assets/Caddyfile" || {
  sed -n '/handle \/gh-agent\/webhook/,/}/p' "$assets/Caddyfile" | grep -q 'reverse_proxy 127.0.0.1:8787'
}
sed -n '/handle @gh_agent/,/}/p' "$assets/Caddyfile" | grep -q 'import pog_login'
sed -n '/handle \/decks\/\*/,/}/p' "$assets/Caddyfile" | grep -q 'import pog_login'
grep -q 'handle /api/feedback\*' "$assets/Caddyfile"
grep -q 'reverse_proxy 127.0.0.1:5183' "$assets/Caddyfile"
grep -q 'mode: required' "$assets/hosted-pog.yaml"
grep -q 'client_id: __GITHUB_CLIENT_ID__' "$assets/hosted-pog.yaml"
grep -q 'device_flow: true' "$assets/hosted-pog.yaml"
! grep -q 'client_secret' "$assets/hosted-pog.yaml"
! grep -q 'EnvironmentFile=' "$assets/kitsoki-pog.service"
grep -q 'POG_KITSOKI_BROWSER_URL=' "$assets/pog-portal.service"
grep -q "show.*POG_KITSOKI_BROWSER_URL" "$deploy"
grep -q 'login-gated portal, API, agent health/run/deck, and evidence routes' "$deploy"
grep -q 'GitHub Device Flow endpoints and the HMAC-verified webhook only' "$deploy"
grep -q 'github.com/login/device/code' "$deploy"
grep -q '/opt/kitsoki-hosted-pog/current/kitsoki' "$assets/kitsoki-pog.service"
grep -q '^KITSOKI_HOSTED_POG_NODE_VERSION=v[0-9]' "$assets/node-runtime.env"
grep -Eq '^KITSOKI_HOSTED_POG_NODE_SHA256=[0-9a-f]{64}$' "$assets/node-runtime.env"
grep -q 'nodejs.org/download/release/' "$assets/node-runtime.env"
grep -q 'shasum -a 256 -c' "$deploy"
grep -q 'sha256sum -c' "$assets/install.sh"
grep -q 'require(.*node:sqlite' "$assets/install.sh"
grep -q '/opt/kitsoki-hosted-pog/node/current/bin/npm' "$assets/pog-portal.service"
grep -q 'previous_node_current' "$assets/install.sh"
grep -q 'npm.* run build' "$assets/install.sh"
grep -q 'runuser -u pog -- git -C.*rev-parse HEAD' "$assets/install.sh"
grep -q 'runuser -u pog -- git -C.*status --porcelain' "$assets/install.sh"
grep -q 'caddy validate' "$assets/install.sh"
grep -q 'expect_public_status 401 /decks/access-probe' "$deploy"
grep -q 'expect_public_status 401 /constructor-studio/decks/access-probe' "$deploy"
grep -q 'expect_public_status 401 /api/run/access-probe' "$assets/install.sh"
grep -q 'expect_public_status 410 /auth/github/device/poll' "$deploy"
grep -q 'expect_public_status 410 /auth/github/device/poll' "$assets/install.sh"
grep -q '__GITHUB_CLIENT_ID__.*github_client_id' "$assets/install.sh"
grep -q 'curl -fsS http://127.0.0.1:8787/healthz' "$root/scripts/deploy-gh-agent.sh"
grep -q "ssh.*curl -fsS http://127.0.0.1:8787/healthz" "$root/scripts/collect-gh-agent-poc-evidence.sh"
grep -q 'restoring the prior release targets and Caddyfile' "$assets/install.sh"

echo "hosted POG deployment assets: OK"
