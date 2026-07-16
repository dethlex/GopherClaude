#!/bin/sh
# Installs the badge agent as a launchd user service: builds the binary into
# ~/.claude-badge (so the service does not depend on this checkout), writes a
# LaunchAgent plist and starts it. Re-running rebuilds and restarts. Idempotent.
set -eu

LABEL="com.claudecontrol.badge-agent"
AGENT_DIR="$HOME/.claude-badge"
BIN="$AGENT_DIR/claude-badge-agent"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG="$AGENT_DIR/agent.log"
REPO_DIR=$(cd "$(dirname "$0")/.." && pwd)
GUI_DOMAIN="gui/$(id -u)"

mkdir -p "$AGENT_DIR" "$HOME/Library/LaunchAgents"

echo "Building agent..."
(cd "$REPO_DIR" && go build -o "$BIN.tmp" ./cmd/agent)
mv "$BIN.tmp" "$BIN"

# Stop a previously installed service before replacing it.
launchctl bootout "$GUI_DOMAIN" "$PLIST" 2>/dev/null || true

# Kill stray foreground/dry-run instances so they do not hold the serial port.
pkill -f "claude-badge-agent" 2>/dev/null || true

cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>$LABEL</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BIN</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>StandardOutPath</key>
    <string>$LOG</string>
    <key>StandardErrorPath</key>
    <string>$LOG</string>
</dict>
</plist>
EOF

launchctl bootstrap "$GUI_DOMAIN" "$PLIST"

echo "Service installed and started: $LABEL"
echo "  status: launchctl print $GUI_DOMAIN/$LABEL | grep state"
echo "  logs:   tail -f $LOG"
echo "  stop:   make uninstall-agent"
