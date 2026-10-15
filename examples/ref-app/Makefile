ROOT_DIR := $(abspath ../..)

DATABASE_URL ?= postgres://localhost:5432/refapp?sslmode=disable
JWT_SECRET ?= super-secure-argon2id-refapp-jwt-secret-32-bytes
REF_APP_ADDR ?= :8090

.PHONY: default run test help

default: run

# Run ref-app directly
run:
	@cd $(ROOT_DIR) && DATABASE_URL="$(DATABASE_URL)" JWT_SECRET="$(JWT_SECRET)" REF_APP_ADDR="$(REF_APP_ADDR)" go run ./examples/ref-app

# Run unit and integration tests
test:
	@cd $(ROOT_DIR) && go test -v ./examples/ref-app

help:
	@echo "Available commands for ref-app:"
	@echo "  make      - Run the ref-app HTTP server"
	@echo "  make test - Run test suite"
