#!/bin/sh
# Installs the badge hooks (idempotent, with backups):
#   - Claude Code: ~/.claude/settings.json
#   - Antigravity CLI (agy), if present: ~/.gemini/config/hooks.json
#   - Codex, if present: ~/.codex/hooks.json ($CODEX_HOME/hooks.json)
# Hooks give the agent exact "waiting for permission/input" signals; without
# them it falls back to heuristics that cannot see a permission dialog.
set -eu

SETTINGS="${CLAUDE_SETTINGS:-$HOME/.claude/settings.json}"
AGY_HOOKS="${AGY_HOOKS:-$HOME/.gemini/config/hooks.json}"
CODEX_HOOKS="${CODEX_HOOKS:-${CODEX_HOME:-$HOME/.codex}/hooks.json}"
BADGE_DIR="$HOME/.claude-badge"
HOOK="$BADGE_DIR/hook.sh"
AGY_HOOK="$BADGE_DIR/agy-hook.sh"
CODEX_HOOK="$BADGE_DIR/codex-hook.sh"
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

command -v jq >/dev/null 2>&1 || {
    echo "error: jq is required (brew install jq)" >&2
    exit 1
}

mkdir -p "$BADGE_DIR"
cp "$SCRIPT_DIR/claude-badge-hook.sh" "$HOOK"
cp "$SCRIPT_DIR/agy-badge-hook.sh" "$AGY_HOOK"
cp "$SCRIPT_DIR/codex-badge-hook.sh" "$CODEX_HOOK"
chmod +x "$HOOK" "$AGY_HOOK" "$CODEX_HOOK"

# --- Claude Code -----------------------------------------------------------
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

echo "Claude Code hooks installed into $SETTINGS (backup: $backup)"

# --- Antigravity CLI -------------------------------------------------------
if [ -d "$(dirname "$AGY_HOOKS")" ]; then
    [ -f "$AGY_HOOKS" ] || printf '{}\n' > "$AGY_HOOKS"

    abackup="$AGY_HOOKS.bak-badge-$(date +%Y%m%d%H%M%S)"
    cp "$AGY_HOOKS" "$abackup"

    # agy groups hooks by an arbitrary name -> event -> entries; we own the
    # "gopher-badge" group and replace it wholesale.
    tmp=$(mktemp)
    jq --arg cmd "$AGY_HOOK" '
      def entry: {hooks: [{type: "command", command: $cmd, timeout: 5}]};
      .["gopher-badge"] = {
        Stop: [entry], Idle: [entry], Notification: [entry],
        PostToolUse: [entry], SessionStart: [entry]
      }
    ' "$AGY_HOOKS" > "$tmp"

    mv "$tmp" "$AGY_HOOKS"

    echo "Antigravity hooks installed into $AGY_HOOKS (backup: $abackup)"
else
    echo "Antigravity config dir not found, skipping agy hooks"
fi

# --- Codex -----------------------------------------------------------------
if [ -d "$(dirname "$CODEX_HOOKS")" ]; then
    [ -f "$CODEX_HOOKS" ] || printf '{}\n' > "$CODEX_HOOKS"

    cbackup="$CODEX_HOOKS.bak-badge-$(date +%Y%m%d%H%M%S)"
    cp "$CODEX_HOOKS" "$cbackup"

    # Codex reads the Claude Code hook schema (event -> matcher groups ->
    # command hooks); a group of ours is replaced, never duplicated. The
    # notify setting in config.toml is left alone: it belongs to whatever
    # the user pointed it at (Computer Use, a notifier).
    tmp=$(mktemp)
    jq --arg cmd "$CODEX_HOOK" '
      def entry: {hooks: [{type: "command", command: $cmd, timeout: 5}]};
      def ensure(ev):
        .hooks[ev] = (((.hooks[ev]) // [])
          | map(select(((.hooks // []) | map(.command) | index($cmd)) == null)))
          + [entry];
      .hooks = (.hooks // {})
      | ensure("UserPromptSubmit")
      | ensure("PostToolUse")
      | ensure("PermissionRequest")
      | ensure("Stop")
      | ensure("Interrupt")
    ' "$CODEX_HOOKS" > "$tmp"

    mv "$tmp" "$CODEX_HOOKS"

    echo "Codex hooks installed into $CODEX_HOOKS (backup: $cbackup)"
else
    echo "Codex data dir not found, skipping codex hooks"
fi

echo "Note: only sessions started from now on will emit hook events."
