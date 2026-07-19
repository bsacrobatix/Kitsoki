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
GH_CLIENT_ID="${KITSOKI_HOSTED_POG_GH_CLIENT_ID:-}"
GH_APP_PROFILE="${KITSOKI_HOSTED_POG_GH_APP_PROFILE:-$HOME/.config/kitsoki/gh-app/bsacrobatix-kitsoki-test/kitsoki.env}"
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
	expect_public_status 410 /auth/github/device/poll -X POST
	expect_public_status 401 /gh-agent/webhook -X POST -H 'Content-Type: application/json' --data '{}'
	login_page="$(curl -fsS "${PUBLIC_BASE_URL%/}/auth/login")"
	grep -q '/auth/github/device/start' <<<"$login_page"
	ssh "$REMOTE" "set -eu; systemctl is-active --quiet kitsoki-gh-agent caddy kitsoki-pog pog-portal; test \"\$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7777/auth/me)\" = 401; test -L /var/lib/pog/runtime; test -L /opt/pog/current/.artifacts; catalog=\"\$(curl -fsS -H 'Host: $PUBLIC_HOST' http://127.0.0.1:5183/api/catalog)\"; printf '%s' \"\$catalog\" | /opt/kitsoki-hosted-pog/node/current/bin/node -e 'const fs=require(\"node:fs\"); const graph=JSON.parse(fs.readFileSync(0,\"utf8\")); const products=(graph.comparison_catalogs??[]).map((entry)=>entry.id); const repos=[...new Set((graph.nodes??[]).map((node)=>node.attrs?.repo).filter(Boolean))].sort(); if(products.join(\",\")!==\"pog,constructor-studio\"||repos.join(\",\")!==\"constructor-studio,pog\"){console.error(JSON.stringify({products,repos}));process.exit(1)}'; curl -fsS -o /dev/null http://127.0.0.1:8787/healthz; caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null"
	echo "hosted-pog verify: services active; exact products=pog,constructor-studio; versioned runtime active; anonymous route-family matrix denied; GitHub Device Flow entrypoint reachable; unsigned webhook denied; public Host accepted by POG; loopback health=ok"
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
if ! device_probe="$(curl -sS -X POST -H 'Accept: application/json' -d "client_id=$GH_CLIENT_ID" https://github.com/login/device/code)"; then
	echo "could not probe GitHub Device Flow for the configured App client ID" >&2
	exit 1
fi
grep -q '"device_code"' <<<"$device_probe" || {
	echo "GitHub Device Flow is not enabled for the configured App client ID" >&2
	exit 1
}
unset device_probe

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
  GitHub login:   Device Flow using client ID from the local App profile
  Node runtime:   $NODE_VERSION (pinned official linux-x64 archive)
  topology:       Caddy -> Kitsoki /auth/check -> POG 127.0.0.1:5183
  products:       POG and Constructor Studio only
  local state:    $([ "$sync_local_state" -eq 1 ] && echo 'bounded portal-state snapshot enabled' || echo 'preserve hosted runtime (no local import)')
  access policy:  login-gated portal, API, agent health/run/deck, and evidence routes
  public protocol: GitHub Device Flow endpoints and the HMAC-verified webhook only
EOF

if [ "$mode" = "dry-run" ]; then
	cat <<'EOF'

dry run only. Re-run with --yes to build, upload, activate, and verify; use
--verify for read-only checks. Device Flow requires no client secret or OAuth
callback; the GitHub App must keep Device Flow enabled. Add
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

GOOS=linux GOARCH=amd64 GOCACHE="$GOCACHE" go build -o "$local_stage/kitsoki" ./cmd/kitsoki
git -C "$POG_ROOT" bundle create "$local_stage/pog.bundle" main
cp "$ROOT"/deploy/hosted-pog/{Caddyfile,hosted-pog.yaml,install.sh,kitsoki-pog.service,node-runtime.env,pog-portal.service} "$local_stage/"
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
