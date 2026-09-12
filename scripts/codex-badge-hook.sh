#!/bin/sh
# Codex hook: appends the event JSON from stdin to the badge agent's event
# log. Codex's hook payload has the same shape as Claude Code's (session_id,
# hook_event_name, cwd), so only a provider tag is added. Installed to
# ~/.claude-badge/codex-hook.sh by scripts/install-hooks.sh.
set -u

dir="$HOME/.claude-badge"
file="$dir/events.jsonl"

mkdir -p "$dir"

# Keep only the fields the agent reads: PostToolUse events carry the whole
# tool output on stdin and would bloat the log otherwise. transcript_path
# names the thread's rollout, a fallback key should session_id ever differ
# from the thread id.
if command -v jq >/dev/null 2>&1; then
    payload=$(jq -c '{session_id, hook_event_name, cwd, transcript_path, provider: "codex"}' 2>/dev/null)
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
