#!/bin/sh
# Installs the badge hook (idempotent, with backups) into every assistant
# that is present:
#   - Claude Code: ~/.claude/settings.json
#   - Antigravity CLI (agy): ~/.gemini/config/hooks.json
#   - Codex: ~/.codex/hooks.json ($CODEX_HOME/hooks.json)
# Hooks give the agent exact "waiting for permission/input" signals; without
# them it falls back to heuristics that cannot see a permission dialog.
set -eu

SETTINGS="${CLAUDE_SETTINGS:-$HOME/.claude/settings.json}"
AGY_HOOKS="${AGY_HOOKS:-$HOME/.gemini/config/hooks.json}"
CODEX_HOOKS="${CODEX_HOOKS:-${CODEX_HOME:-$HOME/.codex}/hooks.json}"
# BADGE_DIR is overridable so a dry run against scratch configs never touches
# the real hook directory the live configs point at.
BADGE_DIR="${BADGE_DIR:-$HOME/.claude-badge}"
HOOK="$BADGE_DIR/hook.sh"
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

# Hook paths earlier versions registered; their entries are replaced by the
# single script and the files removed once every config has been rewritten.
LEGACY_HOOKS="$BADGE_DIR/agy-hook.sh $BADGE_DIR/codex-hook.sh"

command -v jq >/dev/null 2>&1 || {
    echo "error: jq is required (brew install jq)" >&2
    exit 1
}

mkdir -p "$BADGE_DIR"
cp "$SCRIPT_DIR/badge-hook.sh" "$HOOK"
chmod +x "$HOOK"

# backup FILE: creates the file when missing and keeps a timestamped copy.
backup() {
    [ -f "$1" ] || printf '{}\n' > "$1"
    cp "$1" "$1.bak-badge-$(date +%Y%m%d%H%M%S)"
}

# merge_claude_schema FILE EXTRA_JSON EVENT[:MATCHER]...
# Adds one command hook per event to a Claude Code-style hooks object
# ({"hooks": {Event: [{matcher?, hooks: [{type, command, ...}]}]}}), which
# Codex reads too. Entries of ours (current or legacy path) are replaced,
# everyone else's are kept. EXTRA_JSON is merged into the hook entry: Claude
# Code takes {"async": true}, Codex {"timeout": 5}.
merge_claude_schema() {
    file="$1"
    extra="$2"
    shift 2

    tmp=$(mktemp)
    jq --arg cmd "$HOOK" --arg legacy "$LEGACY_HOOKS" --argjson extra "$extra" --arg events "$*" '
      def ours: [$cmd] + ($legacy | split(" "));
      def entry(m):
        (if m == "" then {} else {matcher: m} end)
        + {hooks: [({type: "command", command: $cmd} + $extra)]};
      def ensure(ev; m):
        .hooks[ev] = (((.hooks[ev]) // [])
          | map(select(((.hooks // []) | map(.command) | any(IN(ours[]))) | not)))
          + [entry(m)];
      .hooks = (.hooks // {})
      | reduce ($events | split(" ")[] | split(":")) as $spec (.; ensure($spec[0]; ($spec[1] // "")))
    ' "$file" > "$tmp"
    mv "$tmp" "$file"
}

# --- Claude Code -----------------------------------------------------------
backup "$SETTINGS"
merge_claude_schema "$SETTINGS" '{"async": true}' \
    "Notification:permission_prompt|idle_prompt" Stop UserPromptSubmit PostToolUse SessionStart SessionEnd
echo "Claude Code hooks installed into $SETTINGS"

# --- Antigravity CLI -------------------------------------------------------
if [ -d "$(dirname "$AGY_HOOKS")" ]; then
    backup "$AGY_HOOKS"

    # agy groups hooks by an arbitrary name -> event -> entries; we own the
    # "gopher-badge" group and replace it wholesale.
    tmp=$(mktemp)
    jq --arg cmd "$HOOK" '
      def entry: {hooks: [{type: "command", command: $cmd, timeout: 5}]};
      .["gopher-badge"] = {
        Stop: [entry], Idle: [entry], Notification: [entry],
        PostToolUse: [entry], SessionStart: [entry]
      }
    ' "$AGY_HOOKS" > "$tmp"
    mv "$tmp" "$AGY_HOOKS"

    echo "Antigravity hooks installed into $AGY_HOOKS"
else
    echo "Antigravity config dir not found, skipping agy hooks"
fi

# --- Codex -----------------------------------------------------------------
if [ -d "$(dirname "$CODEX_HOOKS")" ]; then
    backup "$CODEX_HOOKS"
    # The notify setting in config.toml is left alone: it belongs to whatever
    # the user pointed it at (Computer Use, a notifier).
    merge_claude_schema "$CODEX_HOOKS" '{"timeout": 5}' \
        UserPromptSubmit PostToolUse PermissionRequest Stop Interrupt
    echo "Codex hooks installed into $CODEX_HOOKS"
else
    echo "Codex data dir not found, skipping codex hooks"
fi

# Only now, with every config pointing at the single script, the old copies
# can go: an aborted run above must not leave a config pointing at nothing.
for legacy in $LEGACY_HOOKS; do
    rm -f "$legacy"
done

echo "Backups sit next to each file (*.bak-badge-*). Only sessions started from now on emit hook events."
