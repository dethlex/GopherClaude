#!/bin/sh
# Removes the badge hook entries from ~/.claude/settings.json, the
# "gopher-badge" group from ~/.gemini/config/hooks.json (if present) and
# the badge entries from ~/.codex/hooks.json (with backups).
set -eu

SETTINGS="${CLAUDE_SETTINGS:-$HOME/.claude/settings.json}"
AGY_HOOKS="${AGY_HOOKS:-$HOME/.gemini/config/hooks.json}"
HOOK="$HOME/.claude-badge/hook.sh"
CODEX_HOOKS="${CODEX_HOOKS:-${CODEX_HOME:-$HOME/.codex}/hooks.json}"
CODEX_HOOK="$HOME/.claude-badge/codex-hook.sh"

command -v jq >/dev/null 2>&1 || {
    echo "error: jq is required (brew install jq)" >&2
    exit 1
}

if [ -f "$SETTINGS" ]; then
    backup="$SETTINGS.bak-badge-$(date +%Y%m%d%H%M%S)"
    cp "$SETTINGS" "$backup"

    tmp=$(mktemp)
    jq --arg cmd "$HOOK" '
      if .hooks then
        .hooks |= (
          with_entries(
            .value |= map(select(((.hooks // []) | map(.command) | index($cmd)) == null))
          )
          | with_entries(select(.value != []))
        )
      else . end
    ' "$SETTINGS" > "$tmp"

    mv "$tmp" "$SETTINGS"
    echo "Claude Code badge hooks removed from $SETTINGS (backup: $backup)"
fi

if [ -f "$AGY_HOOKS" ]; then
    abackup="$AGY_HOOKS.bak-badge-$(date +%Y%m%d%H%M%S)"
    cp "$AGY_HOOKS" "$abackup"

    tmp=$(mktemp)
    jq 'del(.["gopher-badge"])' "$AGY_HOOKS" > "$tmp"
    mv "$tmp" "$AGY_HOOKS"
    echo "Antigravity badge hooks removed from $AGY_HOOKS (backup: $abackup)"
fi

if [ -f "$CODEX_HOOKS" ]; then
    cbackup="$CODEX_HOOKS.bak-badge-$(date +%Y%m%d%H%M%S)"
    cp "$CODEX_HOOKS" "$cbackup"

    tmp=$(mktemp)
    jq --arg cmd "$CODEX_HOOK" '
      if .hooks then
        .hooks |= (
          with_entries(
            .value |= map(select(((.hooks // []) | map(.command) | index($cmd)) == null))
          )
          | with_entries(select(.value != []))
        )
      else . end
    ' "$CODEX_HOOKS" > "$tmp"

    mv "$tmp" "$CODEX_HOOKS"
    echo "Codex badge hooks removed from $CODEX_HOOKS (backup: $cbackup)"
fi
