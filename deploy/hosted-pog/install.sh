#!/usr/bin/env bash
# Remote half of scripts/deploy-hosted-pog.sh. It is uploaded with a POG git
# bundle, the Kitsoki binary, and the versioned service/proxy templates.
set -euo pipefail

die() {
	echo "hosted-pog install: $*" >&2
	exit 1
}

[ "$(id -u)" -eq 0 ] || die "must run as root"
[ "$#" -eq 5 ] || die "usage: install.sh <pog-commit-sha> <kitsoki-commit-sha> <public-base-url> <github-client-id> <preserve|sync>"

pog_sha="$1"
kitsoki_sha="$2"
public_base_url="${3%/}"
github_client_id="$4"
state_mode="$5"
stage="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
admin="${KITSOKI_HOSTED_POG_ADMIN:-bsacrobatix}"
release_root="/opt/pog/releases"
release="$release_root/$pog_sha"
current="/opt/pog/current"
kitsoki_release_root="/opt/kitsoki-hosted-pog/releases"
kitsoki_release="$kitsoki_release_root/$kitsoki_sha"
kitsoki_current="/opt/kitsoki-hosted-pog/current"
node_release_root="/opt/kitsoki-hosted-pog/node"
node_current="$node_release_root/current"
state_release_root="/opt/kitsoki-hosted-pog/state-releases"
runtime_release_root="/var/lib/pog/runtime-releases"
runtime_current="/var/lib/pog/runtime"
tmp_release=""
tmp_kitsoki_release=""
tmp_node_release=""
tmp_state_release=""
tmp_runtime_release=""

cleanup_incomplete_release() {
	status=$?
	if [ "$status" -ne 0 ]; then
		[ -z "$tmp_release" ] || rm -rf -- "$tmp_release"
		[ -z "$tmp_kitsoki_release" ] || rm -rf -- "$tmp_kitsoki_release"
		[ -z "$tmp_node_release" ] || rm -rf -- "$tmp_node_release"
		[ -z "$tmp_state_release" ] || rm -rf -- "$tmp_state_release"
		[ -z "$tmp_runtime_release" ] || rm -rf -- "$tmp_runtime_release"
	fi
	exit "$status"
}
trap cleanup_incomplete_release EXIT

