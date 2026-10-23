#!/usr/bin/env bash
# Checks phase 4 of docs/aegis-protection-plan.md for real: a control server
# that survives a restart, a package registered with it, `aegis run
# -grant-server` fetching the key instead of a local -key, revocation taking
# effect immediately, and the rate limiter actually limiting.
#
#   ./check.sh
set -u
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
WORK=$(mktemp -d)
pass=0 fail=0
ok()  { pass=$((pass + 1)); printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  \033[31mFAIL\033[0m %s\n         %s\n' "$1" "$2"; }
step() { printf '\n== %s\n' "$1"; }
cleanup() {
  [ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null
  [ -n "${SERVE_PID2:-}" ] && kill "$SERVE_PID2" 2>/dev/null
  [ -n "${SERVE_PID3:-}" ] && kill "$SERVE_PID3" 2>/dev/null
  [ "$fail" -eq 0 ] && rm -rf "$WORK" || echo "kept for inspection: $WORK"
}
trap cleanup EXIT

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'; }
wait_healthy() { # port
  for _ in $(seq 1 50); do
    [ "$(curl -s -m 1 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$1/healthz" 2>/dev/null)" = 200 ] && return 0
    sleep 0.2
  done
  return 1
}

step "build aegis"
( cd "$ROOT" && go build -o "$WORK/aegis" ./aegis/cmd/aegis ) && ok "built" || { bad "build" "see above"; exit 1; }
AEGIS="$WORK/aegis"

step "keygen, compile, package"
"$AEGIS" keygen -out "$WORK/k" >"$WORK/keygen.log" 2>&1 && ok "keygen" || { bad "keygen" "$(cat "$WORK/keygen.log")"; exit 1; }
"$AEGIS" compile -src math/main.go -out "$WORK/math.air" >"$WORK/compile.log" 2>&1 && ok "compile" || { bad "compile" "$(cat "$WORK/compile.log")"; exit 1; }
"$AEGIS" package -module "$WORK/math.air" -key "$WORK/k.key" -enckey "$WORK/k.enc" -out "$WORK/math.apkg" >"$WORK/package.log" 2>&1 \
  && ok "package" || { bad "package" "$(cat "$WORK/package.log")"; exit 1; }

step "start a persistent control server"
# The main flow below makes several legitimate requests in quick succession
# (register, two runs, two refusal attempts, a status check, a revoke); a
# realistic limit (the CLI's own default: 120/min, burst 20) must not get in
# the way of that. The limiter's own enforcement is proven separately, in
# isolation, further down — not by starving this server's real traffic.
PORT=$(free_port)
STORE="$WORK/control.db"
"$AEGIS" serve -addr ":$PORT" -store "$STORE" -key "$WORK/k.key" >"$WORK/serve.log" 2>&1 &
SERVE_PID=$!
if wait_healthy "$PORT"; then ok "serving on :$PORT with -store $STORE"; else bad "serve" "$(cat "$WORK/serve.log")"; exit 1; fi

step "register and run via -grant-server"
REG_OUT=$("$AEGIS" register -pkg "$WORK/math.apkg" -enckey "$WORK/k.enc" -server "http://127.0.0.1:$PORT" 2>&1)
if echo "$REG_OUT" | grep -q "Registered package"; then ok "registered: $REG_OUT"; else bad "register" "$REG_OUT"; fi

RUN_OUT=$("$AEGIS" run -pkg "$WORK/math.apkg" -grant-server "http://127.0.0.1:$PORT" -pubkey "$WORK/k.pub" -func Add -args 10,20 2>&1)
if echo "$RUN_OUT" | grep -q "Result: 30" && echo "$RUN_OUT" | grep -q "\[Grant\] obtained"; then
  ok "Add(10,20) via grant-server = 30"
else
  bad "run via grant-server" "$RUN_OUT"
fi

RUN_OUT2=$("$AEGIS" run -pkg "$WORK/math.apkg" -grant-server "http://127.0.0.1:$PORT" -pubkey "$WORK/k.pub" -func Multiply -args 6,7 2>&1)
if echo "$RUN_OUT2" | grep -q "Result: 42"; then ok "Multiply(6,7) via grant-server = 42"; else bad "run Multiply" "$RUN_OUT2"; fi

step "refusals"
if "$AEGIS" run -pkg "$WORK/math.apkg" -key "$WORK/k.enc" -grant-server "http://127.0.0.1:$PORT" -pubkey "$WORK/k.pub" -func Add -args 1,1 >/dev/null 2>&1; then
  bad "key and grant-server together" "expected a refusal, got success"
else
  ok "-key and -grant-server together are refused"
fi
if "$AEGIS" run -pkg "$WORK/math.apkg" -grant-server "http://127.0.0.1:$PORT" -pubkey "$WORK/k.pub" -func NoSuchFunc >/dev/null 2>&1; then
  bad "unknown function" "expected a refusal"
else
  ok "running an unknown function is refused"
fi

step "revocation takes effect immediately"
INSPECT_OUT=$("$AEGIS" inspect -pkg "$WORK/math.apkg" 2>&1)
PKG_ID=$(echo "$INSPECT_OUT" | grep "Package ID" | cut -d: -f2 | tr -d ' ')
if [ -z "$PKG_ID" ]; then bad "extract package id" "$INSPECT_OUT"; else ok "package id: $PKG_ID"; fi

REVOKE_OUT=$("$AEGIS" revoke -package-id "$PKG_ID" -reason "check.sh test" -server "http://127.0.0.1:$PORT" 2>&1)
if echo "$REVOKE_OUT" | grep -q "Revoked package"; then ok "revoked: $REVOKE_OUT"; else bad "revoke" "$REVOKE_OUT"; fi

if "$AEGIS" run -pkg "$WORK/math.apkg" -grant-server "http://127.0.0.1:$PORT" -pubkey "$WORK/k.pub" -func Add -args 1,1 >/dev/null 2>&1; then
  bad "run after revoke" "expected a refusal, the package is revoked"
else
  ok "run after revoke is refused"
fi

step "persistence: restart the server against the same store"
kill "$SERVE_PID" 2>/dev/null; wait "$SERVE_PID" 2>/dev/null; unset SERVE_PID
PORT2=$(free_port)
"$AEGIS" serve -addr ":$PORT2" -store "$STORE" -key "$WORK/k.key" >"$WORK/serve2.log" 2>&1 &
SERVE_PID2=$!
if wait_healthy "$PORT2"; then ok "restarted on :$PORT2 against the same store file"; else bad "restart" "$(cat "$WORK/serve2.log")"; exit 1; fi

if "$AEGIS" run -pkg "$WORK/math.apkg" -grant-server "http://127.0.0.1:$PORT2" -pubkey "$WORK/k.pub" -func Add -args 1,1 >/dev/null 2>&1; then
  bad "revocation survived restart" "the package ran after restart; it should still be revoked"
else
  ok "the revocation survived the restart (still refused)"
fi

# A second package, registered against the restarted server, still works —
# proving the restart broke nothing else.
"$AEGIS" keygen -out "$WORK/k2" >/dev/null 2>&1
"$AEGIS" package -module "$WORK/math.air" -key "$WORK/k2.key" -enckey "$WORK/k2.enc" -out "$WORK/math2.apkg" >/dev/null 2>&1
"$AEGIS" register -pkg "$WORK/math2.apkg" -enckey "$WORK/k2.enc" -server "http://127.0.0.1:$PORT2" >/dev/null 2>&1
RUN_OUT3=$("$AEGIS" run -pkg "$WORK/math2.apkg" -grant-server "http://127.0.0.1:$PORT2" -pubkey "$WORK/k2.pub" -func Add -args 3,4 2>&1)
if echo "$RUN_OUT3" | grep -q "Result: 7"; then
  ok "a new package registered after restart still works"
else
  bad "new package after restart" "$RUN_OUT3"
fi
kill "$SERVE_PID2" 2>/dev/null; wait "$SERVE_PID2" 2>/dev/null; unset SERVE_PID2

step "rate limiting, in isolation on its own server and port"
# A fresh server, a fresh store, a tiny burst, and nothing else ever talking
# to it: the only traffic against this port is this test's own.
PORT3=$(free_port)
"$AEGIS" serve -addr ":$PORT3" -key "$WORK/k.key" -rate-limit 5 -rate-burst 3 >"$WORK/serve3.log" 2>&1 &
SERVE_PID3=$!
if wait_healthy "$PORT3"; then ok "isolated server on :$PORT3 (-rate-limit 5 -rate-burst 3)"; else bad "serve3" "$(cat "$WORK/serve3.log")"; exit 1; fi

codes=""
for _ in 1 2 3 4 5; do
  codes="$codes $(curl -s -m 2 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT3/v1/package/nonexistent/status")"
done
if echo "$codes" | grep -q 429; then ok "burst of 3 exceeded by the 4th/5th request: codes were [$codes]"; else bad "rate limit" "expected a 429 among 5 rapid requests, got [$codes]"; fi

healthz_codes=""
for _ in 1 2 3 4 5; do
  healthz_codes="$healthz_codes $(curl -s -m 2 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT3/healthz")"
done
if echo "$healthz_codes" | grep -q 429; then
  bad "/healthz exemption" "a liveness probe must never be rate limited, got [$healthz_codes]"
else
  ok "/healthz is never rate limited, even right after exhausting the burst above: [$healthz_codes]"
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
