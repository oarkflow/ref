ADDR ?= :8080
ROOT := $(abspath ../..)

# A demo: the passwords below are for trying it on localhost only.
export SESSION_SECRET ?= demo-session-secret-demo-session-secret-0123456789
export DEMO_PASSWORD  ?= demo-password
export OPS_API_KEY    ?= demo-ops-key-0123456789

.PHONY: run
run:
	@mkdir -p .data
	@cd $(ROOT)/cmd/ref && go run . -addr $(ADDR) $(CURDIR)
