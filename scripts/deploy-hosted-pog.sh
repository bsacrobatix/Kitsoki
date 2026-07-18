#!/usr/bin/env bash
# Deploy the POG portal beside the hosted GitHub agent and put all POG-owned
# HTTP routes behind Kitsoki's invitation-only GitHub session gate.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REMOTE="${KITSOKI_GH_AGENT_REMOTE:-}"
PUBLIC_BASE_URL="${KITSOKI_GH_AGENT_PUBLIC_BASE_URL:-}"
POG_ROOT="${KITSOKI_HOSTED_POG_ROOT:-$HOME/code/POG}"
POG_REF="${KITSOKI_HOSTED_POG_REF:-main}"
ADMIN="${KITSOKI_HOSTED_POG_ADMIN:-bsacrobatix}"
GOCACHE="${GOCACHE:-/private/tmp/kitsoki-gocache}"

mode="dry-run"
case "${1:-}" in
	"") ;;
	--yes) mode="deploy" ;;
	--verify) mode="verify" ;;
	*) echo "usage: scripts/deploy-hosted-pog.sh [--yes|--verify]" >&2; exit 2 ;;
esac

[ -n "$REMOTE" ] || { echo "KITSOKI_GH_AGENT_REMOTE is required" >&2; exit 2; }
[ -n "$PUBLIC_BASE_URL" ] || { echo "KITSOKI_GH_AGENT_PUBLIC_BASE_URL is required" >&2; exit 2; }
[[ "$PUBLIC_BASE_URL" =~ ^https://[A-Za-z0-9.-]+/?$ ]] || { echo "public base URL must be an https origin with no path" >&2; exit 2; }
[[ "$ADMIN" =~ ^[A-Za-z0-9-]+$ ]] || { echo "KITSOKI_HOSTED_POG_ADMIN is not a valid GitHub login" >&2; exit 2; }

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
	# rejected directly. The OAuth entrypoint and signed-webhook transport are
	# the only deliberate protocol exceptions.
	expect_public_status 302 / -H 'Accept: text/html'
	expect_public_status 401 /assets/access-probe.js
	expect_public_status 401 /api/catalog
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
	expect_public_status 401 /gh-agent/webhook -X POST -H 'Content-Type: application/json' --data '{}'
	ssh "$REMOTE" 'set -eu; systemctl is-active --quiet kitsoki-gh-agent caddy kitsoki-pog pog-portal; test "$(curl -sS -o /dev/null -w "%{http_code}" http://127.0.0.1:7777/auth/me)" = 401; curl -fsS -o /dev/null http://127.0.0.1:5183/api/catalog; curl -fsS -o /dev/null http://127.0.0.1:8787/healthz; caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null'
	echo "hosted-pog verify: services active; anonymous route-family matrix denied; OAuth entrypoint reachable; unsigned webhook denied; loopback health=ok"
}

if [ "$mode" = "verify" ]; then
	verify
	exit 0
fi

[ -d "$POG_ROOT/.git" ] || { echo "POG checkout is missing at $POG_ROOT" >&2; exit 2; }
KITSOKI_SHA="$(git -C "$ROOT" rev-parse HEAD)"
git -C "$ROOT" merge-base --is-ancestor "$KITSOKI_SHA" main || { echo "Kitsoki HEAD ($KITSOKI_SHA) is not contained in Kitsoki main" >&2; exit 2; }
pog_sha="$(git -C "$POG_ROOT" rev-parse "$POG_REF^{commit}")"
git -C "$POG_ROOT" merge-base --is-ancestor "$pog_sha" main || { echo "$POG_REF ($pog_sha) is not contained in POG main" >&2; exit 2; }
git -C "$POG_ROOT" show "$pog_sha:portal/vite.config.ts" | grep -q 'POG_KITSOKI_BROWSER_URL' || {
	echo "POG $pog_sha does not support the separate hosted browser URL; promote the hosted-POG compatibility change first" >&2
	exit 2
}

cat <<EOF
deploy-hosted-pog:
  kitsoki source: $ROOT ($KITSOKI_SHA)
  POG source:     $POG_ROOT ($POG_REF -> $pog_sha)
  remote:         $REMOTE
  public URL:     ${PUBLIC_BASE_URL%/}
  GitHub admin:   $ADMIN
  topology:       Caddy -> Kitsoki /auth/check -> POG 127.0.0.1:5183
  unchanged:      signed GitHub webhook, agent health/run/deck routes
EOF

if [ "$mode" = "dry-run" ]; then
	cat <<'EOF'

dry run only. The remote must already contain /etc/kitsoki/hosted-pog.env
with KITSOKI_GH_APP_CLIENT_ID and KITSOKI_GH_APP_CLIENT_SECRET. Re-run with
--yes to build, upload, activate, and verify; use --verify for read-only checks.
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

GOOS=linux GOARCH=amd64 GOCACHE="$GOCACHE" go build -o "$local_stage/kitsoki" ./cmd/kitsoki
git -C "$POG_ROOT" bundle create "$local_stage/pog.bundle" main
cp "$ROOT"/deploy/hosted-pog/{Caddyfile,hosted-pog.yaml,install.sh,kitsoki-pog.service,pog-portal.service} "$local_stage/"

ssh "$REMOTE" "install -d -m 0700 '$remote_stage'"
scp "$local_stage"/* "$REMOTE:$remote_stage/"
local_binary_sha="$(shasum -a 256 "$local_stage/kitsoki" | awk '{print $1}')"
remote_binary_sha="$(ssh "$REMOTE" "sha256sum '$remote_stage/kitsoki' | awk '{print \$1}'")"
[ "$local_binary_sha" = "$remote_binary_sha" ] || { echo "uploaded Kitsoki binary checksum mismatch" >&2; exit 1; }

ssh "$REMOTE" "KITSOKI_HOSTED_POG_ADMIN='$ADMIN' bash '$remote_stage/install.sh' '$pog_sha' '$KITSOKI_SHA' '${PUBLIC_BASE_URL%/}'"
verify
