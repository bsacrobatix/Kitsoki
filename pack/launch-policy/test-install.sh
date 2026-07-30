#!/usr/bin/env bash
# Hermetic install + K1 red-team proof. The temporary repo is intentional: this
# test verifies Git hook installation and exact Git command behavior.
set -euo pipefail
pack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.name "Launch Policy Gate"
git -C "$repo" config user.email "launch-policy@example.invalid"
git -C "$repo" commit -q --allow-empty -m init

"$pack_dir/install.sh" "$repo" --no-siblings
test -x "$repo/.claude/hooks/block-bare-checkout.sh"
test -x "$repo/.kitsoki/bin/claude"
test -x "$repo/.kitsoki/bin/codex"
test -x "$repo/scripts/kitsoki-promotion-route.sh"
cmp -s "$pack_dir/scripts/kitsoki-promotion-route.sh" "$repo/scripts/kitsoki-promotion-route.sh" ||
  { echo "FAIL: installed promotion route differs from pack authority" >&2; exit 1; }
test -f "$repo/.kitsoki/launch-policy.sh"
grep -q 'KITSOKI_AGENT_CLAUDE_BIN' "$repo/.kitsoki/launch-policy.sh"
node -e 'JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"))' "$repo/.claude/settings.json"
grep -q 'require_capsule: false' "$repo/.kitsoki.local.yaml"
grep -q '\.capsules/workspaces' "$repo/.kitsoki.local.yaml"
# `unlimited` needs .worktrees carved out of the protected root, and the Claude
# guard must not police direct git there — that mode is direct git by design.
grep -q '^    - \.worktrees$' "$repo/.kitsoki.local.yaml" \
  || { echo "FAIL: allowed_roots is missing .worktrees" >&2; exit 1; }
grep -q '\.worktrees/\*) exit 0' "$repo/.claude/hooks/block-bare-checkout.sh" \
  || { echo "FAIL: Claude guard does not exempt .worktrees" >&2; exit 1; }

if git -C "$repo" checkout -q -b should-be-blocked 2>/dev/null; then
  echo "FAIL: reference-transaction did not pin primary checkout" >&2
  exit 1
fi
(cd "$repo" && scripts/launch-policy-gate.sh)

# Generated launcher shims are pack-owned. A stale installed shim must be
# upgraded without requiring --force, otherwise `codex superagent` can keep
# calling the wrong workspace creation surface after the template is fixed.
sed_i() { sed "$1" "$2" > "$2.tmp" && mv "$2.tmp" "$2"; }
sed_i 's/capsule workspace create-script/capsule workspace create \\\n    --definition development/' "$repo/.kitsoki/bin/codex"
"$pack_dir/install.sh" "$repo" --no-siblings >/dev/null
grep -q 'capsule workspace create-script' "$repo/.kitsoki/bin/codex" || { echo "FAIL: stale codex shim was not upgraded" >&2; exit 1; }
grep -q 'capsule workspace create-script' "$repo/.kitsoki/bin/claude" || { echo "FAIL: claude shim missing create-script" >&2; exit 1; }
for shim in claude codex; do
  grep -q '"${1:-}" = "unlimited"' "$repo/.kitsoki/bin/$shim" \
    || { echo "FAIL: $shim shim missing the unlimited arm" >&2; exit 1; }
done

out="$("$pack_dir/install.sh" "$repo" --no-siblings)"
grep -q 'no changes' <<<"$out" || { echo "FAIL: installer was not idempotent" >&2; exit 1; }

