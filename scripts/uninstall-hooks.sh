#!/bin/sh
# Removes the badge hook entries from ~/.claude/settings.json (with a backup).
set -eu

SETTINGS="${CLAUDE_SETTINGS:-$HOME/.claude/settings.json}"
HOOK="$HOME/.claude-badge/hook.sh"

command -v jq >/dev/null 2>&1 || {
    echo "error: jq is required (brew install jq)" >&2
    exit 1
}

[ -f "$SETTINGS" ] || {
    echo "nothing to do: $SETTINGS does not exist"
    exit 0
}

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

echo "Badge hooks removed from $SETTINGS"
echo "Backup saved to $backup"
