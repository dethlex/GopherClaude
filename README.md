# GopherClaude — a Claude Code monitor on the Gopher Badge

Turn a [Gopher Badge](https://gopherbadge.com/) (RP2040, 320×240 screen, two
NeoPixels, a buzzer, an accelerometer) into a live, at-a-glance monitor for
[Claude Code](https://claude.com/claude-code) running on your Mac.

The badge has no WiFi, so a small Go agent on the Mac reads Claude Code's
local state and streams one text frame every 2 seconds to the badge over USB
serial. The badge renders it and can nudge you — or send you straight to a
blocked chat — with light and sound.

```
┌─────────────────────────────────┐
│ CLAUDE CONTROL              ◐ ● │
│ CHATS 4                  WAIT 2 │
│ 5-HOUR                   44% 3h │
│ [██████████··············]      │
│ WEEKLY                   17% 2d │
│ [████····················]      │
│ CREDITS                32.66/50 │
│ [████████████████········]      │
│ IN 156.4k  OUT 783.5k           │
│ ····· rotator INPUT ·····       │
└─────────────────────────────────┘
```

- **`◐`** — progress spinner: animates while any session is actually working.
- **`●`** — link status: green when the agent is connected, red when not.
- **Bars** — 5-hour / weekly / credits usage; blue, amber at ≥70%, red at ≥90%.
- **Bottom line** — the alert banner: `ALL QUIET`, the alerting project and
  reason (`PERM` / `INPUT`), or `NO LINK`.

## Features

- **Active chats & waiting count** — how many Claude Code sessions are live
  (`CHATS`) and how many are waiting on you (`WAIT`).
- **Plan-usage bars** — the same numbers as Claude Desktop's *Plan usage*
  panel: 5-hour limit, weekly limit and usage credits, each with a percent and
  a reset countdown (`44% 3h`). Bars go amber at 70% and red at 90%.
- **Burn-rate forecast** — the agent tracks how fast the 5-hour limit is
  filling; if you're on track to hit the cap *before* it resets, the badge
  shows a red `ETA 1.4h` instead of the reset time.
- **Session list page** — flip pages with the D-pad (left/right) to see every
  session: project name, phase (`P` waiting for permission, `I` waiting for
  input, `W` working), minutes in that phase, and context size (`412k`).
- **Jump to a chat** — move the cursor with the D-pad (up/down) and press
  **A**; the agent foregrounds that session's window on the Mac (terminal or
  Claude Desktop). On the dashboard, **A** jumps to the alerting session.
- **Work-in-progress spinner** — a small animated spinner in the header spins
  whenever at least one session is actually working (model generating or a tool
  running), so "busy" is distinguishable from "idle but connected" at a glance.
- **Notifications** — a two-tone chirp on a new "waiting" event, then a single
  reminder beep every ~2 minutes while a session stays blocked. The NeoPixel
  "eyes" encode *what* is wanted by their pattern and *how urgent* by colour:

  | State | Eyes |
  |---|---|
  | Needs permission (blocks the session) | alternate red, left/right — never stops |
  | New wait, first ~8s | both blink amber together |
  | Claude is working | alternate dim cyan, left/right |
  | Someone is still waiting | steady amber |
  | All quiet (no live sessions) | steady green |
  | No link to the agent | steady blue |

  Alternating reads differently from a synchronised blink even in peripheral
  vision, so "something is happening" never looks like "you are needed". The
  amber blink lasts only ~8 seconds — a permanently blinking badge is
  exhausting — and after it, work-in-progress outranks a stale wait, so one
  session parked in "waiting" for hours cannot mask every other state.

  Run `make demo-eyes` to cycle all of these on the badge and compare them
  side by side (useful because a permission prompt never happens if you run
  Claude Code with `--dangerously-skip-permissions`).
- **Do not disturb** — lay the badge flat on the desk (screen up) and it
  sleeps: screen and eyes off, alerts muted, with a confirmation chirp. Pick
  it up and it wakes. Worn on a lanyard it never triggers.
- **Sound toggle** — button **B** mutes/unmutes all sound; a crossed-out
  speaker shows in the header. The setting is saved to flash and survives
  reboots.
