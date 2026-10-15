ADDR ?= :8096
ROOT_DIR := $(abspath ../..)
AEGIS := $(ROOT_DIR)/bin/aegis

CODING_JWT_SECRET ?= super-secure-argon2id-medical-coding-jwt-secret-32-bytes

.PHONY: default run secure package clean help

default: run

# Run directly using Aegis Platform Runtime
run:
	@cd $(ROOT_DIR) && test -f bin/aegis || go build -o bin/aegis ./aegis/cmd/aegis
	@cd $(ROOT_DIR) && CODING_JWT_SECRET="$(CODING_JWT_SECRET)" ./bin/aegis run -addr $(ADDR) "$(CURDIR)"

# Package and run completely protected in-memory via Aegis VFS (zero disk residue)
secure: package
	@cd $(ROOT_DIR) && CODING_JWT_SECRET="$(CODING_JWT_SECRET)" ./bin/aegis run -key .data/aegis/master.enc -pubkey .data/aegis/master.pub "$(CURDIR)/medical-coding.apkg" -addr $(ADDR)

# Package into encrypted, signed Aegis container (.apkg)
package:
	@mkdir -p $(ROOT_DIR)/.data/aegis
	@test -f $(ROOT_DIR)/bin/aegis || (cd $(ROOT_DIR) && go build -o bin/aegis ./aegis/cmd/aegis)
	@test -f $(ROOT_DIR)/.data/aegis/master.key || $(AEGIS) keygen -out $(ROOT_DIR)/.data/aegis/master
	@$(AEGIS) package -dir "$(CURDIR)" -key $(ROOT_DIR)/.data/aegis/master.key -enckey $(ROOT_DIR)/.data/aegis/master.enc -appid "com.ref.medical-coding" -ver "1.0.0" -out "$(CURDIR)/medical-coding.apkg"

clean:
	@rm -f medical-coding.apkg

help:
	@echo "Available commands for Medical Coding:"
	@echo "  make         - Run directly with Aegis"
	@echo "  make secure  - Package & run completely protected via in-memory VFS (zero disk residue)"
	@echo "  make package - Build sealed .apkg container"
	@echo "  make clean   - Clean generated package files"
