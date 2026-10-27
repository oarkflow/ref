ADDR ?= :3000
ROOT_DIR := $(abspath ../..)
AEGIS := $(ROOT_DIR)/bin/aegis
REF := $(ROOT_DIR)/bin/ref

SESSION_SECRET ?= super-secure-argon2id-medical-coding-session-secret-32-bytes
JWT_SECRET ?= super-secure-argon2id-medical-coding-jwt-secret-32-bytes
DB_DSN ?= file:$(ROOT_DIR)/.data/clear/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)

DB_FILE ?= $(ROOT_DIR)/.data/clear/app.db
# SEED=0 skips the demo users and encounters when the database is first created.
SEED ?= 1

.PHONY: default run secure package migrate clean help

default: run

# Bring the database schema up to date. Demo seed data is loaded only when the
# database is first created (seeds are not idempotent). Migrations are read from
# resources/migrations on disk, so this runs before either `run` or `secure`.
migrate:
	@mkdir -p $(dir $(DB_FILE))
	@mkdir -p $(ROOT_DIR)/bin
	@cd $(ROOT_DIR)/cmd/ref && go build -o $(REF) .
	@fresh=0; test -f "$(DB_FILE)" || fresh=1; \
	  DB_DSN="$(DB_DSN)" $(REF) migrate "$(CURDIR)" && \
	  if [ "$$fresh" = 1 ] && [ "$(SEED)" = 1 ]; then DB_DSN="$(DB_DSN)" $(REF) seed "$(CURDIR)"; fi

# Run the configuration-only application through the generic REF runner.
run: migrate
	@SESSION_SECRET="$(SESSION_SECRET)" JWT_SECRET="$(JWT_SECRET)" DB_DSN="$(DB_DSN)" $(REF) -addr $(ADDR) "$(CURDIR)"

# Package and run completely protected in-memory via Aegis VFS (zero disk residue)
secure: package migrate
	@cd $(ROOT_DIR) && export SESSION_SECRET="$(SESSION_SECRET)" JWT_SECRET="$(JWT_SECRET)" DB_DSN="$(DB_DSN)" && cat .data/aegis/master.enc | ./bin/aegis run -key - -pubkey .data/aegis/master.pub "$(CURDIR)/medical.apkg" -addr $(ADDR)

# Package into encrypted, signed Aegis container (.apkg)
package:
	@mkdir -p $(ROOT_DIR)/.data/aegis
	@(cd $(ROOT_DIR) && go build -o bin/aegis ./aegis/cmd/aegis)
	@test -f $(ROOT_DIR)/.data/aegis/master.key || $(AEGIS) keygen -out $(ROOT_DIR)/.data/aegis/master
	@$(AEGIS) package -dir "$(CURDIR)/resources" -key $(ROOT_DIR)/.data/aegis/master.key -enckey $(ROOT_DIR)/.data/aegis/master.enc -appid "com.clear.medical" -ver "2.0.0" -out "$(CURDIR)/medical.apkg"

clean:
	@rm -f medical.apkg

help:
	@echo "Available commands for Medical Coding Platform:"
	@echo "  make         - Run with the generic REF runner"
	@echo "  make secure  - Package & run completely protected via in-memory VFS (zero disk trace)"
	@echo "  make package - Build sealed .apkg container"
	@echo "  make migrate - Apply schema migrations (seeds demo data on a new database; SEED=0 to skip)"
	@echo "  make clean   - Clean generated package files"
