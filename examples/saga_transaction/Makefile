ROOT_DIR := $(abspath ../..)

.PHONY: default run test help

default: run

# Run distributed saga transaction demo
run:
	@cd $(ROOT_DIR) && go run ./examples/saga_transaction

# Run unit tests
test:
	@cd $(ROOT_DIR) && go test -v ./examples/saga_transaction

help:
	@echo "Available commands for Saga Transaction demo:"
	@echo "  make      - Run the saga transaction demo"
	@echo "  make test - Run test suite"
