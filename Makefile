TARGET       := gopher-badge
CYD_TARGET   := esp32-generic
FIRMWARE_DIR := firmware
AGENT_BIN    := bin/claude-badge-agent
UF2          := build/claudecontrol.uf2
CYD_BIN      := build/claudecontrol-cyd.bin

# The display's CH340 port; override with make flash-cyd PORT=/dev/cu.usbserial-XXXX
PORT ?= $(firstword $(wildcard /dev/cu.usbserial* /dev/cu.wchusbserial*))

# TinyGo 0.42 supports the system Go (1.27); no toolchain pin needed any more.
TINYGO     := tinygo
FW_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)
FW_LDFLAGS := -X main.fwVersion=$(FW_VERSION)

# TinyGo runs ESP32 interrupt handlers on the interrupted goroutine's stack,
# and the WiFi blob's handler needs several KB on top of our deepest frames:
# the default 8 KB corrupted the scheduler on the first run, 12 KB hung.
CYD_STACK := 16KB

# The display is flashed with espflasher writing only the image region;
# `tinygo flash` erases the whole chip and with it the settings sectors.
ESPFLASHER := go run tinygo.org/x/espflasher@v0.8.1
CYD_IMAGE_OFFSET := 0x1000

AGENT_LABEL := com.claudecontrol.badge-agent
AGENT_PLIST := $(HOME)/Library/LaunchAgents/$(AGENT_LABEL).plist

.DEFAULT_GOAL := help

.PHONY: help firmware flash flash-cyd flash-monitor monitor agent run dry-run demo-eyes \
        test test-firmware vet install-hooks uninstall-hooks install-agent uninstall-agent \
        tinygo-update clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## --- Firmware (TinyGo) ---

firmware: ## Build both firmware images (badge uf2 + display bin)
	@mkdir -p build
	cd $(FIRMWARE_DIR) && $(TINYGO) build -target=$(TARGET) -ldflags "$(FW_LDFLAGS)" -o ../$(UF2) .
	cd $(FIRMWARE_DIR) && $(TINYGO) build -target=$(CYD_TARGET) -stack-size=$(CYD_STACK) -ldflags "$(FW_LDFLAGS)" -o ../$(CYD_BIN) .
	@ls -la $(UF2) $(CYD_BIN)

flash: ## Flash the Gopher Badge (pauses the agent service; picotool fallback)
	-@launchctl bootout gui/$$(id -u)/$(AGENT_LABEL) 2>/dev/null || true
	@status=0; \
	( cd $(FIRMWARE_DIR) && $(TINYGO) flash -target=$(TARGET) -ldflags "$(FW_LDFLAGS)" . || \
		{ echo "RPI-RP2 volume did not mount — trying picotool (PICOBOOT, no volume)..."; \
		  $(TINYGO) build -target=$(TARGET) -ldflags "$(FW_LDFLAGS)" -o /tmp/claudecontrol-flash.uf2 . && \
		  picotool load -x /tmp/claudecontrol-flash.uf2; } ) || status=$$?; \
	[ -f $(AGENT_PLIST) ] && launchctl bootstrap gui/$$(id -u) $(AGENT_PLIST) 2>/dev/null || true; \
	exit $$status

flash-cyd: firmware ## Flash the 3.2" ESP32 display over its CH340 port, keeping its settings (PORT=... to choose)
	@test -n "$(PORT)" || { echo "no CH340 port found (/dev/cu.usbserial*); pass PORT=..."; exit 1; }
	$(ESPFLASHER) -port $(PORT) -offset $(CYD_IMAGE_OFFSET) $(CYD_BIN)

flash-monitor: ## Flash and open the serial monitor (agent service stays off)
	-@launchctl bootout gui/$$(id -u)/$(AGENT_LABEL) 2>/dev/null || true
	cd $(FIRMWARE_DIR) && $(TINYGO) flash -target=$(TARGET) -ldflags "$(FW_LDFLAGS)" -monitor .
	@echo "Agent service stopped (the monitor needs the port). Restore it with: make install-agent"

monitor: ## Serial monitor (close it before starting the agent!)
	cd $(FIRMWARE_DIR) && $(TINYGO) monitor -target=$(TARGET)

test-firmware: ## Host tests of the hardware-free firmware packages
	cd $(FIRMWARE_DIR) && go test ./internal/...

## --- Host agent (Go) ---

agent: ## Build the host agent into bin/
	go build -o $(AGENT_BIN) ./cmd/agent

run: agent ## Build and run the host agent in the foreground
	./$(AGENT_BIN)

dry-run: agent ## Run the agent without the badge (frames printed to the log)
	./$(AGENT_BIN) -dry-run -debug

demo-eyes: agent ## Cycle synthetic states on the badge to compare eye patterns (Ctrl-C to stop)
	-@launchctl bootout gui/$$(id -u)/$(AGENT_LABEL) 2>/dev/null || true
	@echo "Watch the eyes. Ctrl-C when done; the service is restored on exit."
	-./$(AGENT_BIN) -demo
	-@[ -f $(AGENT_PLIST) ] && launchctl bootstrap gui/$$(id -u) $(AGENT_PLIST) 2>/dev/null || true

test: test-firmware ## Run the agent and firmware unit tests
	go test ./...

vet: ## Run go vet
	go vet ./...

## --- Claude Code hooks ---

install-hooks: ## Install hooks for precise "waiting" signals (needs jq)
	sh scripts/install-hooks.sh

uninstall-hooks: ## Remove the hooks from settings.json
	sh scripts/uninstall-hooks.sh

install-agent: ## Install the agent as a launchd service (autostart at login)
	sh scripts/install-agent.sh

uninstall-agent: ## Stop and remove the launchd service
	sh scripts/uninstall-agent.sh

## --- Misc ---

tinygo-update: ## Update TinyGo to the latest version (brew)
	brew update
	brew upgrade tinygo || brew install tinygo
	tinygo version

clean: ## Remove build artifacts
	rm -rf build bin
