#!/bin/sh
# Stops and removes the launchd service. Keeps events.jsonl and the log.
set -eu

LABEL="com.claudecontrol.badge-agent"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
rm -f "$PLIST" "$HOME/.claude-badge/claude-badge-agent"

echo "Service removed: $LABEL"
