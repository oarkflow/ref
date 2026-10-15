ROOT_DIR := $(abspath ../..)

.PHONY: default run test help

default: run

# Run datasource optimization showcase
run:
	@cd $(ROOT_DIR) && go run ./examples/datasource

# Run unit tests
test:
	@cd $(ROOT_DIR) && go test -v ./examples/datasource

help:
	@echo "Available commands for DataSource showcase:"
	@echo "  make      - Run the showcase scenarios"
	@echo "  make test - Run test suite"
