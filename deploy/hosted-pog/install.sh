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
capsule_state_root=/var/lib/pog/capsules
# Remote worker bundles are admitted into this external queue authority.  It
# stays outside /opt/pog/current so admission never writes the protected
# checkout; the hosted queue worker consumes its queue/ child explicitly.
queue_admission_root=/var/lib/kitsoki-queue-admission/pog
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

for file in pog.bundle kitsoki kitsoki-pog.service node-runtime.env pog-capsule-state.service pog-colony-runner-portfolio.conf pog-portal.service pog-worker-finalizer.service pog-worker-finalizer.timer kitsoki-queue-admission.service kitsoki-queue-worker-admission.conf hosted-pog.yaml Caddyfile gh-client-secret link-capsule-state.sh import-legacy-worker-ships.sh prune-releases.sh; do
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
install -d -o pog -g pog -m 0750 "$capsule_state_root"
install -d -o pog -g pog -m 0700 "$queue_admission_root"
install -d -m 0755 /usr/local/libexec
install -m 0755 "$stage/link-capsule-state.sh" /usr/local/libexec/kitsoki-hosted-pog-link-capsule-state
install -m 0644 "$stage/pog-capsule-state.service" /etc/systemd/system/pog-capsule-state.service

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
	install -d -m 0755 "$tmp_kitsoki_release" "$tmp_kitsoki_release/scripts"
	install -m 0755 "$stage/kitsoki" "$tmp_kitsoki_release/kitsoki"
	install -m 0755 "$stage/kitsoki-dev-workspace.sh" "$tmp_kitsoki_release/scripts/dev-workspace.sh"
	mv "$tmp_kitsoki_release" "$kitsoki_release"
	tmp_kitsoki_release=""
fi
[ -x "$kitsoki_release/kitsoki" ] || die "Kitsoki release is incomplete: $kitsoki_release"
[ -x "$kitsoki_release/scripts/dev-workspace.sh" ] \
	|| die "Kitsoki release lacks its immutable lifecycle helper: $kitsoki_release/scripts/dev-workspace.sh"
staged_kitsoki_sha="$(sha256sum "$stage/kitsoki" | awk '{print $1}')"
installed_kitsoki_sha="$(sha256sum "$kitsoki_release/kitsoki" | awk '{print $1}')"
[ "$staged_kitsoki_sha" = "$installed_kitsoki_sha" ] || die "Kitsoki release checksum does not match commit $kitsoki_sha"
staged_lifecycle_sha="$(sha256sum "$stage/kitsoki-dev-workspace.sh" | awk '{print $1}')"
installed_lifecycle_sha="$(sha256sum "$kitsoki_release/scripts/dev-workspace.sh" | awk '{print $1}')"
[ "$staged_lifecycle_sha" = "$installed_lifecycle_sha" ] \
	|| die "Kitsoki lifecycle helper checksum does not match commit $kitsoki_sha"

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
runuser -u pog -- git -C "$release" update-ref refs/heads/main "$pog_sha"
[ "$(runuser -u pog -- git -C "$release" rev-parse main)" = "$pog_sha" ] || die "release main ref does not match requested SHA"
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
rendered_colony_portfolio="$stage/pog-colony-runner-portfolio.conf.rendered"
sed -e "s|__PUBLIC_BASE_URL__|$public_base_url|g" -e "s|__GITHUB_ADMIN__|$admin|g" -e "s|__GITHUB_CLIENT_ID__|$github_client_id|g" "$stage/hosted-pog.yaml" >"$rendered_config"
sed -e "s|__PUBLIC_HOST__|$public_host|g" "$stage/Caddyfile" >"$rendered_caddy"
sed \
	-e "s|__POG_RELEASE_SHA__|$pog_sha|g" \
	-e "s|__POG_MEMBER_ROOTS__|$member_roots|g" \
	-e "s|__POG_PORTFOLIO_MEMBERS__|$portfolio_members|g" \
	"$stage/pog-portal.service" >"$rendered_portal_service"
sed \
	-e "s|__POG_MEMBER_ROOTS__|$member_roots|g" \
	-e "s|__POG_PORTFOLIO_MEMBERS__|$portfolio_members|g" \
	"$stage/pog-colony-runner-portfolio.conf" >"$rendered_colony_portfolio"
caddy validate --config "$rendered_caddy" --adapter caddyfile >/dev/null

