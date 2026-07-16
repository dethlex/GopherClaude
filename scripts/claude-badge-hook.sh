#!/bin/sh
# Claude Code hook: appends the event JSON from stdin to the badge agent's
# event log. Installed to ~/.claude-badge/hook.sh by scripts/install-hooks.sh.
set -u

dir="$HOME/.claude-badge"
file="$dir/events.jsonl"

mkdir -p "$dir"

# Keep only the fields the agent reads: PostToolUse events carry the whole
# tool output on stdin (hundreds of KB) and would bloat the log otherwise.
if command -v jq >/dev/null 2>&1; then
    payload=$(jq -c '{session_id, hook_event_name, notification_type, message, cwd}' 2>/dev/null)
else
    payload=$(cat | tr -d '\n')
fi
[ -n "$payload" ] || exit 0

printf '{"ts":%s,"event":%s}\n' "$(date +%s)" "$payload" >> "$file"

# Rotate once the log grows past ~1 MB; the agent only needs recent events.
size=$(wc -c < "$file" 2>/dev/null || echo 0)
if [ "$size" -gt 1048576 ]; then
    tail -n 500 "$file" > "$file.tmp" && mv "$file.tmp" "$file"
fi

exit 0
