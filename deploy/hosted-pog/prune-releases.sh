#!/usr/bin/env bash
# Keep hosted immutable release roots bounded after a verified activation.
set -euo pipefail

die() {
	echo "hosted-pog release retention: $*" >&2
	exit 1
}

[ "$#" -eq 3 ] || die "usage: prune-releases.sh <release-root> <current-symlink> <inactive-to-keep>"
release_root="$1"
current_link="$2"
inactive_to_keep="$3"

[[ "$release_root" = /* ]] || die "release root must be absolute"
[[ "$current_link" = /* ]] || die "current link must be absolute"
[[ "$inactive_to_keep" =~ ^[0-9]+$ ]] || die "inactive-to-keep must be a non-negative integer"
[ -d "$release_root" ] || die "release root does not exist: $release_root"
[ -L "$current_link" ] || die "current release is not a symlink: $current_link"

release_root="$(realpath "$release_root")"
current_release="$(realpath "$current_link")"
case "$current_release" in
	"$release_root"/*) ;;
	*) die "current release is outside $release_root: $current_release" ;;
esac

release_has_live_cwd() {
	local candidate="$1" cwd_link cwd
	for cwd_link in /proc/[0-9]*/cwd; do
		cwd="$(readlink -f "$cwd_link" 2>/dev/null || true)"
		case "$cwd" in
			"$candidate"|"$candidate"/*) return 0 ;;
		esac
	done
	return 1
}

kept_inactive=0
while IFS= read -r candidate; do
	[ -n "$candidate" ] || continue
	[ -d "$candidate" ] && [ ! -L "$candidate" ] || continue
	candidate="$(realpath "$candidate")"
	[ "$candidate" != "$current_release" ] || continue
	case "${candidate##*/}" in
		????????????????????????????????????????)
			[[ "${candidate##*/}" =~ ^[0-9a-f]{40}$ ]] || continue
			;;
		*) continue ;;
	esac
	if [ "$kept_inactive" -lt "$inactive_to_keep" ]; then
		kept_inactive=$((kept_inactive + 1))
		continue
	fi
	if release_has_live_cwd "$candidate"; then
		echo "hosted-pog release retention: preserve in-use release $candidate"
		continue
	fi
	echo "hosted-pog release retention: prune inactive release $candidate"
	rm -rf -- "$candidate"
done < <(LC_ALL=C ls -1dt "$release_root"/*/ 2>/dev/null || true)
