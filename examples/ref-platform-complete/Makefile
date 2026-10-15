ADDR ?= :8090
ROOT_DIR := $(abspath ../..)
AEGIS := $(ROOT_DIR)/bin/aegis

DATABASE_URL ?= postgres://localhost:5432/webhook_demo?sslmode=disable
WEBHOOK_API_KEY ?= super-secure-argon2id-webhook-api-key-32-bytes
WEBHOOK_TARGET_URL ?= https://hooks.example.test/events
WEBHOOK_ALLOWED_HOST ?= hooks.example.test

.PHONY: default run secure package clean help

default: run

# Run directly using Aegis Platform Runtime
run:
	@cd $(ROOT_DIR) && test -f bin/aegis || go build -o bin/aegis ./aegis/cmd/aegis
	@cd $(ROOT_DIR) && DATABASE_URL="$(DATABASE_URL)" WEBHOOK_API_KEY="$(WEBHOOK_API_KEY)" WEBHOOK_TARGET_URL="$(WEBHOOK_TARGET_URL)" WEBHOOK_ALLOWED_HOST="$(WEBHOOK_ALLOWED_HOST)" ./bin/aegis run -addr $(ADDR) "$(CURDIR)"

# Package and run completely protected in-memory via Aegis VFS (zero disk residue)
secure: package
	@cd $(ROOT_DIR) && DATABASE_URL="$(DATABASE_URL)" WEBHOOK_API_KEY="$(WEBHOOK_API_KEY)" WEBHOOK_TARGET_URL="$(WEBHOOK_TARGET_URL)" WEBHOOK_ALLOWED_HOST="$(WEBHOOK_ALLOWED_HOST)" ./bin/aegis run -key .data/aegis/master.enc -pubkey .data/aegis/master.pub "$(CURDIR)/webhook-relay.apkg" -addr $(ADDR)

# Package into encrypted, signed Aegis container (.apkg)
package:
	@mkdir -p $(ROOT_DIR)/.data/aegis
	@test -f $(ROOT_DIR)/bin/aegis || (cd $(ROOT_DIR) && go build -o bin/aegis ./aegis/cmd/aegis)
	@test -f $(ROOT_DIR)/.data/aegis/master.key || $(AEGIS) keygen -out $(ROOT_DIR)/.data/aegis/master
	@$(AEGIS) package -dir "$(CURDIR)" -key $(ROOT_DIR)/.data/aegis/master.key -enckey $(ROOT_DIR)/.data/aegis/master.enc -appid "com.ref.webhook-relay" -ver "1.0.0" -out "$(CURDIR)/webhook-relay.apkg"

clean:
	@rm -f webhook-relay.apkg

help:
	@echo "Available commands for Webhook Relay Platform:"
	@echo "  make         - Run directly with Aegis"
	@echo "  make secure  - Package & run completely protected via in-memory VFS (zero disk residue)"
	@echo "  make package - Build sealed .apkg container"
	@echo "  make clean   - Clean generated package files"