# Remote Capsule promotion needs an admission listener on this controller, but
# neither its bearer nor object-store credentials may enter a unit file, git
# bundle, or deploy command line. Reuse an already-installed admission env
# verbatim. On first hosted deployment, derive the controller's configured
# POG source-bucket credentials from the root-owned queue-worker env and mint
# only the separate bearer.  The dispatcher resolves these values into
# KITSOKI_WORKER_OUTPUTS_* only inside each disposable worker; they are not
# controller environment names.
# The staged result is copied at mode 0600 after rollback snapshots exist.
queue_worker_env=/etc/kitsoki/queue-worker.env
queue_admission_env=/etc/kitsoki/queue-admission.env
queue_admission_stage_env="$stage/queue-admission.env"
# This is the checked-in POG vm-pool source_bucket contract.  Keep the
# admission service on the controller-facing names from that contract rather
# than assuming the per-worker aliases exist in queue-worker.env.
queue_admission_bucket_url=https://kitsoki-test.sgp1.digitaloceanspaces.com
queue_admission_key_env=DO_SPACES_KEY_ID
queue_admission_secret_env=DO_KITSOKI_TEST_API_KEY
env_value() {
	local file="$1" name="$2" matches value
	[ -f "$file" ] && [ ! -L "$file" ] || die "credential source is not a regular file: $file"
	matches="$(grep -Ec "^${name}=[^[:space:]].*$" "$file" || true)"
	[ "$matches" = 1 ] || die "credential source must contain exactly one nonempty $name"
	value="$(sed -n "s/^${name}=//p" "$file")"
	case "$value" in *$'\n'*|*$'\r'*) die "credential source has unsafe $name" ;; esac
	printf '%s' "$value"
}
prepare_queue_admission_env() {
	if [ -e "$queue_admission_env" ]; then
		[ ! -L "$queue_admission_env" ] || die "queue-admission environment must not be a symlink"
		[ "$(stat -c '%U:%G %a' "$queue_admission_env")" = 'root:root 600' ] \
			|| die "queue-admission environment must be root-owned mode 0600"
		for required in KITSOKI_QUEUE_ADMISSION_TOKEN KITSOKI_QUEUE_ADMISSION_BUCKET_URL "$queue_admission_key_env" "$queue_admission_secret_env"; do
			env_value "$queue_admission_env" "$required" >/dev/null
		done
		cp "$queue_admission_env" "$queue_admission_stage_env"
		chmod 0600 "$queue_admission_stage_env"
		return
	fi
	[ -e "$queue_worker_env" ] || die "first admission install requires $queue_worker_env with worker-output credentials"
	[ ! -L "$queue_worker_env" ] || die "queue-worker environment must not be a symlink"
	[ "$(stat -c '%U:%G %a' "$queue_worker_env")" = 'root:root 600' ] \
		|| die "queue-worker environment must be root-owned mode 0600 before deriving admission credentials"
	access_key="$(env_value "$queue_worker_env" "$queue_admission_key_env")"
	secret_key="$(env_value "$queue_worker_env" "$queue_admission_secret_env")"
	token="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
	[[ "$token" =~ ^[0-9a-f]{64}$ ]] || die "could not mint queue-admission bearer"
	{
		printf 'KITSOKI_QUEUE_ADMISSION_TOKEN=%s\n' "$token"
		printf 'KITSOKI_QUEUE_ADMISSION_BUCKET_URL=%s\n' "$queue_admission_bucket_url"
		printf '%s=%s\n' "$queue_admission_key_env" "$access_key"
		printf '%s=%s\n' "$queue_admission_secret_env" "$secret_key"
	} >"$queue_admission_stage_env"
	chmod 0600 "$queue_admission_stage_env"
}
prepare_queue_admission_env

previous_current=""
if [ -L "$current" ]; then
	previous_current="$(readlink -f "$current")"
elif [ -e "$current" ]; then
	die "$current exists and is not a symlink"
fi
previous_fixed_autonomously=""
if [ -n "$previous_current" ]; then
	previous_scoreboard="$(curl -fsS http://127.0.0.1:7777/api/feedback-autonomy/scoreboard 2>/dev/null || true)"
	if [ -n "$previous_scoreboard" ]; then
		previous_fixed_autonomously="$(printf '%s' "$previous_scoreboard" | "$node_release/bin/node" -e '
