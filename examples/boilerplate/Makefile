ADDR ?= :9000
ROOT_DIR := $(abspath ../..)
AEGIS := $(ROOT_DIR)/bin/aegis

.PHONY: default run secure package clean help

default: run

# Run boilerplate directly with Aegis
run:
	@cd $(ROOT_DIR) && test -f bin/aegis || go build -o bin/aegis ./aegis/cmd/aegis
	@cd $(ROOT_DIR) && ./bin/aegis run -addr $(ADDR) "$(CURDIR)"

# Package and run completely protected in-memory via Aegis VFS (zero disk residue)
secure: package
	@cd $(ROOT_DIR) && ./bin/aegis run -key .data/aegis/master.enc -pubkey .data/aegis/master.pub "$(CURDIR)/boilerplate.apkg" -addr $(ADDR)

# Package into encrypted, signed Aegis container (.apkg)
package:
	@mkdir -p $(ROOT_DIR)/.data/aegis
	@test -f $(ROOT_DIR)/bin/aegis || (cd $(ROOT_DIR) && go build -o bin/aegis ./aegis/cmd/aegis)
	@test -f $(ROOT_DIR)/.data/aegis/master.key || $(AEGIS) keygen -out $(ROOT_DIR)/.data/aegis/master
	@$(AEGIS) package -dir "$(CURDIR)" -key $(ROOT_DIR)/.data/aegis/master.key -enckey $(ROOT_DIR)/.data/aegis/master.enc -appid "com.ref.boilerplate" -ver "1.0.0" -out "$(CURDIR)/boilerplate.apkg"

clean:
	@rm -f boilerplate.apkg

help:
	@echo "Available commands for Boilerplate:"
	@echo "  make         - Run directly with Aegis"
	@echo "  make secure  - Package & run completely protected via in-memory VFS (zero disk trace)"
	@echo "  make package - Build sealed .apkg container"
	@echo "  make clean   - Clean generated package files"
