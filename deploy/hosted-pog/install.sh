#!/usr/bin/env bash
# Remote half of scripts/deploy-hosted-pog.sh. It is uploaded with a POG git
# bundle, the Kitsoki binary, and the versioned service/proxy templates.
set -euo pipefail

die() {
	echo "hosted-pog install: $*" >&2
	exit 1
}

[ "$(id -u)" -eq 0 ] || die "must run as root"
[ "$#" -eq 3 ] || die "usage: install.sh <pog-commit-sha> <kitsoki-commit-sha> <public-base-url>"

pog_sha="$1"
kitsoki_sha="$2"
public_base_url="${3%/}"
stage="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
admin="${KITSOKI_HOSTED_POG_ADMIN:-bsacrobatix}"
env_file="/etc/kitsoki/hosted-pog.env"
release_root="/opt/pog/releases"
release="$release_root/$pog_sha"
current="/opt/pog/current"
kitsoki_release_root="/opt/kitsoki-hosted-pog/releases"
kitsoki_release="$kitsoki_release_root/$kitsoki_sha"
kitsoki_current="/opt/kitsoki-hosted-pog/current"
tmp_release=""
tmp_kitsoki_release=""

cleanup_incomplete_release() {
	status=$?
	if [ "$status" -ne 0 ]; then
		[ -z "$tmp_release" ] || rm -rf -- "$tmp_release"
		[ -z "$tmp_kitsoki_release" ] || rm -rf -- "$tmp_kitsoki_release"
	fi
	exit "$status"
}
trap cleanup_incomplete_release EXIT