[[ "$pog_sha" =~ ^[0-9a-f]{40}$ ]] || die "invalid POG commit SHA"
[[ "$kitsoki_sha" =~ ^[0-9a-f]{40}$ ]] || die "invalid Kitsoki commit SHA"
[[ "$public_base_url" =~ ^https://[A-Za-z0-9.-]+$ ]] || die "public base URL must be an https origin with no path"
[[ "$github_client_id" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid GitHub App client ID"
[[ "$admin" =~ ^[A-Za-z0-9-]+$ ]] || die "invalid GitHub admin login"
[ "$state_mode" = "preserve" ] || [ "$state_mode" = "sync" ] || die "state mode must be preserve or sync"
public_host="${public_base_url#https://}"

for file in pog.bundle kitsoki kitsoki-pog.service node-runtime.env pog-portal.service hosted-pog.yaml Caddyfile gh-client-secret; do
	[ -f "$stage/$file" ] || die "staged file is missing: $file"
done
github_client_secret="$(tr -d '[:space:]' <"$stage/gh-client-secret")"
[[ "$github_client_secret" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid GitHub App client secret"
# Colony service token: reuse the installed value across deployments (same
# posture as the reused OAuth client secret) and mint one on first install.
# The value lives only in root-only env files on this host; the rendered
# hosted-pog.yaml names the env var, never the value.
colony_token="$(sed -n 's/^KITSOKI_COLONY_TOKEN=//p' /etc/kitsoki/hosted-pog.env 2>/dev/null | head -n 1)"
if [ -z "$colony_token" ]; then
	colony_token="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
fi
[[ "$colony_token" =~ ^[0-9a-f]{16,}$ ]] || die "invalid colony service token"
# shellcheck disable=SC1091 -- uploaded beside this installer.
. "$stage/node-runtime.env"
node_version="${KITSOKI_HOSTED_POG_NODE_VERSION:-}"
node_archive="${KITSOKI_HOSTED_POG_NODE_ARCHIVE:-}"
node_url="${KITSOKI_HOSTED_POG_NODE_URL:-}"
node_sha256="${KITSOKI_HOSTED_POG_NODE_SHA256:-}"
[[ "$node_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "invalid hosted POG Node version"
[ "$node_archive" = "node-$node_version-linux-x64.tar.xz" ] || die "Node archive does not match its version"
[ "$node_url" = "https://nodejs.org/download/release/$node_version/$node_archive" ] || die "Node URL is not the pinned official release URL"
[[ "$node_sha256" =~ ^[0-9a-f]{64}$ ]] || die "invalid hosted POG Node SHA-256"
[ -f "$stage/$node_archive" ] || die "staged Node archive is missing: $node_archive"
printf '%s  %s\n' "$node_sha256" "$stage/$node_archive" | sha256sum -c - >/dev/null || die "staged Node archive checksum mismatch"
node_release="$node_release_root/$node_version"

if ! id -u pog >/dev/null 2>&1; then
	useradd --system --home-dir /var/lib/pog --shell /usr/sbin/nologin pog
fi
install -d -m 0755 /etc/kitsoki "$release_root" "$kitsoki_release_root" "$node_release_root" "$state_release_root"
install -d -o pog -g pog -m 0750 /var/lib/pog /var/cache/pog /var/lib/kitsoki-pog /var/cache/kitsoki-pog
install -d -o pog -g pog -m 0750 "$runtime_release_root"

# Portfolio members federated onto the hosted site, beyond POG's own home
# catalog and the embedded Constructor Studio product. The set is authoritative
# from members.manifest (staged by the local deploy half); each line is
# "<repo-dir> <commit-sha> <catalog-id>", where repo-dir is the POG_MEMBER_ROOTS
# key (basename of the track's repo: field) and catalog-id joins
# POG_PORTFOLIO_MEMBERS. Each member is deployed read-only from its own git
# bundle to /opt/pog/members/<repo-dir> so the portal can roll its catalog into
# the graph. This deliberately supersedes the former pog+constructor-studio-only
# restriction: sibling catalogs (including private ones) are federated onto the
# auth-gated site by design. Resolution is deterministic — POG_MEMBER_ROOTS
# below plus POG_PORTFOLIO_ROOT=/opt/pog/current in pog-portal.service — so no
# member depends on host-layout guesswork (POG portal/src/server/
# federation-roots.ts). Replacement is in place per member (brief window); the
# hosted site is a single-tenant test host, not a rollback-critical release.
members_root="/opt/pog/members"
install -d -o pog -g pog -m 0755 "$members_root"
member_roots=""
member_ids=""
if [ -f "$stage/members.manifest" ]; then
	while read -r member_dir member_sha member_id _rest; do
		[ -n "$member_dir" ] || continue
		[[ "$member_dir" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid member repo dir: $member_dir"
		[[ "$member_sha" =~ ^[0-9a-f]{40}$ ]] || die "invalid member commit sha for $member_dir"
		[[ "$member_id" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid member catalog id: $member_id"
		[ -f "$stage/member-$member_dir.bundle" ] || die "staged member bundle is missing: member-$member_dir.bundle"
		member_dest="$members_root/$member_dir"
		tmp_member="$member_dest.installing.$$"
		rm -rf "$tmp_member"
		git clone --no-checkout --quiet "$stage/member-$member_dir.bundle" "$tmp_member"
		chown -R pog:pog "$tmp_member"
		runuser -u pog -- git -C "$tmp_member" checkout --detach --quiet "$member_sha"
		[ -f "$tmp_member/pog/catalog.yaml" ] || die "member $member_dir bundle lacks pog/catalog.yaml"
		rm -rf "$member_dest"
		mv "$tmp_member" "$member_dest"
		member_roots="${member_roots:+$member_roots,}$member_dir=$member_dest"
		member_ids="${member_ids:+$member_ids,}$member_id"
	done <"$stage/members.manifest"
fi
# The two products always present: POG's home catalog and the embedded
# Constructor Studio product (repo: . inside the POG release).
portfolio_members="pog,constructor-studio${member_ids:+,$member_ids}"

if [ ! -x "$node_release/bin/node" ]; then
	[ ! -e "$node_release" ] || die "Node release path exists but is incomplete: $node_release"
	tmp_node_release="$node_release.installing.$$"
	[ ! -e "$tmp_node_release" ] || die "temporary Node release path already exists: $tmp_node_release"
	install -d -m 0755 "$tmp_node_release"
	tar --no-same-owner -xJf "$stage/$node_archive" -C "$tmp_node_release" --strip-components=1
	[ "$("$tmp_node_release/bin/node" --version)" = "$node_version" ] || die "extracted Node version does not match $node_version"
	"$tmp_node_release/bin/node" --no-warnings -e 'require("node:sqlite")' || die "Node $node_version does not provide node:sqlite"
	mv "$tmp_node_release" "$node_release"
	tmp_node_release=""
fi
[ "$("$node_release/bin/node" --version)" = "$node_version" ] || die "installed Node release does not match $node_version"
"$node_release/bin/node" --no-warnings -e 'require("node:sqlite")' || die "installed Node release has no node:sqlite"

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
	runuser -u pog -- env HOME=/var/lib/pog PATH="$node_release/bin:/usr/local/bin:/usr/bin:/bin" "$node_release/bin/npm" --prefix "$tmp_release/portal" ci --no-audit --no-fund
	runuser -u pog -- env \
		HOME=/var/lib/pog \
		PATH="$node_release/bin:/usr/local/bin:/usr/bin:/bin" \
		POG_PORTAL_ROOT="$tmp_release/portal" \
		POG_CATALOG="$tmp_release/pog/catalog.yaml" \
		POG_PROJECT_ROOT="$tmp_release" \
		POG_PORTFOLIO_ROOT="$tmp_release" \
		POG_MEMBER_ROOTS="$member_roots" \
		POG_PORTFOLIO_MEMBERS="$portfolio_members" \
		POG_KITSOKI_URL=http://127.0.0.1:7778 \
		POG_KITSOKI_BROWSER_URL= \
		POG_RUNNER_URL= \
		"$node_release/bin/npm" --prefix "$tmp_release/portal" run build
	runuser -u pog -- "$node_release/bin/node" --check "$tmp_release/portal/server/server.mjs"
	mv "$tmp_release" "$release"
	tmp_release=""
fi
[ "$(runuser -u pog -- git -C "$release" rev-parse HEAD)" = "$pog_sha" ] || die "release checkout does not match requested SHA"
[ -z "$(runuser -u pog -- git -C "$release" status --porcelain --untracked-files=no)" ] || die "release checkout has tracked changes: $release"
[ -f "$release/portal/dist/index.html" ] || die "release build output is missing"
[ -f "$release/portal/server/server.mjs" ] || die "release production server is missing"
[ -f "$release/portal/server/server.mjs.map" ] || die "release production server source map is missing"
runuser -u pog -- "$node_release/bin/node" --check "$release/portal/server/server.mjs"

# Portfolio members were deployed above under /opt/pog/members and are exposed
# through POG_MEMBER_ROOTS/POG_PORTFOLIO_MEMBERS. The former guard that refused
# any sibling repository is intentionally gone: hosting the full portfolio is
# now the contract (operator-selected via the local deploy half's member list).

# Migrate the pre-runtime-layout state without deleting it. The new service
# paths and release-local .artifacts symlink switch together during activation.
if [ -e "$runtime_current" ] && [ ! -L "$runtime_current" ]; then
	die "$runtime_current exists and is not a symlink"
fi
if [ ! -L "$runtime_current" ]; then
	bootstrap_runtime="$runtime_release_root/bootstrap-$(date -u +%Y%m%dT%H%M%SZ)-$$"
	tmp_runtime_release="$bootstrap_runtime.installing"
	install -d -o pog -g pog -m 0750 "$tmp_runtime_release"
	for legacy_root in feedback graph-mcp streams colony agent-runner; do
		[ ! -e "/var/lib/pog/$legacy_root" ] || cp -a "/var/lib/pog/$legacy_root" "$tmp_runtime_release/$legacy_root"
	done
	install -d -o pog -g pog -m 0750 "$tmp_runtime_release/feedback/hosted" "$tmp_runtime_release/graph-mcp" "$tmp_runtime_release/streams"
	chown -R pog:pog "$tmp_runtime_release"
	mv "$tmp_runtime_release" "$bootstrap_runtime"
	tmp_runtime_release=""
	ln -s "$bootstrap_runtime" "$runtime_current.next.$$"
	mv -Tf "$runtime_current.next.$$" "$runtime_current"
fi

prepared_runtime="$(readlink -f "$runtime_current")"
[ -d "$prepared_runtime" ] || die "active POG runtime target is missing"
state_digest=""
if [ "$state_mode" = "sync" ]; then
	[ -f "$stage/pog-state.tar.gz" ] || die "staged local-state archive is missing"
	[ -f "$stage/pog-state.sha256" ] || die "staged local-state checksum is missing"
	state_digest="$(tr -d '[:space:]' <"$stage/pog-state.sha256")"
	[[ "$state_digest" =~ ^[0-9a-f]{64}$ ]] || die "invalid local-state SHA-256"
	printf '%s  %s\n' "$state_digest" "$stage/pog-state.tar.gz" | sha256sum -c - >/dev/null \
		|| die "staged local-state archive checksum mismatch"

	state_members="$stage/pog-state.members"
	tar -tzf "$stage/pog-state.tar.gz" >"$state_members"
	while IFS= read -r member; do
		case "$member" in
			./|./manifest.json|./feedback|./feedback/*|./graph-mcp|./graph-mcp/*|./streams|./streams/*|./colony|./colony/*|./agent-runner|./agent-runner/|./agent-runner/sessions.db) ;;
			*) die "local-state archive contains an unexpected path: $member" ;;
		esac
	done <"$state_members"
	if tar -tvzf "$stage/pog-state.tar.gz" | awk 'substr($1,1,1) != "-" && substr($1,1,1) != "d" { bad=1 } END { exit bad ? 0 : 1 }'; then
		die "local-state archive contains a link or special file"
	fi

	state_release="$state_release_root/$state_digest"
	if [ ! -d "$state_release" ]; then
		tmp_state_release="$state_release.installing.$$"
		[ ! -e "$tmp_state_release" ] || die "temporary local-state release already exists: $tmp_state_release"
		install -d -m 0750 "$tmp_state_release"
		tar --no-same-owner -xzf "$stage/pog-state.tar.gz" -C "$tmp_state_release"
		[ -f "$tmp_state_release/manifest.json" ] || die "local-state manifest is missing"
		chown -R root:root "$tmp_state_release"
		mv "$tmp_state_release" "$state_release"
		tmp_state_release=""
	fi
	state_digest_tool="$stage/state-content-digest.mjs"
	[ -f "$state_digest_tool" ] || die "staged local-state digest tool is missing"
	state_content_digest="$("$node_release/bin/node" "$state_digest_tool" "$state_release" manifest.json "$pog_sha")" \
		|| die "local-state manifest or content digest is invalid"

	active_state_digest=""
	[ ! -f "$prepared_runtime/.hosted-pog-local-state.sha256" ] \
		|| active_state_digest="$(tr -d '[:space:]' <"$prepared_runtime/.hosted-pog-local-state.sha256")"
	active_state_pog_sha=""
	[ ! -f "$prepared_runtime/.hosted-pog-local-state.json" ] \
		|| active_state_pog_sha="$("$node_release/bin/node" -e 'try { process.stdout.write(JSON.parse(require("node:fs").readFileSync(process.argv[1], "utf8")).pog_sha ?? "") } catch {}' "$prepared_runtime/.hosted-pog-local-state.json")"
	active_state_pristine=0
	if [ -f "$prepared_runtime/.hosted-pog-local-state.json" ] && [ -f "$prepared_runtime/.hosted-pog-local-state.sha256" ]; then
		active_state_content_digest="$("$node_release/bin/node" "$state_digest_tool" \
			"$prepared_runtime" .hosted-pog-local-state.json 2>/dev/null || true)"
		if [ -n "$active_state_content_digest" ] && [ "$active_state_content_digest" = "$active_state_digest" ]; then
			active_state_pristine=1
		fi
	fi
	if [ "$active_state_digest" != "$state_content_digest" ] || [ "$active_state_pog_sha" != "$pog_sha" ] || [ "$active_state_pristine" -ne 1 ]; then
		runtime_release="$runtime_release_root/local-$state_content_digest-$(date -u +%Y%m%dT%H%M%SZ)-$$"
		tmp_runtime_release="$runtime_release.installing"
		install -d -o pog -g pog -m 0750 "$tmp_runtime_release"
		if [ "$active_state_pristine" -eq 1 ]; then
			# The active runtime still hashes exactly to the prior imported
			# snapshot, so replacing it cannot discard hosted-only work.
			cp -a "$state_release/." "$tmp_runtime_release/"
			mv "$tmp_runtime_release/manifest.json" "$tmp_runtime_release/.hosted-pog-local-state.json"
		else
			cp -a "$prepared_runtime/." "$tmp_runtime_release/"
			state_conflicts="$stage/pog-state.conflicts"
			: >"$state_conflicts"
			while IFS= read -r -d '' source_file; do
				relative_file="${source_file#"$state_release/"}"
				[ "$relative_file" != "manifest.json" ] || continue
				target_file="$tmp_runtime_release/$relative_file"
				if [ -e "$target_file" ]; then
					cmp -s "$source_file" "$target_file" || echo "$relative_file" >>"$state_conflicts"
					continue
				fi
				install -d -o pog -g pog -m 0750 "$(dirname "$target_file")"
				cp -p "$source_file" "$target_file"
			done < <(find "$state_release" -type f -print0)
			if [ -s "$state_conflicts" ]; then
				echo "hosted-pog install: local-state sync conflicts with hosted files:" >&2
				sed 's/^/  - /' "$state_conflicts" >&2
				die "local-state sync refused to overwrite divergent hosted state"
			fi
			cp "$state_release/manifest.json" "$tmp_runtime_release/.hosted-pog-local-state.json"
		fi
		printf '%s\n' "$state_content_digest" >"$tmp_runtime_release/.hosted-pog-local-state.sha256"
		chown -R pog:pog "$tmp_runtime_release"
		mv "$tmp_runtime_release" "$runtime_release"
		tmp_runtime_release=""
		prepared_runtime="$runtime_release"
	fi
fi

rendered_config="$stage/hosted-pog.rendered.yaml"
rendered_caddy="$stage/Caddyfile.rendered"
rendered_portal_service="$stage/pog-portal.service.rendered"
sed -e "s|__PUBLIC_BASE_URL__|$public_base_url|g" -e "s|__GITHUB_ADMIN__|$admin|g" -e "s|__GITHUB_CLIENT_ID__|$github_client_id|g" "$stage/hosted-pog.yaml" >"$rendered_config"
sed -e "s|__PUBLIC_HOST__|$public_host|g" "$stage/Caddyfile" >"$rendered_caddy"
sed \
	-e "s|__POG_RELEASE_SHA__|$pog_sha|g" \
	-e "s|__POG_MEMBER_ROOTS__|$member_roots|g" \
	-e "s|__POG_PORTFOLIO_MEMBERS__|$portfolio_members|g" \
	"$stage/pog-portal.service" >"$rendered_portal_service"
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
previous_node_current=""
if [ -L "$node_current" ]; then
	previous_node_current="$(readlink -f "$node_current")"
elif [ -e "$node_current" ]; then
	die "$node_current exists and is not a symlink"
fi
previous_runtime="$(readlink -f "$runtime_current")"
[ -d "$previous_runtime" ] || die "previous POG runtime target is missing"
previous_portfolio_link=""
if [ -L "$release_root/Kitsoki" ]; then
	previous_portfolio_link="$(readlink "$release_root/Kitsoki")"
fi
release_artifacts_mode="missing"
release_artifacts_target=""
if [ -L "$release/.artifacts" ]; then
	release_artifacts_mode="symlink"
	release_artifacts_target="$(readlink "$release/.artifacts")"
elif [ -d "$release/.artifacts" ]; then
	[ -z "$(find "$release/.artifacts" -mindepth 1 -print -quit)" ] || die "$release/.artifacts is not empty"
	release_artifacts_mode="directory"
elif [ -e "$release/.artifacts" ]; then
	die "$release/.artifacts exists and is not a directory or symlink"
fi
previous_caddy="$stage/Caddyfile.previous"
cp /etc/caddy/Caddyfile "$previous_caddy"
previous_kitsoki_service="$stage/kitsoki-pog.service.previous"
previous_portal_service="$stage/pog-portal.service.previous"
had_previous_kitsoki_service=0
had_previous_portal_service=0
if [ -f /etc/systemd/system/kitsoki-pog.service ]; then
	cp /etc/systemd/system/kitsoki-pog.service "$previous_kitsoki_service"
	had_previous_kitsoki_service=1
fi
if [ -f /etc/systemd/system/pog-portal.service ]; then
	cp /etc/systemd/system/pog-portal.service "$previous_portal_service"
	had_previous_portal_service=1
fi
caddy_changed=0
services_changed=0
current_changed=0
kitsoki_current_changed=0
node_current_changed=0
runtime_current_changed=0
portfolio_link_changed=0
release_artifacts_changed=0

rollback() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "hosted-pog install failed; restoring the prior release targets, service units, and Caddyfile" >&2
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
		if [ -n "$previous_node_current" ] && [ -d "$previous_node_current" ]; then
			ln -s "$previous_node_current" "$node_current.rollback.$$"
			mv -Tf "$node_current.rollback.$$" "$node_current"
		elif [ "$node_current_changed" -eq 1 ] && [ -L "$node_current" ]; then
			unlink "$node_current"
		fi
		if [ "$runtime_current_changed" -eq 1 ]; then
			ln -s "$previous_runtime" "$runtime_current.rollback.$$"
			mv -Tf "$runtime_current.rollback.$$" "$runtime_current"
		fi
		if [ "$portfolio_link_changed" -eq 1 ] && [ -n "$previous_portfolio_link" ]; then
			ln -s "$previous_portfolio_link" "$release_root/Kitsoki.rollback.$$"
			mv -Tf "$release_root/Kitsoki.rollback.$$" "$release_root/Kitsoki"
		fi
		if [ "$release_artifacts_changed" -eq 1 ] && [ "$previous_current" = "$release" ]; then
			[ ! -L "$release/.artifacts" ] || unlink "$release/.artifacts"
			case "$release_artifacts_mode" in
				directory) install -d -o pog -g pog -m 0750 "$release/.artifacts" ;;
				symlink) ln -s "$release_artifacts_target" "$release/.artifacts" ;;
			esac
		fi
		if [ "$services_changed" -eq 1 ]; then
			if [ "$had_previous_kitsoki_service" -eq 1 ]; then
				install -m 0644 "$previous_kitsoki_service" /etc/systemd/system/kitsoki-pog.service
			else
				rm -f /etc/systemd/system/kitsoki-pog.service
			fi
			if [ "$had_previous_portal_service" -eq 1 ]; then
				install -m 0644 "$previous_portal_service" /etc/systemd/system/pog-portal.service
			else
				rm -f /etc/systemd/system/pog-portal.service
			fi
			systemctl daemon-reload >/dev/null 2>&1 || true
		fi
		if [ "$caddy_changed" -eq 1 ]; then
			install -m 0644 "$previous_caddy" /etc/caddy/Caddyfile
			systemctl reload caddy >/dev/null 2>&1 || true
		fi
		if [ -n "$previous_current" ] && [ -n "$previous_kitsoki_current" ] && [ -n "$previous_node_current" ]; then
			systemctl restart kitsoki-pog.service pog-portal.service >/dev/null 2>&1 || true
		else
			systemctl stop pog-portal.service kitsoki-pog.service >/dev/null 2>&1 || true
		fi
	fi
	exit "$status"
}
trap rollback EXIT

systemctl stop pog-portal.service >/dev/null 2>&1 || true

# Remove the historical sibling link before starting the candidate. This is
# what makes the two-product contract independent of future files under
# /opt/kitsoki.
if [ -L "$release_root/Kitsoki" ]; then
	unlink "$release_root/Kitsoki"
	portfolio_link_changed=1
fi

if [ -L "$release/.artifacts" ]; then
	[ "$(readlink "$release/.artifacts")" = "$runtime_current" ] || {
		unlink "$release/.artifacts"
		ln -s "$runtime_current" "$release/.artifacts"
		release_artifacts_changed=1
	}
elif [ -d "$release/.artifacts" ]; then
	rmdir "$release/.artifacts"
	ln -s "$runtime_current" "$release/.artifacts"
	release_artifacts_changed=1
else
	ln -s "$runtime_current" "$release/.artifacts"
	release_artifacts_changed=1
fi

if [ "$prepared_runtime" != "$previous_runtime" ]; then
	ln -s "$prepared_runtime" "$runtime_current.next.$$"
	mv -Tf "$runtime_current.next.$$" "$runtime_current"
	runtime_current_changed=1
fi

ln -s "$release" "$current.next.$$"
mv -Tf "$current.next.$$" "$current"
current_changed=1
ln -s "$kitsoki_release" "$kitsoki_current.next.$$"
mv -Tf "$kitsoki_current.next.$$" "$kitsoki_current"
kitsoki_current_changed=1
ln -s "$node_release" "$node_current.next.$$"
mv -Tf "$node_current.next.$$" "$node_current"
node_current_changed=1
install -m 0644 "$rendered_config" /etc/kitsoki/hosted-pog.yaml
# The rendered config references ${KITSOKI_HOSTED_POG_GH_CLIENT_SECRET}; the
# value lives in this root-only env file read by systemd, never by the pog user.
{
	printf 'KITSOKI_HOSTED_POG_GH_CLIENT_SECRET=%s\n' "$github_client_secret"
	printf 'KITSOKI_COLONY_TOKEN=%s\n' "$colony_token"
} >"$stage/hosted-pog.env"
install -m 0600 "$stage/hosted-pog.env" /etc/kitsoki/hosted-pog.env
install -m 0644 "$stage/kitsoki-pog.service" /etc/systemd/system/kitsoki-pog.service
install -m 0644 "$rendered_portal_service" /etc/systemd/system/pog-portal.service
services_changed=1
systemctl daemon-reload
systemctl enable kitsoki-pog.service pog-portal.service >/dev/null
systemctl restart kitsoki-pog.service

for _ in $(seq 1 60); do
	status="$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7778/auth/me 2>/dev/null || true)"
	[ "$status" = "401" ] && break
	sleep 1
done
[ "${status:-}" = "401" ] || die "Kitsoki auth service did not become ready"

# The colony bearer contract must actually authenticate before anything
# depends on it: /auth/check is the same seam Caddy's forward_auth and
# Manager.Wrap use for service tokens.
token_status="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $colony_token" http://127.0.0.1:7778/auth/check 2>/dev/null || true)"
[ "$token_status" = "200" ] || die "colony service token did not authenticate against /auth/check (got ${token_status:-none})"
bare_status="$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7778/auth/check 2>/dev/null || true)"
[ "$bare_status" = "401" ] || die "unauthenticated /auth/check unexpectedly returned ${bare_status:-none}"

# Mirror the token to the POG colony runner (if this host runs one) as
# POG_RUNNER_TOKEN — the env var POG's runner-auth.mjs sends as a bearer.
# The drop-in references a root-only env file so the secret never sits in a
# world-readable unit file.
if systemctl cat pog-colony-runner.service >/dev/null 2>&1; then
	printf 'POG_RUNNER_TOKEN=%s\n' "$colony_token" >"$stage/pog-colony-runner.env"
	install -m 0600 "$stage/pog-colony-runner.env" /etc/kitsoki/pog-colony-runner.env
	install -d -m 0755 /etc/systemd/system/pog-colony-runner.service.d
	printf '[Service]\nEnvironmentFile=-/etc/kitsoki/pog-colony-runner.env\n' >/etc/systemd/system/pog-colony-runner.service.d/runner-token.conf
	systemctl daemon-reload
	systemctl restart pog-colony-runner.service
fi

systemctl restart pog-portal.service
for _ in $(seq 1 60); do
	if curl -fsS -o /dev/null http://127.0.0.1:7777/api/portal-health 2>/dev/null; then
		portal_ready=1
		break
	fi
	sleep 1
done
[ "${portal_ready:-0}" = "1" ] || die "POG production portal did not become ready on port 7777"

health_json="$(curl -fsS http://127.0.0.1:7777/api/portal-health)"
printf '%s' "$health_json" | "$node_release/bin/node" -e '
const fs = require("node:fs");
const health = JSON.parse(fs.readFileSync(0, "utf8"));
if (health.ok !== true || health.service !== "pog-portal" || health.mode !== "production" || health.revision !== process.argv[1]) {
  console.error(JSON.stringify(health));
  process.exit(1);
}
' "$pog_sha" || die "POG production health does not identify the activated revision"
catalog_json="$(curl -fsS http://127.0.0.1:7777/api/catalog)"
printf '%s' "$catalog_json" | "$node_release/bin/node" -e '
const fs = require("node:fs");
const graph = JSON.parse(fs.readFileSync(0, "utf8"));
const expected = process.argv[1].split(",").sort().join(",");
const products = [...new Set((graph.comparison_catalogs ?? []).map((entry) => entry.id))].sort().join(",");
const repos = [...new Set((graph.nodes ?? []).map((node) => node.attrs?.repo).filter(Boolean))].sort().join(",");
const unavailable = (graph.federation?.unavailable ?? []).map((entry) => entry.repo);
if (products !== expected || repos !== expected || unavailable.length) {
  console.error(JSON.stringify({ expected, products, repos, unavailable }));
  process.exit(1);
}
' "$portfolio_members" || die "hosted catalog does not contain every configured portfolio member"
curl -fsS http://127.0.0.1:7777/api/feedback-reports | "$node_release/bin/node" -e '
const fs = require("node:fs");
const body = JSON.parse(fs.readFileSync(0, "utf8"));
if (!Array.isArray(body.reports)) process.exit(1);
' || die "POG feedback reports are not served by the production portal"
portal_pid="$(systemctl show --property MainPID --value pog-portal.service)"
[[ "$portal_pid" =~ ^[1-9][0-9]*$ ]] || die "POG production portal has no main process"
portal_command="$(tr '\0' ' ' <"/proc/$portal_pid/cmdline")"
case "$portal_command" in
	*server/server.mjs*"--addr 127.0.0.1:7777"*) ;;
	*) die "POG service is not running the production server on port 7777: $portal_command" ;;
esac
case "$portal_command" in
	*vite*|*"npm run dev"*) die "POG service unexpectedly runs Vite: $portal_command" ;;
esac
[ -z "$(ss -ltnH 'sport = :5183')" ] || die "legacy Vite port 5183 is still listening"
[ -L "$release/.artifacts" ] && [ "$(readlink -f "$release/.artifacts")" = "$(readlink -f "$runtime_current")" ] \
	|| die "POG release is not bound to the versioned runtime"
if [ "$state_mode" = "sync" ]; then
	[ "$(tr -d '[:space:]' <"$runtime_current/.hosted-pog-local-state.sha256")" = "$state_content_digest" ] \
		|| die "active POG runtime does not match the uploaded local-state snapshot"
fi

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
login_page="$(curl -fsS "$public_base_url/auth/login")"
grep -q '/auth/github/start' <<<"$login_page"
curl -fsS http://127.0.0.1:8787/healthz >/dev/null

trap - EXIT
echo "hosted-pog install: active production POG $pog_sha on 127.0.0.1:7777 at $public_base_url (products=$portfolio_members; state=$state_mode; no Vite runtime; all content requires an invited session)"
