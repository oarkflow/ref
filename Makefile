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

.PHONY: run build test tidy

run:
	@test -n "$(dir)" || { echo "usage: make run dir=./examples/smsgateway [addr=:8080]"; exit 2; }
	cd cmd/ref && go run . $(if $(addr),-addr $(addr)) "$(abspath $(dir))"

build:
	cd cmd/ref && go build -o ../../bin/ref .

test:
	go test ./platform ./serve
	cd contrib/messaging && go test ./...
	cd cmd/ref && go vet ./...

tidy:
	cd cmd/ref && go mod tidy
