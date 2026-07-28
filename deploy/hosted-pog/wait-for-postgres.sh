#!/usr/bin/env bash
# ExecStartPre readiness gate for kitsoki-pog.service when the hosted install
# is configured for the postgres backend (see install.sh). Blocks the unit
# start until Postgres is accepting TCP connections, bounded by a timeout, so
# a slow-to-start database delays activation instead of the daemon crash-
# looping against a backend that is not up yet.
#
# Deliberately host/port only: this is a plain TCP reachability probe, not an
# authenticated connection, so it needs no password and prints nothing that
# could leak one. Authentication failures still surface promptly afterwards
# through the daemon's own Restart=always crash loop and its journal output.
set -euo pipefail

host="${1:-}"
port="${2:-}"
timeout_seconds="${3:-30}"

die() {
	echo "wait-for-postgres: $*" >&2
	exit 1
}

[ -n "$host" ] || die "usage: wait-for-postgres.sh <host> <port> [timeout-seconds]"
[ -n "$port" ] || die "usage: wait-for-postgres.sh <host> <port> [timeout-seconds]"
[[ "$port" =~ ^[0-9]+$ ]] || die "invalid port: $port"
[[ "$timeout_seconds" =~ ^[0-9]+$ ]] || die "invalid timeout: $timeout_seconds"

deadline=$((SECONDS + timeout_seconds))
while :; do
	if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then
		exec 3<&- 3>&-
		echo "wait-for-postgres: $host:$port is accepting connections"
		exit 0
	fi
	[ "$SECONDS" -lt "$deadline" ] || die "$host:$port did not become reachable within ${timeout_seconds}s"
	sleep 1
done
