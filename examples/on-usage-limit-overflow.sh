#!/bin/sh
# Example on_usage_limit_command: when the profile's harness stops at its
# usage limit, continue the job in pi on another model (for example an
# OpenAI-compatible endpoint you have configured in pi's models.json).
#
# Redline runs this in the kept workspace, with the task prompt on stdin.
# Exit 0 tells Redline the job is finished; anything else leaves the run
# failed with the workspace kept for a manual resume.
set -eu

: "${OVERFLOW_MODEL:?set OVERFLOW_MODEL to the pi model to continue on, e.g. openrouter/qwen/qwen3-coder}"

prompt=$(cat)

# The work so far is already in this workspace. Tell the new agent so it
# continues rather than starting over.
{
  printf '%s\n\n' "$prompt"
  printf 'Note: a previous agent (%s) started this task in this workspace and stopped at its usage limit.\n' "$REDLINE_HARNESS"
  printf 'Inspect the current state (git status, git diff, git log) and finish the remaining work; do not redo completed steps.\n'
  if [ -n "${REDLINE_OUTPUT_FILE:-}" ]; then
    printf "The previous agent's transcript is at %s if you need its reasoning.\n" "$REDLINE_OUTPUT_FILE"
  fi
} | pi --print --model "$OVERFLOW_MODEL" --name "redline-overflow-$REDLINE_RUN_ID"
