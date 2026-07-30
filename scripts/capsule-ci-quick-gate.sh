#!/usr/bin/env bash
# Fast, no-spend gate for branches landing in staging/local.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

echo "capsule-ci-quick: preparing embedded story and agent assets"
make embed-stories embed-skills >/dev/null
# The embed inputs are intentionally gitignored generated copies.  Do not let a
# fresh queue workspace reuse a Go test binary from another checkout that was
# compiled while those directories contained only their .gitkeep placeholders.
# A workspace-local cache is also safe for concurrent speculative gates.
export GOCACHE="${KITSOKI_QUICK_GATE_GOCACHE:-${KITSOKI_TEMP_ROOT:-$repo_root/.temp}/capsule-ci-quick-go-build}"
mkdir -p "$GOCACHE"
echo "capsule-ci-quick: checking diff hygiene"
git diff --check
echo "capsule-ci-quick: validating Capsule CI story"
go run ./cmd/kitsoki validate stories/capsule-ci/app.yaml
echo "capsule-ci-quick: replaying Capsule CI flow fixtures"
KITSOKI_CASSETTE_STRICT=1 go run ./cmd/kitsoki test flows stories/capsule-ci/app.yaml
echo "capsule-ci-quick: running focused short tests"
go test -short -count=1 ./internal/capsule/... ./internal/host ./cmd/kitsoki
echo "capsule-ci-quick: passed"
