# Source from an interactive shell in a repository where the launch-policy
# pack is installed. It makes bare Claude/Codex launches policy-aware and also
# routes Kitsoki-hosted backend calls through the same wrappers.
# Works when sourced from bash or zsh (zsh leaves BASH_SOURCE unset, so
# resolve this file's own path per shell; the zsh expansion hides behind eval
# so bash never parses it).
if [ -n "${BASH_SOURCE:-}" ]; then
  _kitsoki_launch_policy_src="${BASH_SOURCE[0]}"
elif [ -n "${ZSH_VERSION:-}" ]; then
  eval '_kitsoki_launch_policy_src="${(%):-%x}"'
else
  _kitsoki_launch_policy_src="$0"
fi
_kitsoki_launch_policy_root="$(cd "$(dirname "$_kitsoki_launch_policy_src")/.." && pwd -P)"
# Agent Mail is intentionally machine-local: Capsules and worktrees need the
# same coordination endpoint, while its bearer token must never be checked into
# any participating repository.  The optional file is sourced by the shell
# that activates the launcher shims, so every descendant Claude/Codex launch
# inherits the endpoint and token.  A missing file preserves the normal
# launcher behavior.
_kitsoki_agent_mail_env="${KITSOKI_AGENT_MAIL_ENV:-${XDG_CONFIG_HOME:-$HOME/.config}/kitsoki/agent-mail.env}"
if [ -r "$_kitsoki_agent_mail_env" ]; then
  # shellcheck disable=SC1090 -- a user-owned, documented local configuration.
  . "$_kitsoki_agent_mail_env"
fi
case ":$PATH:" in
  *":$_kitsoki_launch_policy_root/.kitsoki/bin:"*) ;;
  *) export PATH="$_kitsoki_launch_policy_root/.kitsoki/bin:$PATH" ;;
esac
export KITSOKI_AGENT_CLAUDE_BIN="$_kitsoki_launch_policy_root/.kitsoki/bin/claude"
export KITSOKI_AGENT_CODEX_BIN="$_kitsoki_launch_policy_root/.kitsoki/bin/codex"
unset _kitsoki_agent_mail_env _kitsoki_launch_policy_root _kitsoki_launch_policy_src
