# Source from an interactive shell in a repository where the launch-policy
# pack is installed. It makes bare Claude/Codex launches policy-aware and also
# routes Kitsoki-hosted backend calls through the same wrappers.
_kitsoki_launch_policy_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
export PATH="$_kitsoki_launch_policy_root/.kitsoki/bin:$PATH"
export KITSOKI_AGENT_CLAUDE_BIN="$_kitsoki_launch_policy_root/.kitsoki/bin/claude"
export KITSOKI_AGENT_CODEX_BIN="$_kitsoki_launch_policy_root/.kitsoki/bin/codex"
unset _kitsoki_launch_policy_root
