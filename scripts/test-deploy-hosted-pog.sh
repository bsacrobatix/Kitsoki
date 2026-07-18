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
grep -q 'handle /api/feedback\*' "$assets/Caddyfile"
grep -q 'reverse_proxy 127.0.0.1:5183' "$assets/Caddyfile"
grep -q 'mode: required' "$assets/hosted-pog.yaml"
grep -q 'KITSOKI_GH_APP_CLIENT_SECRET' "$assets/hosted-pog.yaml"
grep -q 'POG_KITSOKI_BROWSER_URL=' "$assets/pog-portal.service"
grep -q '/opt/kitsoki-hosted-pog/current/kitsoki' "$assets/kitsoki-pog.service"
grep -q 'npm.* run build' "$assets/install.sh"
grep -q 'caddy validate' "$assets/install.sh"
grep -q 'restoring the prior release targets and Caddyfile' "$assets/install.sh"

echo "hosted POG deployment assets: OK"
