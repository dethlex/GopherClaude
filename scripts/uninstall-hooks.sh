#!/bin/sh
# Removes the badge hook entries (current and legacy paths) from
# ~/.claude/settings.json and ~/.codex/hooks.json and the "gopher-badge"
# group from ~/.gemini/config/hooks.json, with backups.
set -eu

SETTINGS="${CLAUDE_SETTINGS:-$HOME/.claude/settings.json}"
AGY_HOOKS="${AGY_HOOKS:-$HOME/.gemini/config/hooks.json}"
CODEX_HOOKS="${CODEX_HOOKS:-${CODEX_HOME:-$HOME/.codex}/hooks.json}"
BADGE_DIR="${BADGE_DIR:-$HOME/.claude-badge}"
OURS="$BADGE_DIR/hook.sh $BADGE_DIR/agy-hook.sh $BADGE_DIR/codex-hook.sh"

command -v jq >/dev/null 2>&1 || {
    echo "error: jq is required (brew install jq)" >&2
    exit 1
}

# strip_ours FILE: drops every command hook of ours from a Claude Code-style
# hooks object and the event keys left empty.
strip_ours() {
    file="$1"
    [ -f "$file" ] || return 0

    cp "$file" "$file.bak-badge-$(date +%Y%m%d%H%M%S)"

    tmp=$(mktemp)
    jq --arg ours "$OURS" '
      def mine: $ours | split(" ");
      if .hooks then
        .hooks |= (
          with_entries(.value |= map(select(((.hooks // []) | map(.command) | any(IN(mine[]))) | not)))
          | with_entries(select(.value != []))
        )
      else . end
    ' "$file" > "$tmp"
    mv "$tmp" "$file"

    echo "Badge hooks removed from $file"
}

strip_ours "$SETTINGS"
strip_ours "$CODEX_HOOKS"

if [ -f "$AGY_HOOKS" ]; then
    cp "$AGY_HOOKS" "$AGY_HOOKS.bak-badge-$(date +%Y%m%d%H%M%S)"

    tmp=$(mktemp)
    jq 'del(.["gopher-badge"])' "$AGY_HOOKS" > "$tmp"
    mv "$tmp" "$AGY_HOOKS"

    echo "Antigravity badge hooks removed from $AGY_HOOKS"
fi
