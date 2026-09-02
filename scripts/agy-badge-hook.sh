#!/bin/sh
# Antigravity (agy) hook: normalises agy's hook payload to the same record
# shape the Claude Code hook writes, so the badge agent reads both from one
# log. Installed to ~/.claude-badge/agy-hook.sh by scripts/install-hooks.sh.
#
# agy payload (camelCase):  {"eventName":"Stop","conversationId":"…","workspacePaths":["…"],…}
# record written:           {"ts":…,"event":{"session_id":…,"hook_event_name":…,"cwd":…,"provider":"agy"}}
set -u

dir="$HOME/.claude-badge"
file="$dir/events.jsonl"

mkdir -p "$dir"

command -v jq >/dev/null 2>&1 || exit 0

payload=$(jq -c '{
  session_id: (.conversationId // .conversation_id // .sessionId // ""),
  hook_event_name: (.eventName // .hook_event_name // .eventType // ""),
  notification_type: (.notificationType // .notification_type // ""),
  message: ((.message // "") | tostring | .[0:120]),
  cwd: ((.workspacePaths // [])[0] // .cwd // ""),
  provider: "agy",
  keys: keys
}' 2>/dev/null)
[ -n "$payload" ] || exit 0

printf '{"ts":%s,"event":%s}\n' "$(date +%s)" "$payload" >> "$file"

# Rotate once the log grows past ~1 MB; the agent only needs recent events.
size=$(wc -c < "$file" 2>/dev/null || echo 0)
if [ "$size" -gt 1048576 ]; then
    tail -n 500 "$file" > "$file.tmp" && mv "$file.tmp" "$file"
fi

exit 0
