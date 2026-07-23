#!/usr/bin/env bash
# Bind the release-independent Capsule control-plane store into the active POG
# project. A bind mount (not a symlink) keeps Kitsoki's workspace path inside
# project scope while CI jobs, vmpool receipts, queue state, and workspaces live
# outside /opt/pog/releases/<sha>.
set -euo pipefail

project="${1:-/opt/pog/current}"
state_root="${2:-/var/lib/pog/capsules}"

case "$project" in /*) ;; *) echo "capsule-state bind: project must be absolute" >&2; exit 2 ;; esac
case "$state_root" in /*) ;; *) echo "capsule-state bind: state root must be absolute" >&2; exit 2 ;; esac

project="$(readlink -f "$project")"
[ -d "$project" ] || { echo "capsule-state bind: project is missing: $project" >&2; exit 1; }
target="$project/.capsules"

install -d -o pog -g pog -m 0750 "$state_root"
for dir in workspaces ci vmpool queue; do
	install -d -o pog -g pog -m 0750 "$state_root/$dir"
done

if mountpoint -q "$target"; then
	[ "$(stat -c '%d:%i' "$target")" = "$(stat -c '%d:%i' "$state_root")" ] || {
		echo "capsule-state bind: $target is mounted from a different store" >&2
		exit 1
	}
	printf 'capsule-state bind: active %s -> %s\n' "$target" "$state_root"
	exit 0
fi

[ ! -L "$target" ] || {
	echo "capsule-state bind: refusing symlink target: $target" >&2
	exit 1
}
if [ -d "$target" ] && [ -n "$(find "$target" -mindepth 1 -print -quit)" ]; then
	echo "capsule-state bind: $target path is not empty; refusing implicit migration" >&2
	exit 1
fi
[ ! -e "$target" ] || [ -d "$target" ] || {
	echo "capsule-state bind: target is not a directory: $target" >&2
	exit 1
}
install -d -o pog -g pog -m 0750 "$target"
mount --bind "$state_root" "$target"
[ "$(stat -c '%d:%i' "$target")" = "$(stat -c '%d:%i' "$state_root")" ] || {
	echo "capsule-state bind: mounted target does not resolve to stable state" >&2
	exit 1
}
printf 'capsule-state bind: mounted %s -> %s\n' "$target" "$state_root"
