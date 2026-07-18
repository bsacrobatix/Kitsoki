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
grep -q 'KITSOKI_GH_APP_CLIENT_SECRET' "$assets/hosted-pog.yaml"
grep -q 'POG_KITSOKI_BROWSER_URL=' "$assets/pog-portal.service"
grep -q "show.*POG_KITSOKI_BROWSER_URL" "$deploy"
grep -q 'login-gated portal, API, agent health/run/deck, and evidence routes' "$deploy"
grep -q 'OAuth endpoints and the HMAC-verified GitHub webhook only' "$deploy"
grep -q '/opt/kitsoki-hosted-pog/current/kitsoki' "$assets/kitsoki-pog.service"
grep -q 'npm.* run build' "$assets/install.sh"
grep -q 'caddy validate' "$assets/install.sh"
grep -q 'expect_public_status 401 /decks/access-probe' "$deploy"
grep -q 'expect_public_status 401 /constructor-studio/decks/access-probe' "$deploy"
grep -q 'expect_public_status 401 /api/run/access-probe' "$assets/install.sh"
grep -q 'curl -fsS http://127.0.0.1:8787/healthz' "$root/scripts/deploy-gh-agent.sh"
grep -q "ssh.*curl -fsS http://127.0.0.1:8787/healthz" "$root/scripts/collect-gh-agent-poc-evidence.sh"
grep -q 'restoring the prior release targets and Caddyfile' "$assets/install.sh"

echo "hosted POG deployment assets: OK"
