# Run any REF application directory.
#
#   make run dir=./examples/smsgateway
#   make run dir=./examples/boilerplate addr=:9000
#   make build          builds bin/ref
#   make test           tests the core, the messaging module and cmd/ref
#
# `make run --dir=...` cannot work: GNU make reads --dir as its own --directory
# option and changes directory before it looks for this file. Use dir=.

dir  ?= $(DIR)
addr ?=

.PHONY: run build test tidy wasm wasm-docker wasm-trusted aegis aegis-run release release-obfuscated verify-release

aegis:
	@mkdir -p bin
	go build -o bin/aegis ./aegis/cmd/aegis

# Run any REF example inside the Aegis Secure Platform:
#   make aegis-run dir=./examples/boilerplate
#   make aegis-run dir=./examples/rules addr=:9000
aegis-run: aegis
	@test -n "$(dir)" || { echo "usage: make aegis-run dir=./examples/boilerplate [addr=:8080]"; exit 2; }
	./bin/aegis run $(if $(addr),-addr $(addr)) "$(abspath $(dir))"

# Package any REF application into an encrypted, signed Aegis container:
#   make aegis-package dir=./examples/medical-coding-platform/resources out=medical.apkg
aegis-package: aegis
	@test -n "$(dir)" || { echo "usage: make aegis-package dir=<app-dir> [out=app.apkg]"; exit 2; }
	@mkdir -p .data/aegis
	@test -f .data/aegis/master.key || ./bin/aegis keygen -out .data/aegis/master
	./bin/aegis package -dir "$(dir)" -key .data/aegis/master.key -enckey .data/aegis/master.enc -out $(if $(out),$(out),app.apkg)

# Run an encrypted Aegis container completely in memory via VFS (zero disk residuals):
#   make aegis-run-pkg pkg=medical.apkg [addr=:3000]
aegis-run-pkg: aegis
	@test -n "$(pkg)" || { echo "usage: make aegis-run-pkg pkg=app.apkg [addr=:8080]"; exit 2; }
	./bin/aegis run -key .data/aegis/master.enc -pubkey .data/aegis/master.pub "$(pkg)" $(if $(addr),-addr $(addr))

# Release builds: -trimpath and a stripped symbol table (-s -w, no build ID)
# remove every source path on this machine and every function/method name
# from the symbol table `go tool nm` reads. They do NOT remove Go's own
# runtime function-name strings (kept for panics and stack traces) — a
# plain release binary still contains "github.com/oarkflow/..." as readable
# text; `scripts/verify-build.sh` catches this, `make release-obfuscated`
# (below) is what actually removes it.
#
#   make release                 # bin/release/{aegis,ref}-<os>-<arch>[.exe]
#   make verify-release          # checks every binary release just built
RELEASE_DIR    := bin/release
RELEASE_LDFLAGS := -s -w -buildid=
RELEASE_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