# The operating-principles managed block: created in AGENTS.md/README.md,
# refreshed in place on upgrade, and everything outside the markers survives.
grep -q 'BEGIN kitsoki:launch-policy' "$repo/AGENTS.md" || { echo "FAIL: AGENTS.md missing managed block" >&2; exit 1; }
grep -q 'BEGIN kitsoki:launch-policy' "$repo/README.md" || { echo "FAIL: README.md missing managed block" >&2; exit 1; }
printf '# My Repo\n\nlocal preamble\n\n' > "$repo/AGENTS.md"
"$pack_dir/install.sh" "$repo" --no-siblings >/dev/null
grep -q 'local preamble' "$repo/AGENTS.md" || { echo "FAIL: consumer AGENTS.md content lost" >&2; exit 1; }
grep -q 'BEGIN kitsoki:launch-policy' "$repo/AGENTS.md" || { echo "FAIL: block not re-added to rewritten AGENTS.md" >&2; exit 1; }
sed_i 's/Launch through the shims/HAND EDIT INSIDE BLOCK/' "$repo/AGENTS.md"
"$pack_dir/install.sh" "$repo" --no-siblings >/dev/null
grep -q 'HAND EDIT INSIDE BLOCK' "$repo/AGENTS.md" && { echo "FAIL: upgrade did not refresh block content" >&2; exit 1; }
grep -q 'local preamble' "$repo/AGENTS.md" || { echo "FAIL: refresh clobbered content outside markers" >&2; exit 1; }
count="$(grep -c 'BEGIN kitsoki:launch-policy' "$repo/AGENTS.md")"
[ "$count" -eq 1 ] || { echo "FAIL: $count managed blocks after refresh" >&2; exit 1; }
echo "PASS: operating-principles managed block upsert"

# A divergent managed file must be skipped without aborting the rest of the
# install, and the exit code must flag the partial convergence.
repo2="$tmp/repo2"
git init -q -b main "$repo2"
git -C "$repo2" config user.name "Launch Policy Gate"
git -C "$repo2" config user.email "launch-policy@example.invalid"
git -C "$repo2" commit -q --allow-empty -m init
echo "# locally customized" > "$repo2/.kitsoki.local.yaml"
rc=0
"$pack_dir/install.sh" "$repo2" --no-siblings 2>/dev/null || rc=$?
[ "$rc" -eq 3 ] || { echo "FAIL: skip did not exit 3 (got $rc)" >&2; exit 1; }
grep -q '# locally customized' "$repo2/.kitsoki.local.yaml" || { echo "FAIL: divergent file was overwritten" >&2; exit 1; }
test -x "$repo2/.kitsoki/bin/claude" || { echo "FAIL: skip aborted the remaining install" >&2; exit 1; }
test -x "$repo2/scripts/launch-policy-gate.sh" || { echo "FAIL: gate script missing after skip" >&2; exit 1; }
test -x "$repo2/scripts/kitsoki-promotion-route.sh" || { echo "FAIL: promotion route missing after skip" >&2; exit 1; }
echo "PASS: divergent-file skip continues installing"

# The activation file must self-locate under zsh (BASH_SOURCE is unset there)
# and keep PATH idempotent across repeated sourcing. It resolves physical
# paths (pwd -P), so compare against the symlink-resolved repo root.
repo_phys="$(cd "$repo" && pwd -P)"
agent_mail_env="$tmp/agent-mail.env"
cat > "$agent_mail_env" <<'ENV'
export KITSOKI_AGENT_MAIL_URL=http://127.0.0.1:8765/mcp/
export KITSOKI_AGENT_MAIL_TOKEN=launch-policy-test-token
ENV
for shell in bash zsh; do
  command -v "$shell" >/dev/null 2>&1 || continue
  got="$(KITSOKI_AGENT_MAIL_ENV="$agent_mail_env" "$shell" -c "source '$repo/.kitsoki/launch-policy.sh'; source '$repo/.kitsoki/launch-policy.sh'; printf '%s\n%s\n%s' \"\$KITSOKI_AGENT_CLAUDE_BIN\" \"\$PATH\" \"\$KITSOKI_AGENT_MAIL_URL\"")"
  bin_line="${got%%$'\n'*}"
  rest="${got#*$'\n'}"
  path_line="${rest%%$'\n'*}"
  agent_mail_url="${rest#*$'\n'}"
  [ "$bin_line" = "$repo_phys/.kitsoki/bin/claude" ] || { echo "FAIL: $shell resolved KITSOKI_AGENT_CLAUDE_BIN to $bin_line" >&2; exit 1; }
  count="$(printf '%s' "$path_line" | tr ':' '\n' | grep -cx "$repo_phys/.kitsoki/bin")" || true
  [ "$count" -eq 1 ] || { echo "FAIL: $shell PATH has $count shim entries after double-source" >&2; exit 1; }
  [ "$agent_mail_url" = "http://127.0.0.1:8765/mcp/" ] || { echo "FAIL: $shell did not load Agent Mail environment" >&2; exit 1; }
done
echo "PASS: activation file resolves Agent Mail under bash and zsh with idempotent PATH"
"$pack_dir/test-delegation.sh"
echo "PASS: launch-policy pack install and red-team gate"