- **Standby** — after 5 minutes with no activity the screen and eyes turn off;
  any event, button, or picking the badge up wakes it.
- **Self-recovery** — a hardware watchdog resets the badge if the firmware
  ever stalls, so it can't get stuck on a frozen screen.

## Hardware

Gopher Badge = RP2040 (264 KB RAM, 8 MB flash), ST7789 320×240 IPS display,
two WS2812 NeoPixels, a piezo buzzer, an LIS3DH accelerometer, and A/B +
D-pad buttons. No WiFi — USB is the only data path.

## Repository layout

```
firmware/    badge firmware (TinyGo, target gopher-badge)
cmd/agent/   host agent (Go), runs on the Mac
internal/    agent logic — domain / usecase / infra (clean architecture)
scripts/     Claude Code hook + launchd service install scripts
```

## Requirements

- macOS (the agent reads the Claude Code data dir and macOS Keychain, and
  focuses windows via `open`).
- [TinyGo](https://tinygo.org) ≥ 0.41 — `brew install tinygo-org/tools/tinygo`
  (upgrade later with `make tinygo-update`).
- Go ≥ 1.24.
- `jq` — only for `make install-hooks`.
- `picotool` — optional, only used as a flashing fallback (`brew install picotool`).

## Quick start

```sh
# 1. Flash the badge (plug it in via USB-C)
make flash

# 2. (Recommended) install hooks for precise "waiting for permission" signals
make install-hooks

# 3. Run the agent — either in the foreground...
make run

# ...or as a launchd service (autostart at login, restart on crash)
make install-agent
```

Live numbers appear within a couple of seconds. The service copies its binary
to `~/.claude-badge/` (so it doesn't depend on this checkout) and logs to
`~/.claude-badge/agent.log`. After changing agent code, just re-run
`make install-agent` — it rebuilds and restarts.

No badge handy? `make dry-run` prints the protocol frames to the log.

## Controls

| Input                     | Action                                                        |
|---------------------------|---------------------------------------------------------------|
| **Button A**              | Open a chat on the Mac (dashboard → alerting one; list → selected row) |
| **Button B**              | Toggle all sound on/off (persisted to flash)                  |
| **D-pad ← / →**           | Switch page (dashboard ↔ session list)                        |
| **D-pad ↑ / ↓**           | Move the cursor on the session list                           |
| **Lay flat (screen up)**  | Do-not-disturb: sleep + mute                                  |

## How it works

```
~/.claude/sessions/<pid>.json      live sessions (pid checked with kill -0)
~/.claude/projects/**/*.jsonl      usage: assistant records → message.usage
~/.claude-badge/events.jsonl       hook events (Stop, Notification, ...)
api.anthropic.com/api/oauth/usage  plan limits (5h / weekly / credits)
                 │
                 ▼
        agent (every 2s) ──USB CDC──▶ badge
```

- **Active chats** come from the live-session registry `~/.claude/sessions`,
  not from `ps` (Electron helpers and MCP wrappers pollute it) and not from
  transcript mtime (it updates on idle sessions too).
- **Today's usage** is aggregated from the transcript files incrementally
  (per-file offsets). One API response is written to the transcript as several
  records, each carrying a full copy of `usage`, so records are deduplicated by
  `message.id` — without that the totals are inflated roughly threefold.
- **"Waiting for action"** is a hybrid: hooks give an exact signal
  (`Notification: permission_prompt / idle_prompt`, `Stop`, `UserPromptSubmit`,
  `PostToolUse`). For sessions started before the hooks were installed, a
  transcript-tail heuristic is used (`stop_reason=end_turn` ⇒ waiting for
  input). Without hooks, "waiting for permission" is indistinguishable from
  "a tool is running" — so hooks are recommended.
- **Plan limits** come from the same endpoint Claude Desktop's *Plan usage*
  panel uses (`/api/oauth/usage`). The OAuth token is read from the macOS
  Keychain item `Claude Code-credentials` via the `security` CLI (Claude Code
  keeps it fresh). Polled every 60s; on failure the last known value is served,
  and `--` until the first success.

`make install-hooks` edits `~/.claude/settings.json` idempotently and keeps a
backup next to it (`settings.json.bak-badge-*`); undo with
`make uninstall-hooks`. Hooks only take effect for sessions started afterwards.

## Wire protocol (host → badge)

One line per frame, fields separated by `|`:

```
CC3|<chats>|<wait>|<5h_pct>|<5h_reset>|<5h_eta>|<wk_pct>|<wk_reset>|<cred_pct>|<cred_text>|<tok_in>|<tok_out>|<msg>|<sessions>\n
```

Percentages are `0..100`, or `-1` when unknown. Reset and ETA columns are
host-formatted durations (`3h`, `45m`, `2d`) because the badge has no clock.
`<sessions>` is up to 8 rows of `name~phase~minutes~ctx` joined by `;` (phase
is `P` / `I` / `W`). Text fields are printable ASCII only — the badge fonts
are 7-bit.

The badge echoes `ok chats=N wait=M` per frame (logged at debug level); if no
frame arrives for 10 seconds it shows `NO LINK`.

**Back-channel (badge → host):** button A sends `CMD focus` (dashboard) or
`CMD focus <row>` (session list). The agent walks the session pid's process
tree to the outermost `.app` ancestor and `open`s it — no TCC prompt, so it
works from the background service.

The encoder (`internal/infra/badge/protocol.go`) and the parser
(`firmware/protocol.go`) implement the same format; change them together and
bump the `CC3` prefix on incompatible changes so a stale-firmware badge shows
`NO LINK` instead of garbage.

## Make targets

| Target                 | What it does                                       |
|------------------------|----------------------------------------------------|
| `make flash`           | Flash the badge                                    |
| `make flash-monitor`   | Flash and open the serial monitor                  |
| `make firmware`        | Just build the UF2 into `build/`                   |
| `make monitor`         | Serial monitor (close before running the agent)    |
| `make run`             | Build and run the host agent                       |
| `make dry-run`         | Agent without the badge, frames to the log         |
| `make demo-eyes`       | Cycle synthetic states to compare eye patterns      |
| `make test` / `vet`    | Agent unit tests / static analysis                 |
| `make install-hooks`   | Install Claude Code hooks                          |
| `make uninstall-hooks` | Remove the hooks                                    |
| `make install-agent`   | Agent as a launchd service (autostart at login)    |
| `make uninstall-agent` | Stop and remove the service                        |
| `make tinygo-update`   | Update TinyGo via brew                             |
| `make clean`           | Remove `build/` and `bin/`                         |

Agent flags: `-port /dev/cu.usbmodemXXX` (default `auto`), `-interval 2s`,
`-dry-run`, `-debug`, `-claude-dir`, `-events`.

## Troubleshooting

- **`tinygo flash` can't find the port.** Something else is holding it — close
  `tinygo monitor` / stop the agent. If the firmware hung and USB won't
  enumerate, hold **BOOTSEL** on the back while plugging in USB-C; an
  `RPI-RP2` volume appears and `make flash` works (or just copy
  `build/claudecontrol.uf2` onto that volume).
- **`unable to locate any volume: [RPI-RP2]`** — the board is in the
  bootloader but macOS didn't mount the volume. `make flash` automatically
  falls back to `picotool` (PICOBOOT, no volume needed); install it with
  `brew install picotool`.
- **Limit bars show `--`.** Usually after some idle time: the Keychain OAuth
  token expired (the agent gets a 401) until you open Claude Code, which
  refreshes it — the bars come back within a minute or two. Or the API
  returned 429, in which case the agent backs off for 5 minutes and retries.
- **Multiple `/dev/cu.usbmodem*`.** The agent picks the first alphabetically
  and logs a warning; pass one explicitly with `-port`.
- **Agent and monitor at once.** The serial port is exclusive — run either
  `make monitor` or the agent, not both.
- **`WAIT` never shows "waiting for permission".** Install hooks
  (`make install-hooks`) and restart your Claude Code sessions — without hooks
  a permission dialog is indistinguishable from a running tool.
- **Badge stuck on `NO LINK`.** Unplug and replug it; the watchdog also
  self-resets a hung badge within a few seconds.

## License

[MIT](LICENSE)