release:
	@mkdir -p $(RELEASE_DIR)
	@for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "==> aegis $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(RELEASE_LDFLAGS)" -o $(RELEASE_DIR)/aegis-$$os-$$arch$$ext ./aegis/cmd/aegis || exit 1; \
		echo "==> ref $$os/$$arch"; \
		(cd cmd/ref && GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(RELEASE_LDFLAGS)" -o ../../$(RELEASE_DIR)/ref-$$os-$$arch$$ext .) || exit 1; \
	done
	@echo "built $$(ls $(RELEASE_DIR) | wc -l | tr -d ' ') binaries in $(RELEASE_DIR)"

# Same, but also obfuscates with garble: package paths, identifiers and
# string literals are renamed/encrypted so they do not appear in the binary
# even as runtime bookkeeping (verified: a plain release binary had ~9,700
# occurrences of "github.com/oarkflow/" as text; the garbled one had 0).
# This has a real, measured cost — on this machine, building ./aegis/cmd/aegis
# alone went from 16s to 71s (4.3x) and 31MB to 104MB (3.4x); no runtime
# startup-time difference was measurable. It is a one-time release-build
# cost, not a per-request one, but it is not free, hence a separate target
# rather than the default. See docs/aegis-protection-plan.md, phase 2.
#
# Needs `go install mvdan.cc/garble@latest` and a garble release that
# supports your Go toolchain version (garble trails new Go releases; check
# `garble version` against `go version` before relying on this).
release-obfuscated:
	@command -v garble >/dev/null || { echo "garble is required: go install mvdan.cc/garble@latest"; exit 1; }
	@mkdir -p $(RELEASE_DIR)
	@for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "==> aegis $$os/$$arch (garbled)"; \
		GOOS=$$os GOARCH=$$arch garble -literals -tiny build -trimpath -ldflags="$(RELEASE_LDFLAGS)" -o $(RELEASE_DIR)/aegis-$$os-$$arch$$ext ./aegis/cmd/aegis || exit 1; \
		echo "==> ref $$os/$$arch (garbled)"; \
		(cd cmd/ref && GOOS=$$os GOARCH=$$arch garble -literals -tiny build -trimpath -ldflags="$(RELEASE_LDFLAGS)" -o ../../$(RELEASE_DIR)/ref-$$os-$$arch$$ext .) || exit 1; \
	done
	@echo "built $$(ls $(RELEASE_DIR) | wc -l | tr -d ' ') obfuscated binaries in $(RELEASE_DIR)"

# Verifies every binary in bin/release against scripts/verify-build.sh. Pass
# allow_paths=1 after a plain `make release` (expected to still carry
# package-path strings); omit it after `make release-obfuscated`.
verify-release:
	@test -d $(RELEASE_DIR) || { echo "nothing in $(RELEASE_DIR); run make release or make release-obfuscated first"; exit 2; }
	@failed=0; for f in $(RELEASE_DIR)/*; do \
		echo "--- $$f"; \
		scripts/verify-build.sh $(if $(allow_paths),--allow-package-paths) "$$f" || failed=1; \
	done; exit $$failed

run:
	@test -n "$(dir)" || { echo "usage: make run dir=./examples/smsgateway [addr=:8080]"; exit 2; }
	cd cmd/ref && go run . $(if $(addr),-addr $(addr)) "$(abspath $(dir))"

build:
	cd cmd/ref && go build -o ../../bin/ref .

test:
	go test ./platform ./serve ./etl
	cd contrib/messaging && go test ./...
	cd cmd/ref && go vet ./...

tidy:
	cd cmd/ref && go mod tidy

# The console's secure fetch is github.com/oarkflow/fh's WebAssembly transport
# (wasm/ and mw/securetransport in that module). This builds its client with
# TinyGo and installs it, with the module's own loader and integrity manifest,
# into the data-pipeline example. TinyGo needs Go 1.26 or older; point GO126 at
# one (go install golang.org/dl/go1.26.5@latest && go1.26.5 download).
#
# Without trust variables the client is loopback-only (development). For a real
# deployment pass all five, printed by the server's key tooling:
#   make wasm WASM_TRUSTED_ORIGIN=https://app.example.com \
#     WASM_TRUSTED_TRANSPORT_KEY=... WASM_TRUSTED_TRANSPORT_KEY_ID=... \
#     WASM_TRUSTED_RESPONSE_KEY=... WASM_TRUSTED_RESPONSE_KEY_ID=...
WASM_APP := examples/data-pipeline
GO126 ?= $(HOME)/sdk/go1.26.5/bin
WASM_LDFLAGS :=
ifneq ($(strip $(WASM_TRUSTED_ORIGIN)),)
WASM_LDFLAGS := -X main.embeddedTrustedOrigin=$(WASM_TRUSTED_ORIGIN) -X main.embeddedTransportPublicKey=$(WASM_TRUSTED_TRANSPORT_KEY) -X main.embeddedTransportKeyID=$(WASM_TRUSTED_TRANSPORT_KEY_ID) -X main.embeddedResponseSigningPublicKey=$(WASM_TRUSTED_RESPONSE_KEY) -X main.embeddedResponseSigningKeyID=$(WASM_TRUSTED_RESPONSE_KEY_ID)
endif
wasm:
	@command -v tinygo >/dev/null || { echo "tinygo is required (brew install tinygo binaryen)"; exit 1; }
	@tmp=$$(mktemp -d) && fh="$$(go list -m -f '{{.Dir}}' github.com/oarkflow/fh)" && \
	  cp -R "$$fh/." "$$tmp" && chmod -R u+w "$$tmp" && \
	  (cd "$$tmp" && PATH="$(GO126):$$PATH" tinygo build -target wasm -no-debug -ldflags="$(WASM_LDFLAGS)" -o wasm/dist/securefetch.wasm ./wasm/cmd/securefetch && \
	   cp wasm/cmd/securefetch/wasm_exec.js wasm/dist/wasm_exec.js && go run ./wasm/cmd/manifest -dir wasm/dist) && \
	  rm -rf $(WASM_APP)/static/wasm && mkdir -p $(WASM_APP)/static/wasm && \
	  cp "$$tmp"/wasm/dist/*.js "$$tmp"/wasm/dist/*.wasm "$$tmp"/wasm/dist/asset-manifest.json "$$tmp"/wasm/dist/SHA256SUMS $(WASM_APP)/static/wasm/ && \
	  rm -rf "$$tmp" && echo "installed $$(wc -c < $(WASM_APP)/static/wasm/securefetch.wasm) bytes of securefetch.wasm into $(WASM_APP)/static/wasm" && cat $(WASM_APP)/static/wasm/SHA256SUMS

# Same build without installing TinyGo or Go 1.26 locally: runs it in the
# official TinyGo image (needs Docker). The committed client in
# $(WASM_APP)/static/wasm is already built, so this is only needed to rebuild it
# (for example to embed trust keys).
TINYGO_IMAGE ?= tinygo/tinygo:0.40.1
wasm-docker:
	@command -v docker >/dev/null || { echo "docker is required"; exit 1; }
	@fh="$$(go list -m -f '{{.Dir}}' github.com/oarkflow/fh)" && tmp=$$(mktemp -d) && \
	  cp -R "$$fh/." "$$tmp" && chmod -R u+w "$$tmp" && \
	  docker run --rm -v "$$tmp":/src -w /src $(TINYGO_IMAGE) tinygo build -target wasm -no-debug -ldflags="$(WASM_LDFLAGS)" -o wasm/dist/securefetch.wasm ./wasm/cmd/securefetch && \
	  cp "$$tmp"/wasm/cmd/securefetch/wasm_exec.js "$$tmp"/wasm/dist/wasm_exec.js && (cd "$$tmp" && go run ./wasm/cmd/manifest -dir wasm/dist) && \
	  rm -rf $(WASM_APP)/static/wasm && mkdir -p $(WASM_APP)/static/wasm && \
	  cp "$$tmp"/wasm/dist/*.js "$$tmp"/wasm/dist/*.wasm "$$tmp"/wasm/dist/asset-manifest.json "$$tmp"/wasm/dist/SHA256SUMS $(WASM_APP)/static/wasm/ && \
	  rm -rf "$$tmp" && cat $(WASM_APP)/static/wasm/SHA256SUMS

# Builds the client with the origin and the server's public keys built in (read
# from its key files), which is what a deployment outside loopback requires.
#   make wasm-trusted ORIGIN=https://app.example.com [KEYS=/run/secrets]
wasm-trusted:
	@test -n "$(ORIGIN)" || { echo "usage: make wasm-trusted ORIGIN=https://app.example.com [KEYS=dir with transport.key and signing.key]"; exit 2; }
	$(MAKE) wasm $$(go run ./contrib/securetrust -origin "$(ORIGIN)" $(if $(KEYS),-keys "$(KEYS)",))
