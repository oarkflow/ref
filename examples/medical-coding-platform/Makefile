ADDR ?= :3000
ROOT_DIR := $(abspath ../..)
AEGIS := $(ROOT_DIR)/bin/aegis

SESSION_SECRET ?= super-secure-argon2id-medical-coding-session-secret-32-bytes
JWT_SECRET ?= super-secure-argon2id-medical-coding-jwt-secret-32-bytes
DB_DSN ?= file:$(ROOT_DIR)/.data/clear/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)

.PHONY: default run secure package clean help

default: run

# Run directly using Aegis
run:
	@cd $(ROOT_DIR) && test -f bin/aegis || go build -o bin/aegis ./aegis/cmd/aegis
	@cd $(ROOT_DIR) && SESSION_SECRET="$(SESSION_SECRET)" JWT_SECRET="$(JWT_SECRET)" DB_DSN="$(DB_DSN)" ./bin/aegis run -addr $(ADDR) "$(CURDIR)/resources"

# Package and run completely protected in-memory via Aegis VFS (zero disk residue)
secure: package
	@cd $(ROOT_DIR) && SESSION_SECRET="$(SESSION_SECRET)" JWT_SECRET="$(JWT_SECRET)" DB_DSN="$(DB_DSN)" ./bin/aegis run -key .data/aegis/master.enc -pubkey .data/aegis/master.pub "$(CURDIR)/medical.apkg" -addr $(ADDR)

# Package into encrypted, signed Aegis container (.apkg)
package:
	@mkdir -p $(ROOT_DIR)/.data/aegis
	@test -f $(ROOT_DIR)/bin/aegis || (cd $(ROOT_DIR) && go build -o bin/aegis ./aegis/cmd/aegis)
	@test -f $(ROOT_DIR)/.data/aegis/master.key || $(AEGIS) keygen -out $(ROOT_DIR)/.data/aegis/master
	@$(AEGIS) package -dir "$(CURDIR)/resources" -key $(ROOT_DIR)/.data/aegis/master.key -enckey $(ROOT_DIR)/.data/aegis/master.enc -appid "com.clear.medical" -ver "2.0.0" -out "$(CURDIR)/medical.apkg"

clean:
	@rm -f medical.apkg

help:
	@echo "Available commands for Medical Coding Platform:"
	@echo "  make         - Run directly with Aegis"
	@echo "  make secure  - Package & run completely protected via in-memory VFS (zero disk trace)"
	@echo "  make package - Build sealed .apkg container"
	@echo "  make clean   - Clean generated package files"
