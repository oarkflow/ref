#!/usr/bin/env bash
# Checks the whole example: static checks, the Go tests, the application served by
# the real runner (every endpoint, including the refusals), and four deliberately
# broken copies that must each refuse to start.
#
#   ./check.sh            from anywhere
#   KEEP_LOG=1 ./check.sh keep the server log on success too
set -u
cd "$(dirname "$0")"
EXAMPLE=$PWD
ROOT=$(cd ../.. && pwd)
AEGIS=$ROOT/bin/aegis
WORK=$(mktemp -d)
LOG=$WORK/server.log
PID=""
pass=0 fail=0

CLERK=dev-clerk-key-change-me-0123456789
VIEWER=dev-viewer-key-change-me-012345678

cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null && wait "$PID" 2>/dev/null
  pkill -f "aegis run.*$WORK" 2>/dev/null
  if [ "$fail" -eq 0 ] && [ -z "${KEEP_LOG:-}" ]; then rm -rf "$WORK"; else echo "logs and copies kept in $WORK"; fi
}
trap cleanup EXIT

ok()  { pass=$((pass + 1)); printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  \033[31mFAIL\033[0m %s\n         %s\n' "$1" "$2"; }
step() { printf '\n== %s\n' "$1"; }

# expect NAME WANT_CODE WANT_BODY_SUBSTRING METHOD PATH [KEY] [BODY]
expect() {
  local name=$1 want=$2 sub=$3 method=$4 path=$5 key=${6:-} body=${7:-}
  local args=(-s -m 10 -o "$WORK/body" -w '%{http_code}' -X "$method" "http://127.0.0.1:$PORT$path")
  [ -n "$key" ] && args+=(-H "X-API-Key: $key")
  [ -n "$body" ] && args+=(-H 'content-type: application/json' -d "$body")
  local code; code=$(curl "${args[@]}")
  if [ "$code" != "$want" ]; then bad "$name" "status $code, want $want: $(head -c 200 "$WORK/body")"
  elif [ -n "$sub" ] && ! grep -qF -- "$sub" "$WORK/body"; then bad "$name" "body lacks $sub: $(head -c 200 "$WORK/body")"
  else ok "$name"; fi
}

# expect_exact: as expect, with the whole body (a bare value) compared.
expect_exact() {
  expect "$1" "$2" "" "$4" "$5" "${6:-}" "${7:-}"
  local got; got=$(tr -d '[:space:]' <"$WORK/body")
  [ "$got" = "$3" ] || bad "$1 (body)" "got $got, want $3"
}

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'; }

wait_ready() { # PORT PID
  for _ in $(seq 1 100); do
    kill -0 "$2" 2>/dev/null || return 1
    [ "$(curl -s -m 2 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$1/live")" = 200 ] && return 0
    sleep 0.3
  done
  return 1
}

step "static checks"
if [ -n "$(gofmt -l . )" ]; then bad "gofmt" "$(gofmt -l .)"; else ok "gofmt"; fi
if (cd "$ROOT" && go vet ./platform ./examples/dynamic-definitions) >"$WORK/vet.log" 2>&1; then ok "go vet"; else bad "go vet" "$(head -5 "$WORK/vet.log")"; fi
for f in scripts/*.spl; do
  if grep -q '\\"' "$f"; then bad "$f" "contains an escaped JSON string"; else ok "$f is plain SPL"; fi
done
if grep -rnE '(source|expr)[[:space:]]+"\{' config; then bad "config" "an inline JSON string is used as a definition body"; else ok "no JSON strings in config"; fi

step "go tests"
if (cd "$ROOT" && go test ./platform ./examples/dynamic-definitions) >"$WORK/test.log" 2>&1; then ok "platform and example tests"; else bad "go test" "$(grep -E '^(---|FAIL|panic)|_test.go' "$WORK/test.log" | head -8)"; fi

step "build the runner"
# bin/aegis is not rebuilt by the Makefile, so a stale one would run old code.
if (cd "$ROOT" && go build -o bin/aegis ./aegis/cmd/aegis) >"$WORK/build.log" 2>&1; then ok "bin/aegis built from the current source"; else bad "build" "$(head -5 "$WORK/build.log")"; fi

step "serve the application"
PORT=$(free_port)
"$AEGIS" run -addr ":$PORT" "$EXAMPLE" >"$LOG" 2>&1 &
PID=$!
started=0
if wait_ready "$PORT" "$PID"; then started=1; ok "started on :$PORT"; else bad "start" "$(grep -v '^\s*$' "$LOG" | tail -5 | cut -c1-240)"; fi

if [ "$started" -eq 1 ]; then
  step "open handler"
  expect "GET /live is open" 200 '"ok":true' GET /live

  step "authentication and authorization"
  QUOTE='{"lines":[{"qty":2,"price":30},{"qty":1,"price":55}]}'
  expect "quote without a key is 401"        401 ""             POST /quote "" "$QUOTE"
  expect "quote with a bad key is 401"       401 ""             POST /quote "not-a-key" "$QUOTE"
  expect "quote as viewer is 403"            403 ""             POST /quote "$VIEWER" "$QUOTE"
  expect "shipping as viewer is 403"         403 ""             POST /shipping "$VIEWER" '{"kg":4}'
  expect "review as viewer is 403"           403 ""             POST /orders/review "$VIEWER" '{"total":800}'

  step "handlers"
  expect "quote prices the lines"            200 '"subtotal":115' POST /quote "$CLERK" "$QUOTE"
  expect "quote names the caller"            200 '"quoted_by":"clerk-1"' POST /quote "$CLERK" "$QUOTE"
  expect "quote of no lines is 0"            200 '"subtotal":0'   POST /quote "$CLERK" '{"lines":[]}'
  for pair in "0.5:5" "1:5" "4:12" "10:12" "20:25"; do
    kg=${pair%%:*} want=${pair##*:}
    expect_exact "shipping $kg kg costs $want" 200 "$want" POST /shipping "$CLERK" "{\"kg\":$kg}"
  done

  step "node type, helper node and helper() in an expression"
  expect "800 is high risk"                  200 '"level":"high"' POST /orders/review "$CLERK" '{"total":800}'
  expect "800 taxed at 13% is 104"           200 '"tax":104'      POST /orders/review "$CLERK" '{"total":800}'
  expect "helper() gives 904 with tax"       200 '"total_with_tax":904' POST /orders/review "$CLERK" '{"total":800}'
  expect "300 is low risk"                   200 '"level":"low"'  POST /orders/review "$CLERK" '{"total":300}'

  step "the server log"
  for d in node_type/risk.score helper/vat helper/shipping handler/quote handler/shipping handler/live; do
    if grep -q "definition loaded kind=${d%%/*} name=${d##*/} " "$LOG"; then ok "loaded: $d"; else bad "loaded: $d" "not in the log"; fi
  done
  if grep -qE 'panic|level=ERROR' "$LOG"; then bad "log" "errors in the server log: $(grep -E 'panic|level=ERROR' "$LOG" | head -3 | cut -c1-200)"; else ok "no errors or panics in the log"; fi
fi
kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null; PID=""

# Each copy is broken in one way and must refuse to start, naming the cause.
step "broken copies must refuse to start"
broken() { # NAME EXPECTED_MESSAGE EDIT_COMMAND...
  local name=$1 want=$2; shift 2
  local dir=$WORK/broken-$name
  mkdir -p "$dir" && cp -R config scripts "$dir"/
  ( cd "$dir" && "$@" )
  local out; out=$(timeout 60 "$AEGIS" run -addr ":$(free_port)" "$dir" 2>&1); local rc=$?
  if [ "$rc" -eq 0 ] || [ "$rc" -eq 124 ]; then bad "$name" "started (exit $rc) but should have been refused"
  elif ! grep -qF -- "$want" <<<"$out"; then bad "$name" "refused, but not for the expected reason ($want): $(grep -E 'ref/platform|Error|error' <<<"$out" | head -2 | cut -c1-220)"
  else ok "$name is refused"; fi
}
broken open-handler   "without authorization" \
  sed -i.bak -e '/handler "quote"/,/^}/{/auth "api_auth"/d;/authz {/d;}' config/10_definitions.bcl
broken typo-param     "is not a param" \
  sed -i.bak -e 's/config { threshold 500 }/config { thresold 500 }/' config/20_flows.bcl
broken over-ceiling   "ceiling" \
  sed -i.bak -e 's/max_steps 200000/max_steps 10/; s#file "scripts/risk_score.spl"#file "scripts/risk_score.spl" max_steps 500#' config/10_definitions.bcl
broken shadow-builtin "shadow" \
  sed -i.bak -e 's/node_type "risk.score"/node_type "collect"/; s/uses "risk.score"/uses "collect"/' config/10_definitions.bcl config/20_flows.bcl
broken disk-under-ephemeral "storage_policy" \
  sed -i.bak -e '/^storage_policy/a\
resource "disk_cache" { kind "cache.file" config { dir ".data/cache" } }
' config/00_app.bcl

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
