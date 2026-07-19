#!/usr/bin/env bash
# Build the bounded local-state snapshot consumed by the hosted POG portal.
# This deliberately excludes the rest of .artifacts: caches, binaries, render
# scratch, workspaces, logs, and machine-local process identity are not product
# state and must not be uploaded to the collaboration host.
set -euo pipefail

die() {
	echo "hosted-pog state: $*" >&2
	exit 1
}

[ "$#" -eq 3 ] || die "usage: package-hosted-pog-state.sh <pog-root> <pog-commit-sha> <output-dir>"

pog_root="$(cd "$1" && pwd)"
pog_sha="$2"
output_dir="$3"
artifacts="$pog_root/.artifacts"

[[ "$pog_sha" =~ ^[0-9a-f]{40}$ ]] || die "invalid POG commit SHA"
[ -d "$pog_root/.git" ] || die "POG checkout is missing Git metadata: $pog_root"
[ -d "$artifacts" ] || die "POG local artifacts are missing: $artifacts"
command -v sqlite3 >/dev/null 2>&1 || die "sqlite3 is required for a consistent runner-session snapshot"
command -v jq >/dev/null 2>&1 || die "jq is required to write the state manifest"
command -v node >/dev/null 2>&1 || die "Node.js is required to digest the state snapshot"

mkdir -p "$output_dir"
snapshot="$(mktemp -d "${TMPDIR:-/tmp}/kitsoki-hosted-pog-state.XXXXXX")"
cleanup() {
	rm -rf -- "$snapshot"
}
trap cleanup EXIT

copy_root() {
	local name="$1" source="$artifacts/$1"
	[ -e "$source" ] || return 0
	if find "$source" \( -type l -o -type p -o -type s \) -print -quit | grep -q .; then
		die "state root contains a symlink or special file: $source"
	fi
	cp -R -p "$source" "$snapshot/$name"
}

copy_root feedback
copy_root graph-mcp
copy_root streams
copy_root colony

# PIDs and watch logs describe local processes, not durable product history.
# A copied PID can accidentally match an unrelated VM process and falsely make
# the hosted UI claim its local runner is alive.
rm -f -- "$snapshot/colony/runner.pid" "$snapshot/colony/runner-watch.log"

if [ -f "$artifacts/agent-runner/sessions.db" ]; then
	mkdir -p "$snapshot/agent-runner"
	sqlite3 "$artifacts/agent-runner/sessions.db" ".timeout 10000" ".backup '$snapshot/agent-runner/sessions.db'"
	sqlite3 "$snapshot/agent-runner/sessions.db" "PRAGMA quick_check;" | grep -qx ok \
		|| die "runner-session backup failed SQLite quick_check"
	rm -f -- "$snapshot/agent-runner/sessions.db-shm" "$snapshot/agent-runner/sessions.db-wal"
fi
if find "$snapshot" \( -type l -o -type p -o -type s \) -print -quit | grep -q .; then
	die "state changed while it was copied and introduced a symlink or special file"
fi

file_count="$(find "$snapshot" -type f | wc -l | tr -d ' ')"
byte_count=0
while IFS= read -r -d '' state_file; do
	byte_count=$((byte_count + $(wc -c <"$state_file")))
done < <(find "$snapshot" -type f -print0)
created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
content_sha256="$(node -e '
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const root = process.argv[1];
const hash = crypto.createHash("sha256");
function walk(directory, prefix = "") {
  const entries = fs.readdirSync(directory, { withFileTypes: true })
    .sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
  for (const entry of entries) {
    const relative = prefix ? `${prefix}/${entry.name}` : entry.name;
    const absolute = path.join(directory, entry.name);
    if (entry.isDirectory()) walk(absolute, relative);
    else if (entry.isFile()) {
      const content = fs.readFileSync(absolute);
      hash.update(relative); hash.update("\0");
      hash.update(String(content.length)); hash.update("\0");
      hash.update(content);
    } else process.exit(2);
  }
}
walk(root);
process.stdout.write(hash.digest("hex"));
' "$snapshot")"
[[ "$content_sha256" =~ ^[0-9a-f]{64}$ ]] || die "could not digest local state"

jq -n \
	--arg schema "kitsoki/hosted-pog-local-state/v1" \
	--arg pog_sha "$pog_sha" \
	--arg created_at "$created_at" \
	--arg content_sha256 "$content_sha256" \
	--argjson file_count "$file_count" \
	--argjson byte_count "$byte_count" \
	'{
		schema: $schema,
		pog_sha: $pog_sha,
		created_at: $created_at,
		content_sha256: $content_sha256,
		file_count: $file_count,
		byte_count: $byte_count,
		roots: ["feedback", "graph-mcp", "streams", "colony", "agent-runner/sessions.db"],
		excludes: ["colony/runner.pid", "colony/runner-watch.log", "caches", "binaries", "render-scratch", "temporary-workspaces"]
	}' >"$snapshot/manifest.json"

archive="$output_dir/pog-state.tar.gz"
checksum="$output_dir/pog-state.sha256"
# macOS otherwise writes LIBARCHIVE xattr records (and `._*` compatibility
# members) that GNU tar warns about and the remote allowlist correctly rejects.
COPYFILE_DISABLE=1 tar --no-xattrs -C "$snapshot" -czf "$archive" .
shasum -a 256 "$archive" | awk '{print $1}' >"$checksum"

echo "hosted-pog state: $file_count portal-state file(s), $byte_count byte(s), content $content_sha256, archive $(cat "$checksum")"
