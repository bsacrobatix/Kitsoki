#!/usr/bin/env bash
# Import immutable worker ship evidence from a pre-runtime hosted POG release.
#
# Older releases kept .artifacts inside /opt/pog/releases/<sha>. A deployment
# that replaced /opt/pog/current with a release whose .artifacts points at the
# versioned runtime therefore made valid worker ship records disappear from the
# autonomy scoreboard. Only the immutable scoreboard inputs are migrated here;
# bulky caches, logs, and mutable process state stay out of the runtime import.
set -euo pipefail

[ "$#" -eq 2 ] || {
	echo "usage: import-legacy-worker-ships.sh <legacy-artifacts> <durable-runtime>" >&2
	exit 2
}

legacy_artifacts="$1"
durable_runtime="$2"
source_root="$legacy_artifacts/feedback/dispatch"
destination_root="$durable_runtime/feedback/dispatch"

[ -d "$source_root" ] || {
	printf 'legacy worker ship import: imported=0\n'
	exit 0
}
[ -d "$durable_runtime" ] || {
	echo "legacy worker ship import: durable runtime is missing: $durable_runtime" >&2
	exit 1
}

ship_records() {
	find "$source_root" -mindepth 2 -maxdepth 2 -type f \
		\( -name 'pog-bugfix-result.json' -o -name '*.pog-bugfix-landing.json' \) \
		-print0
}

# Preflight every destination before copying anything. A dispatch identity is
# immutable: identical evidence is an idempotent replay; different evidence at
# the same path is corruption and must stop the deployment.
while IFS= read -r -d '' source; do
	dispatch_id="$(basename "$(dirname "$source")")"
	case "$dispatch_id" in
		''|*[!A-Za-z0-9._-]*)
			echo "legacy worker ship import: unsafe dispatch id: $dispatch_id" >&2
			exit 1
			;;
	esac
	destination="$destination_root/$dispatch_id/$(basename "$source")"
	if [ -L "$destination" ]; then
		echo "legacy worker ship import: destination is a symlink: $destination" >&2
		exit 1
	fi
	if [ -e "$destination" ] && ! cmp -s "$source" "$destination"; then
		echo "legacy worker ship import: divergent durable evidence: $destination" >&2
		exit 1
	fi
done < <(ship_records)

imported=0
while IFS= read -r -d '' source; do
	dispatch_id="$(basename "$(dirname "$source")")"
	destination_dir="$destination_root/$dispatch_id"
	destination="$destination_dir/$(basename "$source")"
	[ -e "$destination" ] && continue
	mkdir -p "$destination_dir"
	temporary="$destination.importing.$$"
	cp -p "$source" "$temporary"
	mv "$temporary" "$destination"
	imported=$((imported + 1))
done < <(ship_records)

printf 'legacy worker ship import: imported=%s\n' "$imported"