const fs = require("node:fs");
try {
  const count = JSON.parse(fs.readFileSync(0, "utf8"))?.counts?.fixed_autonomously;
  if (Number.isSafeInteger(count) && count >= 0) process.stdout.write(String(count));
} catch {}
')"
	fi
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
previous_finalizer_service="$stage/pog-worker-finalizer.service.previous"
previous_finalizer_timer="$stage/pog-worker-finalizer.timer.previous"
previous_queue_worker_engine="$stage/kitsoki-queue-worker.hosted-engine.conf.previous"
previous_queue_worker_admission="$stage/kitsoki-queue-worker.admission.conf.previous"
previous_queue_admission_service="$stage/kitsoki-queue-admission.service.previous"
previous_queue_admission_env="$stage/queue-admission.env.previous"
previous_colony_portfolio="$stage/pog-colony-runner.portfolio-authority.conf.previous"
previous_colony_env="$stage/pog-colony-runner.env.previous"
had_previous_kitsoki_service=0
had_previous_portal_service=0
had_previous_finalizer_service=0
had_previous_finalizer_timer=0
had_previous_queue_worker_engine=0
had_previous_queue_worker_admission=0
had_previous_queue_admission_service=0
had_previous_queue_admission_env=0
had_previous_colony_portfolio=0
had_previous_colony_env=0
if [ -f /etc/systemd/system/kitsoki-pog.service ]; then
	cp /etc/systemd/system/kitsoki-pog.service "$previous_kitsoki_service"
	had_previous_kitsoki_service=1
fi
if [ -f /etc/systemd/system/pog-portal.service ]; then
	cp /etc/systemd/system/pog-portal.service "$previous_portal_service"
	had_previous_portal_service=1
fi
if [ -f /etc/systemd/system/pog-worker-finalizer.service ]; then
	cp /etc/systemd/system/pog-worker-finalizer.service "$previous_finalizer_service"
	had_previous_finalizer_service=1
fi
if [ -f /etc/systemd/system/pog-worker-finalizer.timer ]; then
	cp /etc/systemd/system/pog-worker-finalizer.timer "$previous_finalizer_timer"
	had_previous_finalizer_timer=1
fi
if [ -f /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf ]; then
	cp /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf "$previous_queue_worker_engine"
	had_previous_queue_worker_engine=1
fi
if [ -f /etc/systemd/system/kitsoki-queue-worker.service.d/10-queue-admission.conf ]; then
	cp /etc/systemd/system/kitsoki-queue-worker.service.d/10-queue-admission.conf "$previous_queue_worker_admission"
	had_previous_queue_worker_admission=1
fi
if [ -f /etc/systemd/system/kitsoki-queue-admission.service ]; then
	cp /etc/systemd/system/kitsoki-queue-admission.service "$previous_queue_admission_service"
	had_previous_queue_admission_service=1
fi
if [ -f "$queue_admission_env" ]; then
	cp "$queue_admission_env" "$previous_queue_admission_env"
	had_previous_queue_admission_env=1
fi
if [ -f /etc/systemd/system/pog-colony-runner.service.d/portfolio-authority.conf ]; then
	cp /etc/systemd/system/pog-colony-runner.service.d/portfolio-authority.conf "$previous_colony_portfolio"
	had_previous_colony_portfolio=1
fi
if [ -f /etc/kitsoki/pog-colony-runner.env ]; then
	cp /etc/kitsoki/pog-colony-runner.env "$previous_colony_env"
	had_previous_colony_env=1