[[ "$pog_sha" =~ ^[0-9a-f]{40}$ ]] || die "invalid POG commit SHA"
[[ "$kitsoki_sha" =~ ^[0-9a-f]{40}$ ]] || die "invalid Kitsoki commit SHA"
[[ "$public_base_url" =~ ^https://[A-Za-z0-9.-]+$ ]] || die "public base URL must be an https origin with no path"
[[ "$admin" =~ ^[A-Za-z0-9-]+$ ]] || die "invalid GitHub admin login"
public_host="${public_base_url#https://}"

for file in pog.bundle kitsoki kitsoki-pog.service pog-portal.service hosted-pog.yaml Caddyfile; do
	[ -f "$stage/$file" ] || die "staged file is missing: $file"
done
[ -f "$env_file" ] || die "$env_file is missing; configure the GitHub OAuth client id and secret first"
grep -Eq '^[[:space:]]*KITSOKI_GH_APP_CLIENT_ID=.+' "$env_file" || die "$env_file has no KITSOKI_GH_APP_CLIENT_ID"
grep -Eq '^[[:space:]]*KITSOKI_GH_APP_CLIENT_SECRET=.+' "$env_file" || die "$env_file has no KITSOKI_GH_APP_CLIENT_SECRET"

if ! id -u pog >/dev/null 2>&1; then
	useradd --system --home-dir /var/lib/pog --shell /usr/sbin/nologin pog
fi
install -d -m 0755 /etc/kitsoki "$release_root" "$kitsoki_release_root"
install -d -o pog -g pog -m 0750 /var/lib/pog /var/cache/pog /var/lib/kitsoki-pog /var/cache/kitsoki-pog
install -d -o pog -g pog -m 0750 /var/lib/pog/feedback/hosted /var/lib/pog/graph-mcp /var/lib/pog/streams

if [ ! -d "$kitsoki_release" ]; then
	tmp_kitsoki_release="$kitsoki_release.installing.$$"
	[ ! -e "$tmp_kitsoki_release" ] || die "temporary Kitsoki release path already exists: $tmp_kitsoki_release"
	install -d -m 0755 "$tmp_kitsoki_release"
	install -m 0755 "$stage/kitsoki" "$tmp_kitsoki_release/kitsoki"
	mv "$tmp_kitsoki_release" "$kitsoki_release"
	tmp_kitsoki_release=""
fi
[ -x "$kitsoki_release/kitsoki" ] || die "Kitsoki release is incomplete: $kitsoki_release"
staged_kitsoki_sha="$(sha256sum "$stage/kitsoki" | awk '{print $1}')"
installed_kitsoki_sha="$(sha256sum "$kitsoki_release/kitsoki" | awk '{print $1}')"
[ "$staged_kitsoki_sha" = "$installed_kitsoki_sha" ] || die "Kitsoki release checksum does not match commit $kitsoki_sha"

if [ ! -d "$release/.git" ]; then
	[ ! -e "$release" ] || die "release path exists but is incomplete: $release"
	tmp_release="$release.installing.$$"
	[ ! -e "$tmp_release" ] || die "temporary release path already exists: $tmp_release"
	git clone --no-checkout --quiet "$stage/pog.bundle" "$tmp_release"
	chown -R pog:pog "$tmp_release"
	runuser -u pog -- git -C "$tmp_release" checkout --detach --quiet "$pog_sha"
	runuser -u pog -- env HOME=/var/lib/pog /usr/local/bin/npm --prefix "$tmp_release/portal" ci --no-audit --no-fund
	runuser -u pog -- env HOME=/var/lib/pog /usr/local/bin/npm --prefix "$tmp_release/portal" run build
	install -d -o pog -g pog -m 0750 "$tmp_release/.artifacts"
	mv "$tmp_release" "$release"
	tmp_release=""
fi
[ "$(git -C "$release" rev-parse HEAD)" = "$pog_sha" ] || die "release checkout does not match requested SHA"
[ -z "$(git -C "$release" status --porcelain --untracked-files=no)" ] || die "release checkout has tracked changes: $release"
[ -x "$release/portal/node_modules/.bin/vite" ] || die "release dependencies are incomplete"
[ -f "$release/portal/dist/index.html" ] || die "release build output is missing"

# POG's federation records ../Kitsoki. Each immutable release therefore sees
# the already-deployed GitHub-agent checkout through this stable sibling.
if [ -e "$release_root/Kitsoki" ] && [ ! -L "$release_root/Kitsoki" ]; then
	die "$release_root/Kitsoki exists and is not a symlink"
fi
ln -sfn /opt/kitsoki "$release_root/Kitsoki"

rendered_config="$stage/hosted-pog.rendered.yaml"
rendered_caddy="$stage/Caddyfile.rendered"
sed -e "s|__PUBLIC_BASE_URL__|$public_base_url|g" -e "s|__GITHUB_ADMIN__|$admin|g" "$stage/hosted-pog.yaml" >"$rendered_config"
sed -e "s|__PUBLIC_HOST__|$public_host|g" "$stage/Caddyfile" >"$rendered_caddy"
caddy validate --config "$rendered_caddy" --adapter caddyfile >/dev/null

previous_current=""
if [ -L "$current" ]; then
	previous_current="$(readlink -f "$current")"
elif [ -e "$current" ]; then
	die "$current exists and is not a symlink"
fi
previous_kitsoki_current=""
if [ -L "$kitsoki_current" ]; then
	previous_kitsoki_current="$(readlink -f "$kitsoki_current")"
elif [ -e "$kitsoki_current" ]; then
	die "$kitsoki_current exists and is not a symlink"
fi
previous_caddy="$stage/Caddyfile.previous"
cp /etc/caddy/Caddyfile "$previous_caddy"
caddy_changed=0
current_changed=0
kitsoki_current_changed=0

rollback() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "hosted-pog install failed; restoring the prior release targets and Caddyfile" >&2
		if [ -n "$previous_current" ] && [ -d "$previous_current" ]; then
			ln -s "$previous_current" "$current.rollback.$$"
			mv -Tf "$current.rollback.$$" "$current"
		elif [ "$current_changed" -eq 1 ] && [ -L "$current" ]; then
			unlink "$current"
		fi
		if [ -n "$previous_kitsoki_current" ] && [ -d "$previous_kitsoki_current" ]; then
			ln -s "$previous_kitsoki_current" "$kitsoki_current.rollback.$$"
			mv -Tf "$kitsoki_current.rollback.$$" "$kitsoki_current"
		elif [ "$kitsoki_current_changed" -eq 1 ] && [ -L "$kitsoki_current" ]; then
			unlink "$kitsoki_current"
		fi
		if [ -n "$previous_current" ] && [ -n "$previous_kitsoki_current" ]; then
			systemctl restart kitsoki-pog.service pog-portal.service >/dev/null 2>&1 || true
		else
			systemctl stop pog-portal.service kitsoki-pog.service >/dev/null 2>&1 || true
		fi
		if [ "$caddy_changed" -eq 1 ]; then
			install -m 0644 "$previous_caddy" /etc/caddy/Caddyfile
			systemctl reload caddy >/dev/null 2>&1 || true
		fi
	fi
	exit "$status"
}
trap rollback EXIT

ln -s "$release" "$current.next.$$"
mv -Tf "$current.next.$$" "$current"
current_changed=1
ln -s "$kitsoki_release" "$kitsoki_current.next.$$"
mv -Tf "$kitsoki_current.next.$$" "$kitsoki_current"
kitsoki_current_changed=1
install -m 0644 "$rendered_config" /etc/kitsoki/hosted-pog.yaml
install -m 0644 "$stage/kitsoki-pog.service" /etc/systemd/system/kitsoki-pog.service
install -m 0644 "$stage/pog-portal.service" /etc/systemd/system/pog-portal.service
systemctl daemon-reload
systemctl enable kitsoki-pog.service pog-portal.service >/dev/null
systemctl restart kitsoki-pog.service

for _ in $(seq 1 60); do
	status="$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7777/auth/me 2>/dev/null || true)"
	[ "$status" = "401" ] && break
	sleep 1
done
[ "${status:-}" = "401" ] || die "Kitsoki auth service did not become ready"

systemctl restart pog-portal.service
for _ in $(seq 1 60); do
	if curl -fsS -o /dev/null http://127.0.0.1:5183/api/catalog 2>/dev/null; then
		portal_ready=1
		break
	fi
	sleep 1
done
[ "${portal_ready:-0}" = "1" ] || die "POG portal did not become ready"

install -m 0644 "$rendered_caddy" /etc/caddy/Caddyfile
caddy_changed=1
systemctl reload caddy

expect_public_status() {
	local expected="$1" path="$2" actual
	shift 2
	actual="$(curl -sS -o /dev/null -w '%{http_code}' "$@" "$public_base_url$path")"
	[ "$actual" = "$expected" ] || die "anonymous $path returned HTTP $actual, expected HTTP $expected"
}

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
curl -fsS http://127.0.0.1:8787/healthz >/dev/null

trap - EXIT
echo "hosted-pog install: active POG $pog_sha at $public_base_url (all content requires an invited session)"
