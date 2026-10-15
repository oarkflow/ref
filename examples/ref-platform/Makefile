ADDR ?= :8089
ROOT_DIR := $(abspath ../..)
AEGIS := $(ROOT_DIR)/bin/aegis

DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/orders?sslmode=disable
SESSION_SECRET ?= super-secure-argon2id-orders-session-secret-32-bytes
JWT_SECRET ?= super-secure-argon2id-orders-jwt-secret-32-bytes
PAYMENT_API_KEY ?= test-payment-key
PAYMENT_WEBHOOK_SECRET ?= super-secure-argon2id-orders-webhook-secret-32-bytes

.PHONY: default run secure package clean help

default: run

# Run directly using Aegis Platform Runtime
run:
	@cd $(ROOT_DIR) && test -f bin/aegis || go build -o bin/aegis ./aegis/cmd/aegis
	@cd $(ROOT_DIR) && DATABASE_URL="$(DATABASE_URL)" SESSION_SECRET="$(SESSION_SECRET)" JWT_SECRET="$(JWT_SECRET)" PAYMENT_API_KEY="$(PAYMENT_API_KEY)" PAYMENT_WEBHOOK_SECRET="$(PAYMENT_WEBHOOK_SECRET)" ./bin/aegis run -addr $(ADDR) "$(CURDIR)"

# Package and run completely protected in-memory via Aegis VFS (zero disk residue)
secure: package
	@cd $(ROOT_DIR) && DATABASE_URL="$(DATABASE_URL)" SESSION_SECRET="$(SESSION_SECRET)" JWT_SECRET="$(JWT_SECRET)" PAYMENT_API_KEY="$(PAYMENT_API_KEY)" PAYMENT_WEBHOOK_SECRET="$(PAYMENT_WEBHOOK_SECRET)" ./bin/aegis run -key .data/aegis/master.enc -pubkey .data/aegis/master.pub "$(CURDIR)/orders.apkg" -addr $(ADDR)

# Package into encrypted, signed Aegis container (.apkg)
package:
	@mkdir -p $(ROOT_DIR)/.data/aegis
	@test -f $(ROOT_DIR)/bin/aegis || (cd $(ROOT_DIR) && go build -o bin/aegis ./aegis/cmd/aegis)
	@test -f $(ROOT_DIR)/.data/aegis/master.key || $(AEGIS) keygen -out $(ROOT_DIR)/.data/aegis/master
	@$(AEGIS) package -dir "$(CURDIR)" -key $(ROOT_DIR)/.data/aegis/master.key -enckey $(ROOT_DIR)/.data/aegis/master.enc -appid "com.ref.orders" -ver "1.0.0" -out "$(CURDIR)/orders.apkg"

clean:
	@rm -f orders.apkg

help:
	@echo "Available commands for Orders Platform:"
	@echo "  make         - Run directly with Aegis"
	@echo "  make secure  - Package & run completely protected via in-memory VFS (zero disk residue)"
	@echo "  make package - Build sealed .apkg container"
	@echo "  make clean   - Clean generated package files"
