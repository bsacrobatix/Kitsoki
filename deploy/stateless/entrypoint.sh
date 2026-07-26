#!/bin/bash
# Entrypoint for the stateless kitsoki image. The root filesystem is expected
# to be read-only (docker run --read-only); /scratch is the single writable
# mount. Volumes/tmpfs start empty, so create the state/tmp/cache layout the
# image's KITSOKI_STATE_DIR / XDG_DATA_HOME / TMPDIR / KITSOKI_CACHE_DIR env
# points at, then exec kitsoki with whatever subcommand the container was given.
set -euo pipefail
mkdir -p /scratch/state /scratch/tmp /scratch/cache
exec kitsoki "$@"
