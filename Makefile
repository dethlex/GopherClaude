TARGET       := gopher-badge
FIRMWARE_DIR := firmware
AGENT_BIN    := bin/claude-badge-agent
UF2          := build/claudecontrol.uf2

AGENT_LABEL := com.claudecontrol.badge-agent
AGENT_PLIST := $(HOME)/Library/LaunchAgents/$(AGENT_LABEL).plist

.DEFAULT_GOAL := help
# TinyGo lags the Go release cycle (0.41 supports Go 1.19–1.26); pin the Go
# toolchain it sees so a brew-upgraded Go does not break the firmware build.
TINYGO_GO := go1.26.0
TINYGO    := GOTOOLCHAIN=$(TINYGO_GO) tinygo

.PHONY: help firmware flash flash-monitor monitor agent run dry-run demo-eyes \
        test vet install-hooks uninstall-hooks install-agent uninstall-agent \
        tinygo-update clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## --- Firmware (TinyGo) ---

firmware: ## Build the firmware into build/claudecontrol.uf2
	@mkdir -p build
	cd $(FIRMWARE_DIR) && $(TINYGO) build -target=$(TARGET) -o ../$(UF2) .
	@ls -la $(UF2)

flash: ## Flash the Gopher Badge (pauses the agent service; picotool fallback)
	-@launchctl bootout gui/$$(id -u)/$(AGENT_LABEL) 2>/dev/null || true
	cd $(FIRMWARE_DIR) && $(TINYGO) flash -target=$(TARGET) . || \
		{ echo "RPI-RP2 volume did not mount — trying picotool (PICOBOOT, no volume)..."; \
		  $(TINYGO) build -target=$(TARGET) -o /tmp/claudecontrol-flash.uf2 . && \
		  picotool load -x /tmp/claudecontrol-flash.uf2; }
	-@[ -f $(AGENT_PLIST) ] && launchctl bootstrap gui/$$(id -u) $(AGENT_PLIST) 2>/dev/null || true

flash-monitor: ## Flash and open the serial monitor (agent service stays off)
	-@launchctl bootout gui/$$(id -u)/$(AGENT_LABEL) 2>/dev/null || true
	cd $(FIRMWARE_DIR) && $(TINYGO) flash -target=$(TARGET) -monitor .
	@echo "Agent service stopped (the monitor needs the port). Restore it with: make install-agent"

monitor: ## Serial monitor (close it before starting the agent!)
	cd $(FIRMWARE_DIR) && $(TINYGO) monitor -target=$(TARGET)

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

test: ## Run the agent unit tests
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