fi
caddy_changed=0
services_changed=0
current_changed=0
kitsoki_current_changed=0
node_current_changed=0
runtime_current_changed=0
portfolio_link_changed=0
release_artifacts_changed=0
colony_was_active=0
queue_worker_was_active=0
queue_admission_was_active=0
queue_admission_was_enabled=0
finalizer_timer_was_active=0
finalizer_timer_was_enabled=0

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
			systemctl stop kitsoki-queue-admission.service >/dev/null 2>&1 || true
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
			if [ "$had_previous_finalizer_service" -eq 1 ]; then
				install -m 0644 "$previous_finalizer_service" /etc/systemd/system/pog-worker-finalizer.service
			else
				rm -f /etc/systemd/system/pog-worker-finalizer.service
			fi
			if [ "$had_previous_finalizer_timer" -eq 1 ]; then
				install -m 0644 "$previous_finalizer_timer" /etc/systemd/system/pog-worker-finalizer.timer
			else
				rm -f /etc/systemd/system/pog-worker-finalizer.timer
			fi
			if [ "$had_previous_queue_worker_engine" -eq 1 ]; then
				install -d -m 0755 /etc/systemd/system/kitsoki-queue-worker.service.d
				install -m 0644 "$previous_queue_worker_engine" /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
			else
				rm -f /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
			fi
			if [ "$had_previous_queue_worker_admission" -eq 1 ]; then
				install -d -m 0755 /etc/systemd/system/kitsoki-queue-worker.service.d
				install -m 0644 "$previous_queue_worker_admission" /etc/systemd/system/kitsoki-queue-worker.service.d/10-queue-admission.conf
			else
				rm -f /etc/systemd/system/kitsoki-queue-worker.service.d/10-queue-admission.conf
			fi
			if [ "$had_previous_queue_admission_service" -eq 1 ]; then
				install -m 0644 "$previous_queue_admission_service" /etc/systemd/system/kitsoki-queue-admission.service
			else
				rm -f /etc/systemd/system/kitsoki-queue-admission.service
			fi
			if [ "$had_previous_queue_admission_env" -eq 1 ]; then
				install -m 0600 "$previous_queue_admission_env" "$queue_admission_env"
			else
				rm -f "$queue_admission_env"
			fi
			if [ "$had_previous_colony_portfolio" -eq 1 ]; then
				install -d -m 0755 /etc/systemd/system/pog-colony-runner.service.d
				install -m 0644 "$previous_colony_portfolio" /etc/systemd/system/pog-colony-runner.service.d/portfolio-authority.conf
			else
				rm -f /etc/systemd/system/pog-colony-runner.service.d/portfolio-authority.conf
			fi
			if [ "$had_previous_colony_env" -eq 1 ]; then
				install -m 0600 "$previous_colony_env" /etc/kitsoki/pog-colony-runner.env
			else
				rm -f /etc/kitsoki/pog-colony-runner.env
			fi
			systemctl daemon-reload >/dev/null 2>&1 || true
		fi
		if [ "$caddy_changed" -eq 1 ]; then
			install -m 0644 "$previous_caddy" /etc/caddy/Caddyfile
			systemctl reload caddy >/dev/null 2>&1 || true
		fi
		if [ -n "$previous_current" ] && [ -n "$previous_kitsoki_current" ] && [ -n "$previous_node_current" ]; then
			systemctl restart pog-capsule-state.service >/dev/null 2>&1 || true
			systemctl restart kitsoki-pog.service pog-portal.service >/dev/null 2>&1 || true
		else
			systemctl stop pog-portal.service kitsoki-pog.service >/dev/null 2>&1 || true
		fi
		if [ "$queue_admission_was_enabled" -eq 1 ] && [ "$had_previous_queue_admission_service" -eq 1 ]; then
			systemctl enable kitsoki-queue-admission.service >/dev/null 2>&1 || true
		else
			systemctl disable kitsoki-queue-admission.service >/dev/null 2>&1 || true
		fi
		if [ "$queue_admission_was_active" -eq 1 ] && [ "$had_previous_queue_admission_service" -eq 1 ]; then
			systemctl restart kitsoki-queue-admission.service >/dev/null 2>&1 || true
		fi
		[ "$queue_worker_was_active" -eq 0 ] || systemctl restart kitsoki-queue-worker.service >/dev/null 2>&1 || true
		[ "$colony_was_active" -eq 0 ] || systemctl restart pog-colony-runner.service >/dev/null 2>&1 || true
		if [ "$finalizer_timer_was_enabled" -eq 1 ]; then
			systemctl enable pog-worker-finalizer.timer >/dev/null 2>&1 || true
		else
			systemctl disable pog-worker-finalizer.timer >/dev/null 2>&1 || true
		fi
		if [ "$finalizer_timer_was_active" -eq 1 ]; then
			systemctl start pog-worker-finalizer.timer >/dev/null 2>&1 || true
		else
			systemctl stop pog-worker-finalizer.timer >/dev/null 2>&1 || true
		fi
	fi
	exit "$status"
}
trap rollback EXIT

