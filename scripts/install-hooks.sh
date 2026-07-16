#!/bin/sh
# Installs the badge hook into ~/.claude/settings.json (idempotent, with a
# backup). The hook gives the agent exact "waiting for permission/input"
# signals; without it the agent falls back to transcript heuristics that
# cannot distinguish a permission dialog from a running tool.
set -eu

SETTINGS="${CLAUDE_SETTINGS:-$HOME/.claude/settings.json}"
BADGE_DIR="$HOME/.claude-badge"
HOOK="$BADGE_DIR/hook.sh"
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

command -v jq >/dev/null 2>&1 || {
    echo "error: jq is required (brew install jq)" >&2
    exit 1
}

mkdir -p "$BADGE_DIR"
cp "$SCRIPT_DIR/claude-badge-hook.sh" "$HOOK"
chmod +x "$HOOK"

[ -f "$SETTINGS" ] || printf '{}\n' > "$SETTINGS"

backup="$SETTINGS.bak-badge-$(date +%Y%m%d%H%M%S)"
cp "$SETTINGS" "$backup"

tmp=$(mktemp)
jq --arg cmd "$HOOK" '
  def entry(m):
    (if m == "" then {} else {matcher: m} end)
    + {hooks: [{type: "command", command: $cmd, async: true}]};
  def ensure(ev; m):
    .hooks[ev] = (((.hooks[ev]) // [])
      | map(select(((.hooks // []) | map(.command) | index($cmd)) == null)))
      + [entry(m)];
  .hooks = (.hooks // {})
  | ensure("Notification"; "permission_prompt|idle_prompt")
  | ensure("Stop"; "")
  | ensure("UserPromptSubmit"; "")
  | ensure("PostToolUse"; "")
  | ensure("SessionStart"; "")
  | ensure("SessionEnd"; "")
' "$SETTINGS" > "$tmp"

mv "$tmp" "$SETTINGS"

echo "Badge hooks installed into $SETTINGS"
echo "Backup saved to $backup"
echo "Note: only sessions started from now on will emit hook events."
