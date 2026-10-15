ROOT_DIR := $(abspath ../..)

.PHONY: default run test help

default: run

# Run event sourcing & CQRS demo
run:
	@cd $(ROOT_DIR) && go run ./examples/event_sourcing

# Run unit tests
test:
	@cd $(ROOT_DIR) && go test -v ./examples/event_sourcing

help:
	@echo "Available commands for Event Sourcing demo:"
	@echo "  make      - Run the event sourcing demo"
	@echo "  make test - Run test suite"