systemctl is-active --quiet pog-colony-runner.service && colony_was_active=1 || true
systemctl is-active --quiet kitsoki-queue-worker.service && queue_worker_was_active=1 || true
systemctl is-active --quiet kitsoki-queue-admission.service && queue_admission_was_active=1 || true
systemctl is-enabled --quiet kitsoki-queue-admission.service && queue_admission_was_enabled=1 || true
systemctl is-active --quiet pog-worker-finalizer.timer && finalizer_timer_was_active=1 || true
systemctl is-enabled --quiet pog-worker-finalizer.timer && finalizer_timer_was_enabled=1 || true
systemctl stop pog-worker-finalizer.timer pog-worker-finalizer.service >/dev/null 2>&1 || true
systemctl stop pog-portal.service pog-colony-runner.service kitsoki-queue-worker.service kitsoki-queue-admission.service >/dev/null 2>&1 || true

# Releases predating the versioned runtime wrote immutable worker ship records
# into their own .artifacts tree. Import those exact scoreboard inputs after
# stopping the portal (which also stops its dispatch children) and before the
# current-release switch. The importer is idempotent and fails closed if an
# existing durable record differs.
if [ -n "$previous_current" ] && [ -d "$previous_current/.artifacts" ] && [ ! -L "$previous_current/.artifacts" ]; then
	bash "$stage/import-legacy-worker-ships.sh" "$previous_current/.artifacts" "$prepared_runtime"
	chown -R pog:pog "$prepared_runtime/feedback/dispatch"
fi

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
# `queue-worker.env` is intentionally shared with the portal and finalizer
# for worker credentials.  It must not also choose their engine: a stale
# selector there used to override the versioned service contract during a
# deploy.  The queue worker selects the hosted engine through its dedicated
# systemd drop-in, leaving the historical seal/control binary untouched.
if [ -f /etc/kitsoki/queue-worker.env ]; then
	sed -i '/^[[:space:]]*POG_KITSOKI_BIN=/d' /etc/kitsoki/queue-worker.env
