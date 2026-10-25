ADDR ?= :8080
ROOT := $(abspath ../..)

# A demo: the passwords below are for trying it on localhost only.
export SESSION_SECRET ?= demo-session-secret-demo-session-secret-0123456789
export DEMO_PASSWORD  ?= demo-password
export OPS_API_KEY    ?= demo-ops-key-0123456789
# The call service and this application share a service key. rtcd refuses
# anything shorter than 24 characters.
export RTCD_API_KEY      ?= demo-rtcd-service-key-0123456789
export RTCD_TOKEN_SECRET ?= demo-rtcd-token-secret-0123456789ab
export RTCD_URL          ?= http://127.0.0.1:8480

# The call service is a separate project, github.com/oarkflow/rtcd. Point RTCD_DIR
# at a checkout of it (by default one next to this repository), or at a binary
# you built with RTCD_BIN.
#
#   git clone git@github.com:oarkflow/rtcd.git ../../../rtcd
RTCD_DIR ?= $(abspath $(ROOT)/../rtcd)
RTCD_BIN ?= $(CURDIR)/.data/rtcd

.PHONY: run rtcd app build-rtcd test-browser
# run starts the call service in the background, then the application.
run: build-rtcd
	@mkdir -p .data
	@cd $(CURDIR) && $(RTCD_BIN) -config rtcd.bcl & echo $$! > .data/rtcd.pid; \
	  trap 'kill `cat .data/rtcd.pid` 2>/dev/null' EXIT INT TERM; \
	  cd $(ROOT)/cmd/ref && go run . -addr $(ADDR) $(CURDIR)

# build-rtcd builds the call service from its checkout. (Not `go install`: its
# go.mod replaces pion/ice with a fork, and Go refuses to install such a module.)
build-rtcd:
	@mkdir -p .data
	@test -d "$(RTCD_DIR)" || { echo "rtcd is not at $(RTCD_DIR). Clone it: git clone git@github.com:oarkflow/rtcd.git $(RTCD_DIR)  (or set RTCD_DIR)"; exit 1; }
	@cd "$(RTCD_DIR)" && go build -o $(RTCD_BIN) ./cmd/rtcd

# rtcd and app run the two halves separately.
rtcd: build-rtcd
	@./.data/rtcd -config rtcd.bcl

app:
	@mkdir -p .data
	@cd $(ROOT)/cmd/ref && go run . -addr $(ADDR) $(CURDIR)

# test-browser runs the real-Chrome scenarios (see test/browser/README.md).
#   make test-browser SCENARIO=e2ee_group_call
SCENARIO ?=
test-browser: build-rtcd
	@mkdir -p .data
	@cd $(ROOT)/cmd/ref && go build -o $(CURDIR)/.data/refbin .
	@node test/browser/run.js $(SCENARIO)
