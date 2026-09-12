#!/bin/sh
# Badge hook for Claude Code, Antigravity (agy) and Codex: appends the event
# JSON from stdin to the agent's event log, reduced to the few fields the
# agent reads (PostToolUse payloads carry whole tool outputs otherwise).
# Installed as ~/.claude-badge/hook.sh by scripts/install-hooks.sh and
# registered in all three hook configs: the provider is recognised from the
# payload shape, so one file serves them all.
set -u

dir="$HOME/.claude-badge"
file="$dir/events.jsonl"

mkdir -p "$dir"

# Without jq the whole payload would land in the log; the installer requires
# jq, so its absence means "not set up" rather than "flood the file".
command -v jq >/dev/null 2>&1 || exit 0

payload=$(jq -c '
  if has("eventName") or has("conversationId") then
    # agy: camelCase names and a workspace list instead of cwd.
    {session_id: (.conversationId // .conversation_id // .sessionId // ""),
     hook_event_name: (.eventName // .hook_event_name // .eventType // ""),
     notification_type: (.notificationType // .notification_type // ""),
     message: ((.message // "") | tostring | .[0:120]),
     cwd: ((.workspacePaths // [])[0] // .cwd // ""),
     provider: "agy"}
  elif ((.transcript_path // "") | test("/sessions/[0-9]{4}/[0-9]{2}/[0-9]{2}/rollout-")) then
    # Codex: the Claude Code schema, told apart by its dated rollout path
    # (Claude transcripts live under projects/<dir>/<uuid>.jsonl), which
    # also survives a custom CODEX_HOME. transcript_path is kept as a
    # fallback key should session_id ever differ from the thread id.
    {session_id, hook_event_name, cwd, transcript_path, provider: "codex"}
  else
    {session_id, hook_event_name, notification_type, message, cwd, provider: "claude"}
  end' 2>/dev/null)
[ -n "$payload" ] || exit 0

printf '{"ts":%s,"event":%s}\n' "$(date +%s)" "$payload" >> "$file"

# Rotate once the log grows past ~1 MB; the agent only needs recent events.
size=$(wc -c < "$file" 2>/dev/null || echo 0)
if [ "$size" -gt 1048576 ]; then
    tail -n 500 "$file" > "$file.tmp" && mv "$file.tmp" "$file"
fi

exit 0
