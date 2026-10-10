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

.PHONY: run build test tidy wasm wasm-docker wasm-trusted

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
