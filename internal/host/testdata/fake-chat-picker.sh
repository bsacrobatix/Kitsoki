#!/usr/bin/env bash
# fake-chat-picker.sh — emulates `claude -p --model haiku ...` for the
# ChatResolveRefHandler LLM fallback. Behavior is keyed off the prompt:
#
#   ref contains MAGIC_SHALLOW → shallow pass picks 2; deep never runs
#   ref contains MAGIC_DEEP    → shallow returns NONE; deep picks 1
#   ref contains MAGIC_NONE    → both passes return NONE
#   otherwise                  → both passes return NONE
#
# Pass detection: the deep-pass prompt includes the literal phrase
# "by reading the transcripts" (see buildDeepPickPrompt). Otherwise it's the
# shallow pass.
set -euo pipefail

prompt="$(cat /dev/stdin)"

is_deep=0
if echo "$prompt" | grep -q "by reading the transcripts"; then
  is_deep=1
fi

if echo "$prompt" | grep -q MAGIC_SHALLOW; then
  if [ "$is_deep" -eq 0 ]; then
    printf '2\nshallow match\n'
  else
    printf 'NONE\nshould-not-reach-deep\n'
  fi
  exit 0
fi

if echo "$prompt" | grep -q MAGIC_DEEP; then
  if [ "$is_deep" -eq 0 ]; then
    printf 'NONE\nshallow no match\n'
  else
    printf '1\ndeep match\n'
  fi
  exit 0
fi

# default (MAGIC_NONE or anything else): both passes return NONE
printf 'NONE\nno match\n'
