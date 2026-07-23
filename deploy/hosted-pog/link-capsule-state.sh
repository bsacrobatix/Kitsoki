#!/usr/bin/env bash
# Give every hosted POG release one canonical Capsule control-plane path.
# Symlink resolution makes absolute workspace paths persisted by an older
# release converge with the current release's workspace grant.
set -euo pipefail

project="${1:-/opt/pog/current}"
state_root="${2:-/var/lib/pog/capsules}"

case "$project" in /*) ;; *) echo "capsule-state link: project must be absolute" >&2; exit 2 ;; esac
case "$state_root" in /*) ;; *) echo "capsule-state link: state root must be absolute" >&2; exit 2 ;; esac

project="$(readlink -f "$project")"
[ -d "$project" ] || { echo "capsule-state link: project is missing: $project" >&2; exit 1; }
target="$project/.capsules"

install -d -o pog -g pog -m 0750 "$state_root"
for dir in workspaces ci vmpool queue; do
	install -d -o pog -g pog -m 0750 "$state_root/$dir"
done

if [ -L "$target" ]; then
	[ "$(readlink -f "$target")" = "$(readlink -f "$state_root")" ] || {
		echo "capsule-state link: $target points at a different store" >&2
		exit 1
	}
	chown -h pog:pog "$target"
	printf 'capsule-state link: active %s -> %s\n' "$target" "$state_root"
	exit 0
fi

# Upgrade the prior bind-mount implementation without forcing an in-use
# mount. Deployment stops every Capsule consumer first; at boot no consumer
# has started because this unit orders them after itself.
if mountpoint -q "$target"; then
	[ "$(stat -c '%d:%i' "$target")" = "$(stat -c '%d:%i' "$state_root")" ] || {
		echo "capsule-state link: $target is mounted from a different store" >&2
		exit 1
	}
	umount "$target" || {
		echo "capsule-state link: $target is busy; refusing forced unmount" >&2
		exit 1
	}
fi

if [ -d "$target" ] && [ -n "$(find "$target" -mindepth 1 -print -quit)" ]; then
	echo "capsule-state link: $target path is not empty; refusing implicit migration" >&2
	exit 1
fi
[ ! -e "$target" ] || [ -d "$target" ] || {
	echo "capsule-state link: target is not a directory: $target" >&2
	exit 1
}
[ ! -d "$target" ] || rmdir "$target"
ln -s "$state_root" "$target"
chown -h pog:pog "$target"
[ "$(readlink -f "$target")" = "$(readlink -f "$state_root")" ] || {
	echo "capsule-state link: target does not resolve to stable state" >&2
	exit 1
}
printf 'capsule-state link: linked %s -> %s\n' "$target" "$state_root"