fi
install -m 0644 "$stage/kitsoki-pog.service" /etc/systemd/system/kitsoki-pog.service
# The hosted daemon owns worker dispatch, so it must use the same versioned
# engine as the portal, finalizer, and queue worker. Historical operator
# drop-ins selected cache-local binaries under /opt/pog/current/.artifacts;
# those paths disappear during legitimate cache hygiene and silently override
# the base unit after a release flip. Remove only that obsolete selector from
# existing drop-ins, then install a lexically-last managed selector. Other
# settings in those drop-ins remain intact.
install -d -m 0755 /etc/systemd/system/kitsoki-pog.service.d
for dropin in /etc/systemd/system/kitsoki-pog.service.d/*.conf; do
	[ -f "$dropin" ] || continue
	sed -i '/^[[:space:]]*Environment=POG_KITSOKI_BIN=/d' "$dropin"
done
printf '[Service]\nEnvironment=POG_KITSOKI_BIN=/opt/kitsoki-hosted-pog/current/kitsoki\n' \
	>/etc/systemd/system/kitsoki-pog.service.d/zz-hosted-engine.conf
install -m 0644 "$rendered_portal_service" /etc/systemd/system/pog-portal.service
install -m 0644 "$stage/pog-worker-finalizer.service" /etc/systemd/system/pog-worker-finalizer.service
install -m 0644 "$stage/pog-worker-finalizer.timer" /etc/systemd/system/pog-worker-finalizer.timer
if systemctl cat pog-colony-runner.service >/dev/null 2>&1; then
	install -d -m 0755 /etc/systemd/system/pog-colony-runner.service.d
	printf '[Unit]\nRequires=pog-capsule-state.service\nAfter=pog-capsule-state.service\n' \
		>/etc/systemd/system/pog-colony-runner.service.d/capsule-state.conf
	install -m 0644 "$rendered_colony_portfolio" /etc/systemd/system/pog-colony-runner.service.d/portfolio-authority.conf
fi
if systemctl cat kitsoki-queue-worker.service >/dev/null 2>&1; then
	install -d -m 0755 /etc/systemd/system/kitsoki-queue-worker.service.d
	printf '[Unit]\nRequires=pog-capsule-state.service\nAfter=pog-capsule-state.service\n' \
		>/etc/systemd/system/kitsoki-queue-worker.service.d/capsule-state.conf
	install -m 0644 "$stage/kitsoki-queue-worker-admission.conf" /etc/systemd/system/kitsoki-queue-worker.service.d/10-queue-admission.conf
	install -m 0644 "$stage/kitsoki-queue-worker-hosted-engine.conf" /etc/systemd/system/kitsoki-queue-worker.service.d/zz-hosted-engine.conf
fi
install -m 0644 "$stage/kitsoki-queue-admission.service" /etc/systemd/system/kitsoki-queue-admission.service
install -m 0600 "$queue_admission_stage_env" "$queue_admission_env"
services_changed=1
systemctl daemon-reload
systemctl enable pog-capsule-state.service kitsoki-pog.service pog-portal.service kitsoki-queue-admission.service >/dev/null
systemctl restart pog-capsule-state.service
systemctl restart kitsoki-pog.service

# A missing/invalid root-only admission env must fail activation before the
# queue worker can consume this authority. POST without a bearer is a harmless
# liveness/authentication probe; the server intentionally returns 401 before
# examining request capacity or body.
systemctl restart kitsoki-queue-admission.service
systemctl is-active --quiet kitsoki-queue-admission.service \
	|| die "hosted queue-admission service did not become active"
test "$(stat -c '%U:%G %a' "$queue_admission_root")" = 'pog:pog 700' \
	|| die "queue-admission authority ownership/mode drifted"
test "$(stat -c '%U:%G %a' "$queue_admission_env")" = 'root:root 600' \
	|| die "queue-admission environment ownership/mode drifted"
test -z "$(ss -ltnH 'sport = :7444' | awk '$4 != "127.0.0.1:7444" { print }')" \
	|| die "queue-admission listener escaped loopback"
# systemd considers Type=simple started once exec succeeds; the Go listener can
# still be between process start and bind.  Wait only for the intentional
# unauthenticated response.  A capacity response is never an acceptable
# substitute: authentication is evaluated before admission capacity, so 429
# here would violate the 401-versus-429 contract rather than indicate startup.
admission_status=""
for _ in $(seq 1 30); do
	admission_status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:7444/v1/queue/admissions 2>/dev/null || true)"
	[ "$admission_status" = 401 ] && break
	[ "$admission_status" != 429 ] || die "queue-admission unauthenticated probe returned 429; authentication must precede capacity"
	sleep 1
done
[ "$admission_status" = 401 ] || die "queue-admission authentication probe returned ${admission_status:-none} after readiness wait, expected 401"

hosted_engine=/opt/kitsoki-hosted-pog/current/kitsoki
test -x "$hosted_engine" || die "hosted Kitsoki engine is not executable: $hosted_engine"
systemctl show --property Environment --value kitsoki-pog.service \
	| grep -Fq "POG_KITSOKI_BIN=$hosted_engine" \
	|| die "kitsoki-pog did not activate with the versioned hosted engine"

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

# Reconcile once against the newly active release before the portal starts,
# then keep doing so independently at boot and on cadence. This is deliberately
# not a child or dependency of pog-portal.service: it is the recovery path when
# that controller dies. systemd reads the root-only EnvironmentFile and passes
# only the resulting environment to the unprivileged pog process.
systemctl start pog-worker-finalizer.service
[ "$(systemctl show --property Result --value pog-worker-finalizer.service)" = "success" ] \
	|| die "durable worker finalizer did not complete successfully after activation"
systemctl enable --now pog-worker-finalizer.timer >/dev/null

# Mirror the token to the POG colony runner (if this host runs one) as
# POG_RUNNER_TOKEN — the env var POG's runner-auth.mjs sends as a bearer.
# The drop-in references a root-only env file so the secret never sits in a
# world-readable unit file.
if systemctl cat pog-colony-runner.service >/dev/null 2>&1; then
	{
		printf 'POG_RUNNER_TOKEN=%s\n' "$colony_token"
		printf 'POG_GEARS_RUST_SRC=/opt/pog/members/gears-rust\n'
		printf 'POG_AGENT_RUNNER_DB=/var/lib/kitsoki-pog/sessions.db\n'
		# POG's compatibility bridge delegates lifecycle operations to the
		# helper shipped beside the exact-revision hosted binary. Pointing at
		# the activated immutable release removes the former dependency on the
		# mutable /opt/kitsoki-src checkout and its recursively growing
		# .capsules tree.
		printf 'KITSOKI_SOURCE_DIR=/opt/kitsoki-hosted-pog/current\n'
		# The reaper resolves the lifecycle CLI lazily only when a terminal
		# workspace is eligible. Pin it to the same activated hosted engine as
		# the portal instead of falling back to a developer checkout that does
		# not exist on the orchestrator.
		printf 'POG_KITSOKI_BIN=%s\n' "$hosted_engine"
	} >"$stage/pog-colony-runner.env"
	install -m 0600 "$stage/pog-colony-runner.env" /etc/kitsoki/pog-colony-runner.env
	install -d -m 0755 /etc/systemd/system/pog-colony-runner.service.d
	printf '[Service]\nEnvironmentFile=-/etc/kitsoki/pog-colony-runner.env\n' >/etc/systemd/system/pog-colony-runner.service.d/runner-token.conf
	systemctl daemon-reload
	# A successful hosted activation owns returning the colony to service even
	# when an operator deliberately held it before deployment. Rollback still
	# restores the prior active/inactive state via colony_was_active.
	systemctl restart pog-colony-runner.service
	colony_environment="$(systemctl show --property Environment --value pog-colony-runner.service)"
	colony_pid="$(systemctl show --property MainPID --value pog-colony-runner.service)"
	[ "$colony_pid" -gt 0 ] || die "colony runner has no live process after restart"
	colony_process_environment="$(tr '\0' '\n' <"/proc/$colony_pid/environ")"
	grep -Fq 'POG_PORTFOLIO_ROOT=/opt/pog/current' <<<"$colony_environment" \
		|| die "colony runner lacks the hosted portfolio root authority"
	grep -Fq "POG_MEMBER_ROOTS=$member_roots" <<<"$colony_environment" \
		|| die "colony runner lacks the rendered member-root authority"
	grep -Fq "POG_PORTFOLIO_MEMBERS=$portfolio_members" <<<"$colony_environment" \
		|| die "colony runner lacks the rendered portfolio-member authority"
	grep -Fq "POG_KITSOKI_BIN=$hosted_engine" <<<"$colony_environment" \
		|| die "colony runner lacks the activated hosted Kitsoki engine"
	grep -Fxq 'KITSOKI_SOURCE_DIR=/opt/kitsoki-hosted-pog/current' <<<"$colony_process_environment" \
		|| die "colony runner still depends on a mutable Kitsoki source checkout"
fi

[ "$queue_worker_was_active" -eq 0 ] || systemctl restart kitsoki-queue-worker.service

systemctl restart pog-portal.service
for _ in $(seq 1 60); do
	if curl -fsS -o /dev/null http://127.0.0.1:7777/api/portal-health 2>/dev/null; then
		portal_ready=1
		break
	fi
	sleep 1
done
[ "${portal_ready:-0}" = "1" ] || die "POG production portal did not become ready on port 7777"

if [ -n "$previous_fixed_autonomously" ]; then
	current_fixed_autonomously="$(curl -fsS http://127.0.0.1:7777/api/feedback-autonomy/scoreboard | "$node_release/bin/node" -e '
const fs = require("node:fs");
const count = JSON.parse(fs.readFileSync(0, "utf8"))?.counts?.fixed_autonomously;
if (!Number.isSafeInteger(count) || count < 0) process.exit(1);
process.stdout.write(String(count));
')" || die "POG autonomy scoreboard is unreadable after activation"
	[ "$current_fixed_autonomously" -ge "$previous_fixed_autonomously" ] \
		|| die "POG autonomy scoreboard regressed from $previous_fixed_autonomously to $current_fixed_autonomously"
fi

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
[ -L /opt/pog/current/.capsules ] \
	|| die "POG release does not link to stable Capsule control-plane state"
[ "$(readlink -f /opt/pog/current/.capsules)" = "$(readlink -f "$capsule_state_root")" ] \
	|| die "POG Capsule control-plane link does not resolve to $capsule_state_root"
systemctl is-enabled --quiet pog-worker-finalizer.timer \
	|| die "durable worker finalizer timer is not enabled"
systemctl is-active --quiet pog-worker-finalizer.timer \
	|| die "durable worker finalizer timer is not active"
[ "$(systemctl show --property Result --value pog-worker-finalizer.service)" = "success" ] \
	|| die "durable worker finalizer service has not completed successfully"
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

# Immutable releases are rollback material, not durable state: .artifacts and
# .capsules point at stable roots outside them. Keep two inactive rollbacks for
# each product after every fully verified activation, and preserve any older
# release still serving as a live process's working directory.
"$stage/prune-releases.sh" "$release_root" "$current" 2
"$stage/prune-releases.sh" "$kitsoki_release_root" "$kitsoki_current" 2

trap - EXIT
echo "hosted-pog install: active production POG $pog_sha on 127.0.0.1:7777 at $public_base_url (products=$portfolio_members; state=$state_mode; no Vite runtime; all content requires an invited session)"
